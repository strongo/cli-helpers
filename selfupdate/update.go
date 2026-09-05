package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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
	// ActionPlanned means Options.DryRun was set and Update stopped before
	// any manager process, download, or write. PlannedCommand names a
	// managed operation; PlannedURL names a manual-install asset
	// (REQ: dry-run).
	ActionPlanned
	// ActionManagerExecuted means the configured package-manager command
	// exited successfully. The manager remains the install authority; the
	// core did not download or replace the executable itself.
	ActionManagerExecuted
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
	case ActionManagerExecuted:
		return "manager_executed"
	default:
		return "unknown"
	}
}

// Outcome describes what Update did. Result and Target are only meaningful
// for the actions that actually compared or resolved a version
// (ActionAlreadyCurrent, ActionUpdated, and manual ActionAborted/
// ActionPlanned); for managed outcomes and every error return, they are left
// at their zero value and the caller should look at Detection.Manager instead.
type Outcome struct {
	// Action is what happened.
	Action Action
	// Detection is how the running binary's install was classified.
	Detection Detection
	// Result is the version comparison that led to Action, when one was
	// performed.
	Result CheckResult
	// ReleaseCheckWarning records an advisory failure while looking up the
	// latest published release for a managed install. The package manager
	// remains the update authority, so this warning never prevents a redirect
	// or configured manager command from proceeding.
	ReleaseCheckWarning error
	// Target is the normalized version that was (or would be, or was
	// declined to be) installed.
	Target string
	// Downgrade is true when Target orders below the running version, i.e.
	// this was a downgrade (only possible via a pinned Options.
	// PinnedVersion with AllowDowngrade set).
	Downgrade bool
	// PlannedURL is the exact asset URL a non-dry-run call would have
	// fetched for a manual install. Set only when Action is ActionPlanned.
	PlannedURL string
	// PlannedCommand is the exact display command an executable manager
	// would run. Set for a managed ActionPlanned outcome.
	PlannedCommand string
	// PostSwapWarning is set when Action is ActionUpdated and the post-swap
	// version probe did not confirm the expected version, or when
	// ActionManagerExecuted and the installed CLI could not be probed after
	// the manager command completed. The mutation already succeeded — this
	// is a warning to surface, not a failed Update.
	PostSwapWarning error
	// AfterUpdateWarning is set when Options.AfterUpdate could not resolve the
	// installed executable or returned an error. The binary update has already
	// completed, so this is a warning to surface separately, never an Update
	// failure.
	AfterUpdateWarning error
}

// ExecutableIdentity identifies the executable a post-update integration can
// invoke after a successful update. Path is the absolute invocation path;
// ResolvedPath is that path after symlinks are followed. Manager-owned updates
// resolve both only after the package-manager command completes, so they track
// cask or version-directory changes instead of retaining the old path.
type ExecutableIdentity struct {
	Path         string
	ResolvedPath string
}

// AfterUpdate is supplied to Options.AfterUpdate after an update reaches a
// successful terminal outcome. Outcome is the completed update receipt and
// Executable identifies the installed binary that an integration may reexec.
type AfterUpdate struct {
	Outcome    Outcome
	Executable ExecutableIdentity
}

// AfterUpdateFunc runs after a successful self-update outcome. Its error is
// retained as Outcome.AfterUpdateWarning because the binary update is already
// complete and must not be reported as failed.
type AfterUpdateFunc func(ctx context.Context, update AfterUpdate) error

// ManagedCommandRunner executes a configured package-manager program and argv.
// The core deliberately owns no process I/O; command adapters provide a runner
// that wires stdin/stdout/stderr according to their own output contract.
type ManagedCommandRunner func(ctx context.Context, executable string, args []string) error

// ManagedBinaryVerifier probes the CLI after a successful package-manager
// command. A failure becomes Outcome.PostSwapWarning because the manager
// command has already completed.
type ManagedBinaryVerifier func(ctx context.Context, binary string, args []string, expectedVersion string) error

// Availability is the structured version information reported before an
// update confirmation or package-manager command. Pinned is true when Target
// names the requested release rather than the latest stable release. Warning
// means the managed-release lookup was unavailable; Result.Current remains
// useful, but Result.Latest is empty and must not be presented as a version.
type Availability struct {
	Result    CheckResult
	Target    string
	Pinned    bool
	Detection Detection
	Warning   error
}

