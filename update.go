package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Action is what Update actually did (or, for a dry run, would do).
type Action int

const (
	// ActionRedirected means a managed install was detected; nothing was
	// downloaded, written, or replaced.
	ActionRedirected Action = iota
	// ActionAlreadyCurrent means the running version already equals the
	// latest stable release; nothing was downloaded or replaced.
	ActionAlreadyCurrent
	// ActionUpdated means the binary was downloaded, verified, and swapped.
	ActionUpdated
	// ActionAborted means Options.Confirm was called and declined (returned
	// false, nil) — the caller chose not to proceed, as opposed to Update
	// refusing on its own account. Nothing was downloaded or replaced.
	ActionAborted
	// ActionPlanned means Options.DryRun was set: Update walked the full
	// decision path — detection, target resolution, version comparison, the
	// downgrade guard — and stopped before any download or write. Outcome.
	// PlannedURL names exactly what a real run would have fetched
	// (REQ: dry-run).
	ActionPlanned
)

// String renders the action as a stable, lower_snake_case token suitable
// for machine-readable output.
func (a Action) String() string {
	switch a {
	case ActionRedirected:
		return "redirected"
	case ActionAlreadyCurrent:
		return "already_current"
	case ActionUpdated:
		return "updated"
	case ActionAborted:
		return "aborted"
	case ActionPlanned:
		return "planned"
	default:
		return "unknown"
	}
}

// Outcome describes what Update did. Result and Target are only meaningful
// for the actions that actually compared or resolved a version
// (ActionAlreadyCurrent, ActionUpdated, ActionAborted, ActionPlanned); for
// ActionRedirected, and for every error return, they are left at their zero
// value and the caller should look at Detection.Manager instead.
type Outcome struct {
	// Action is what happened.
	Action Action
	// Detection is how the running binary's install was classified.
	Detection Detection
	// Result is the version comparison that led to Action, when one was
	// performed.
	Result CheckResult
	// Target is the normalized version that was (or would be, or was
	// declined to be) installed.
	Target string
	// Downgrade is true when Target orders below the running version, i.e.
	// this was a downgrade (only possible via a pinned Options.
	// PinnedVersion with AllowDowngrade set).
	Downgrade bool
	// PlannedURL is the exact asset URL a non-dry-run call would have
	// fetched. Set only when Action is ActionPlanned.
	PlannedURL string
	// PostSwapWarning is set when Action is ActionUpdated but the post-swap
	// version probe (REQ: post-swap-version-check) did not confirm the
	// expected version. The swap itself already succeeded — this is a
	// warning to surface to the user, not a reason to treat the call as
	// failed, so it is reported here rather than as Update's error return.
	PostSwapWarning error
}

// Options controls one Update call. The zero value (no pin, no downgrade
// allowance, DryRun false, Confirm nil) updates unconditionally to the
// latest stable release with no confirmation gate at all — that's the right
// default for a caller that has already obtained consent some other way;
// see Confirm's doc for the interactive case.
type Options struct {
	// PinnedVersion, when non-empty, installs exactly that release instead
	// of the latest stable one (REQ: version-pin). A leading "v" is
	// optional.
	PinnedVersion string
	// AllowDowngrade permits a PinnedVersion that orders below the running
	// version (REQ: pinned-downgrade-guard). Ignored when PinnedVersion is
	// empty, and ignored when the running version is undetermined (there is
	// no direction to guard).
	AllowDowngrade bool
	// DryRun walks the full decision path and stops before any download or
	// write (REQ: dry-run). See ActionPlanned.
	DryRun bool
	// Confirm, when non-nil, is called with a human-readable description of
	// the version transition (e.g. "1.0.0 → 1.1.0", or "downgrade: 1.1.0 →
	// 1.0.0") before any download begins, and must return whether to
	// proceed. This is the ONLY place Update touches anything resembling
	// user interaction, and it does none of the interaction itself
	// (REQ: no-io-side-effects-in-core) — prompting, or deciding to skip the
	// prompt because a --yes flag was given, or refusing because no
	// terminal is attached (REQ: non-interactive-refusal), all belong to
	// Confirm's implementation. A refusal like the non-interactive one is
	// reported by returning a *Failure (e.g. {Kind: KindNonInteractive})
	// as the error, which Update passes straight through; returning
	// (false, nil) instead means "the user was asked and said no", which
	// Update reports as ActionAborted with a nil error, not a failure.
	// Nil means no confirmation gate at all — Update proceeds immediately.
	Confirm func(transition string) (bool, error)
}