// AvailabilityReporter receives version information before any confirmation
// or mutation. It is a callback so the core remains framework- and I/O-free.
type AvailabilityReporter func(Availability)

// Options controls one Update call. For a manual install, the zero value (no
// pin, no downgrade allowance, DryRun false, Confirm nil) updates
// unconditionally to the latest stable release with no confirmation gate.
// An executable managed install additionally requires RunManaged and
// VerifyManaged; see Confirm's doc for the interactive case.
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
	// RunManaged is required when the detected Manager opted into executable
	// upgrades. It receives structured argv, never a shell command string.
	RunManaged ManagedCommandRunner
	// VerifyManaged is required alongside RunManaged and probes the CLI found
	// after the manager command using Config.VersionProbeArgs.
	VerifyManaged ManagedBinaryVerifier
	// ReportAvailability is called once after version information is resolved
	// and before confirmation or mutation. Its callback is advisory output only;
	// it cannot alter update control flow.
	ReportAvailability AvailabilityReporter
	// AfterUpdate runs only after an actual successful update, an already-current
	// result, or a successful executable package-manager update. It receives the
	// resolved installed executable identity so integrations can reexec the new
	// binary. An error becomes Outcome.AfterUpdateWarning; it never changes a
	// completed binary update into a failure.
	AfterUpdate AfterUpdateFunc
}

var (
	execLookPath               = exec.LookPath
	absPath                    = filepath.Abs
	managedAvailabilityTimeout = 5 * time.Second
)

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
		if opts.PinnedVersion != "" {
			managerName := "the package manager"
			if detection.Manager != nil {
				managerName = detection.Manager.Name
			}
			return Outcome{Detection: detection}, &Failure{
				Kind: KindManagedVersion,
				Err:  fmt.Errorf("%s cannot install the requested version %q; package-manager updates do not support release pins", managerName, opts.PinnedVersion),
			}
		}
		availability := cfg.managedAvailability(ctx, detection)
		cfg.reportAvailability(opts, availability)
		// REQ: managed-no-overwrite — this branch never reaches the
		// download/write path below. Redirect-only managers preserve the
		// original behavior exactly; executable managers remain under the
		// package manager's authority through the structured runner.
		if detection.Manager == nil || !detection.Manager.CanExecuteUpgrade() {
			return Outcome{Action: ActionRedirected, Detection: detection, Result: availability.Result, ReleaseCheckWarning: availability.Warning}, nil
		}
		return cfg.updateManaged(ctx, opts, detection, availability)
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
		target = cfg.versionFromTag(resolvedTag)

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
		cfg.reportAvailability(opts, Availability{Result: result, Target: target, Pinned: true, Detection: detection})
	} else {
		latestTag, err := cfg.latestStableTag(ctx)
		if err != nil {
			return Outcome{Detection: detection}, &Failure{Kind: KindReleaseLookup, Err: err}
		}
		tag = latestTag
		target = cfg.versionFromTag(latestTag)
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
			cfg.reportAvailability(opts, Availability{Result: result, Target: target, Detection: detection})
			// REQ: no-op-when-current.
			outcome := Outcome{Action: ActionAlreadyCurrent, Detection: detection, Result: result}
			cfg.runAfterUpdate(ctx, opts, &outcome)
			return outcome, nil
		}
		cfg.reportAvailability(opts, Availability{Result: result, Target: target, Detection: detection})
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
	cfg.runAfterUpdate(ctx, opts, &outcome)
	return outcome, nil
}

func (c Config) updateManaged(ctx context.Context, opts Options, detection Detection, availability Availability) (Outcome, error) {
	m := detection.Manager
	base := Outcome{Detection: detection, Result: availability.Result, ReleaseCheckWarning: availability.Warning}

	if opts.DryRun {
		return Outcome{
			Action:              ActionPlanned,
			Detection:           base.Detection,
			Result:              base.Result,
			ReleaseCheckWarning: base.ReleaseCheckWarning,
			PlannedCommand:      m.UpgradeCommand,
		}, nil
	}

	if opts.RunManaged == nil {
		return base, &Failure{
			Kind: KindManagedCommand,
			Err:  errors.New("managed update runner is not configured"),
		}
	}
	if opts.VerifyManaged == nil {
		return base, &Failure{
			Kind: KindManagedCommand,
			Err:  errors.New("managed post-update version probe is not configured"),
		}
	}

	transition := fmt.Sprintf("%s will upgrade %s by running: %s", m.Name, c.BinaryName, m.UpgradeCommand)
	if opts.Confirm != nil {
		proceed, err := opts.Confirm(transition)
		if err != nil {
			var f *Failure
			if errors.As(err, &f) {
				return base, f
			}
			return base, &Failure{Kind: KindUnexpected, Err: err}
		}
		if !proceed {
			return Outcome{Action: ActionAborted, Detection: detection, Result: base.Result, ReleaseCheckWarning: base.ReleaseCheckWarning}, nil
		}
	}

	steps := m.executableUpgradeSteps()
	for index, step := range steps {
		if err := opts.RunManaged(ctx, step.Executable, step.Args); err != nil {
			return base, &Failure{
				Kind: KindManagedCommand,
				Err:  fmt.Errorf("run managed update step %d/%d (%s): %w", index+1, len(steps), step.Executable, err),
			}
		}
	}

	outcome := Outcome{Action: ActionManagerExecuted, Detection: detection, Result: base.Result, ReleaseCheckWarning: base.ReleaseCheckWarning}
	probeArgs := append([]string(nil), c.VersionProbeArgs...)
	if err := opts.VerifyManaged(ctx, c.BinaryName, probeArgs, availability.Target); err != nil {
		outcome.PostSwapWarning = err
	}
	c.runAfterUpdate(ctx, opts, &outcome)
	return outcome, nil
}

func (c Config) managedAvailability(ctx context.Context, detection Detection) Availability {
	lookupCtx, cancel := context.WithTimeout(ctx, managedAvailabilityTimeout)
	defer cancel()
	result, err := c.Check(lookupCtx)
	if err != nil {
		current := c.CurrentVersion
		if !c.isUndetermined(current) {
			current = normalize(current)
		}
		return Availability{Result: CheckResult{Current: current}, Detection: detection, Warning: err}
	}
	return Availability{Result: result, Target: result.Latest, Detection: detection}
}

func (c Config) reportAvailability(opts Options, availability Availability) {
	if opts.ReportAvailability != nil {
		opts.ReportAvailability(availability)
	}
}

func (c Config) runAfterUpdate(ctx context.Context, opts Options, outcome *Outcome) {
	if opts.AfterUpdate == nil || opts.DryRun || !isAfterUpdateAction(outcome.Action) {
		return
	}
	executable, err := c.installedExecutable(outcome)
	if err != nil {
		outcome.AfterUpdateWarning = fmt.Errorf("resolve installed executable after self-update: %w", err)
		return
	}
	if err := opts.AfterUpdate(ctx, AfterUpdate{Outcome: *outcome, Executable: executable}); err != nil {
		outcome.AfterUpdateWarning = err
	}
}

func isAfterUpdateAction(action Action) bool {
	return action == ActionUpdated || action == ActionAlreadyCurrent || action == ActionManagerExecuted
}

func (c Config) installedExecutable(outcome *Outcome) (ExecutableIdentity, error) {
	path := outcome.Detection.Path
	if outcome.Action == ActionManagerExecuted {
		var err error
		path, err = execLookPath(c.BinaryName)
		if err != nil {
			return ExecutableIdentity{}, err
		}
	}
	if !filepath.IsAbs(path) {
		var err error
		path, err = absPath(path)
		if err != nil {
			return ExecutableIdentity{}, err
		}
	}
	resolved, err := evalSymlinksFunc(path)
	if err != nil {
		return ExecutableIdentity{}, fmt.Errorf("resolve installed executable %q: %w", path, err)
	}
	return ExecutableIdentity{Path: path, ResolvedPath: resolved}, nil
}

// buildTransition renders the human-readable version-change description
// passed to Options.Confirm.
func buildTransition(current, target string, downgrade bool) string {
	if downgrade {
		return fmt.Sprintf("downgrade: %s → %s", current, target)
	}
	return fmt.Sprintf("%s → %s", current, target)
}