// Update resolves the target release (the latest stable release, or an
// exact pin), and — unless the install is managed, the platform is
// unsupported, the downgrade guard refuses, Options.DryRun stops it first,
// or Options.Confirm declines — downloads, verifies, and atomically swaps
// the running binary.
//
// Every return, error or not, carries the Detection so a caller can build
// its own message without a second DetectSelf call.
func (c Config) Update(ctx context.Context, opts Options) (Outcome, error) {
	cfg := c.withDefaults()

	detection, err := cfg.DetectSelf()
	if err != nil {
		return Outcome{}, &Failure{Kind: KindUnexpected, Err: fmt.Errorf("resolve running executable: %w", err)}
	}

	if detection.Method == Managed {
		// REQ: managed-no-overwrite — return before opts (a pin, a skipped
		// confirmation, DryRun) are even inspected, so no option
		// combination can reach the download/write path below.
		return Outcome{Action: ActionRedirected, Detection: detection}, nil
	}
	if detection.Method != Manual {
		// Ambiguous, or any value that is not a recognized Manual
		// classification: REQ: ambiguous-safe-default — never resolves to
		// Manual.
		return Outcome{Detection: detection},
			&Failure{Kind: KindAmbiguous, Path: detection.Path, Err: errors.New("install method is ambiguous; refusing to self-replace")}
	}

	if !cfg.platformSupported() {
		return Outcome{Detection: detection},
			&Failure{Kind: KindUnsupportedPlatform, Err: fmt.Errorf("no published asset for %s/%s", goosName, goarchName)}
	}

	undetermined := cfg.isUndetermined(cfg.CurrentVersion)

	var (
		tag       string
		target    string
		result    CheckResult
		downgrade bool
	)

	if pinned := opts.PinnedVersion; pinned != "" {
		resolvedTag, err := cfg.resolveTag(ctx, pinned)
		if err != nil {
			return Outcome{Detection: detection}, err
		}
		tag = resolvedTag
		target = normalize(resolvedTag)

		result.Latest = target
		if undetermined {
			result.Current = cfg.CurrentVersion
			result.Verdict = Undetermined
		} else {
			result.Current = normalize(cfg.CurrentVersion)
			result.Verdict = UpdateAvailable
			if CompareVersions(target, result.Current) < 0 {
				downgrade = true
				if !opts.AllowDowngrade {
					return Outcome{Detection: detection, Result: result, Target: target, Downgrade: true},
						&Failure{Kind: KindDowngrade, Err: fmt.Errorf("refusing to downgrade from %s to %s", result.Current, target)}
				}
			}
		}
	} else {
		latestTag, err := cfg.latestStableTag(ctx)
		if err != nil {
			return Outcome{Detection: detection}, &Failure{Kind: KindReleaseLookup, Err: err}
		}
		tag = latestTag
		target = normalize(latestTag)
		result.Latest = target

		if undetermined {
			result.Current = cfg.CurrentVersion
			result.Verdict = Undetermined
		} else {
			result.Current = normalize(cfg.CurrentVersion)
			result.Verdict = UpdateAvailable
			if CompareVersions(result.Current, target) == 0 {
				result.Verdict = UpToDate
			}
		}

		if result.Verdict == UpToDate {
			// REQ: no-op-when-current.
			return Outcome{Action: ActionAlreadyCurrent, Detection: detection, Result: result}, nil
		}
	}

	transition := buildTransition(result.Current, target, downgrade)

	if opts.DryRun {
		asset := cfg.AssetName(cfg.BinaryName, target, goosName, goarchName)
		return Outcome{
			Action:     ActionPlanned,
			Detection:  detection,
			Result:     result,
			Target:     target,
			Downgrade:  downgrade,
			PlannedURL: cfg.DownloadURL(cfg.Repository, tag, asset),
		}, nil
	}

	if opts.Confirm != nil {
		proceed, err := opts.Confirm(transition)
		if err != nil {
			var f *Failure
			if errors.As(err, &f) {
				return Outcome{Detection: detection, Result: result, Target: target, Downgrade: downgrade}, f
			}
			return Outcome{Detection: detection, Result: result, Target: target, Downgrade: downgrade},
				&Failure{Kind: KindUnexpected, Err: err}
		}
		if !proceed {
			return Outcome{Action: ActionAborted, Detection: detection, Result: result, Target: target, Downgrade: downgrade}, nil
		}
	}

	tmpPath, err := cfg.downloadAndVerify(ctx, tag, target)
	if err != nil {
		return Outcome{Detection: detection, Result: result, Target: target, Downgrade: downgrade}, err
	}
	defer func() { _ = os.Remove(tmpPath) }()

	if err := replaceExecutable(cfg.BinaryName, detection.Path, tmpPath); err != nil {
		kind := KindUnexpected
		if errors.Is(err, fs.ErrPermission) {
			kind = KindPermission
		}
		return Outcome{Detection: detection, Result: result, Target: target, Downgrade: downgrade},
			&Failure{Kind: kind, Path: detection.Path, Err: err}
	}

	outcome := Outcome{Action: ActionUpdated, Detection: detection, Result: result, Target: target, Downgrade: downgrade}
	if err := verifyBinaryVersion(detection.Path, cfg.VersionProbeArgs, target); err != nil {
		// The swap already succeeded; a failed confirmation is reported on
		// the outcome, not treated as a failed Update (REQ: post-swap-
		// version-check).
		outcome.PostSwapWarning = err
	}
	return outcome, nil
}

// buildTransition renders the human-readable version-change description
// passed to Options.Confirm.
func buildTransition(current, target string, downgrade bool) string {
	if downgrade {
		return fmt.Sprintf("downgrade: %s → %s", current, target)
	}
	return fmt.Sprintf("%s → %s", current, target)
}
