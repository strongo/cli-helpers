package cliinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/strongo/cli-helpers/selfupdate"
)

// UpgradeOutcome classifies one named target's result from a batch
// PlanUpgrade/CheckUpgrades/Upgrade call (cli-install#req:upgrade-batch-
// semantics: "per-target results (upgraded, manager executed, redirected,
// already current, ahead of latest, skipped non-release build, not
// installed, unrecognized, refused, dry run, declined, or failed)" — this
// type carries exactly those twelve tokens).
//
// Every non-terminal value below (everything except SkippedNonRelease,
// NotInstalled, Unrecognized) is produced by mapping
// selfupdate.Config.UpdateAt's own Outcome.Action or Failure — never by a
// decision this package makes on its own (cli-install#req:upgrade-per-
// target-policy: "handled by the Self-Update Library's policy"). See
// mapAction and PlanUpgrade's own doc comment.
type UpgradeOutcome int

const (
	// UpgradeOutcomeUpgraded means a manual install was verified,
	// downloaded and atomically replaced (selfupdate.ActionUpdated).
	UpgradeOutcomeUpgraded UpgradeOutcome = iota
	// UpgradeOutcomeManagerExecuted means an executable package-manager
	// update ran to completion (selfupdate.ActionManagerExecuted).
	UpgradeOutcomeManagerExecuted
	// UpgradeOutcomeRedirected means a redirect-only managed install's
	// upgrade command was reported without running anything
	// (selfupdate.ActionRedirected).
	UpgradeOutcomeRedirected
	// UpgradeOutcomeAlreadyCurrent means the running version already equals
	// the latest stable release; nothing changed
	// (selfupdate.ActionAlreadyCurrent). For the host, and only for a real
	// (non-dry-run, non-check) run, ExecuteUpgrade makes one further real
	// UpdateAt call for this row so its after-update hook still runs,
	// exactly as self-update does (REQ: host-target-is-running-binary;
	// task-22 review B2) — selfupdate.Config.UpdateAt's own runAfterUpdate
	// skips the hook under DryRun, so PlanUpgrade's own DryRun(true) call
	// alone never fires it.
	UpgradeOutcomeAlreadyCurrent
	// UpgradeOutcomeAhead means the installed version orders strictly above
	// the latest stable release (self-update#req:ahead-of-latest); nothing
	// changed and this never counts as an available upgrade
	// (selfupdate.ActionAhead).
	UpgradeOutcomeAhead
	// UpgradeOutcomeSkippedNonRelease means an --all- or report-sourced
	// target was a non-release build and was never looked up
	// (cli-install#req:upgrade-skips-non-release-builds).
	UpgradeOutcomeSkippedNonRelease
	// UpgradeOutcomeNotInstalled means an explicitly named target has no
	// located copy; InstallHint names the remedy.
	UpgradeOutcomeNotInstalled
	// UpgradeOutcomeUnrecognized means a located copy's identity could not
	// be confirmed; it is never touched
	// (cli-install#req:unrecognized-copy-not-trusted).
	UpgradeOutcomeUnrecognized
	// UpgradeOutcomeRefused means the install method is ambiguous
	// (selfupdate#req:ambiguous-safe-default): Failure always carries
	// selfupdate.KindAmbiguous, and this counts as a batch failure exactly
	// like UpgradeOutcomeFailed — see UpgradeBatchResult.Failed/Failure —
	// because `self-update` itself fails outright for an ambiguous install,
	// and `upgrade <self>`/`upgrade <name>` must reach the same verdict
	// (cli-install#req:self-update-equals-upgrade-self). It is computed
	// BEFORE any verdict is even considered: an ambiguous target is refused
	// whether it is current, ahead, or has an update available.
	UpgradeOutcomeRefused
	// UpgradeOutcomeDryRun means this target would be upgraded: it is
	// either --dry-run's own final answer, or PlanUpgrade's "still pending
	// confirmation" marker that ExecuteUpgrade replaces with a terminal
	// outcome (selfupdate.ActionPlanned, mapped 1:1).
	UpgradeOutcomeDryRun
	// UpgradeOutcomeDeclined means the batch confirmation was asked and
	// declined; nothing changed and this is not a failure.
	UpgradeOutcomeDeclined
	// UpgradeOutcomeFailed means resolving or applying this target's
	// upgrade failed with the typed Failure.
	UpgradeOutcomeFailed
)

// String renders UpgradeOutcome as a stable, lower_snake_case token,
// matching this module's other String() conventions.
func (o UpgradeOutcome) String() string {
	switch o {
	case UpgradeOutcomeUpgraded:
		return "upgraded"
	case UpgradeOutcomeManagerExecuted:
		return "manager_executed"
	case UpgradeOutcomeRedirected:
		return "redirected"
	case UpgradeOutcomeAlreadyCurrent:
		return "already_current"
	case UpgradeOutcomeAhead:
		return "ahead"
	case UpgradeOutcomeSkippedNonRelease:
		return "skipped_non_release_build"
	case UpgradeOutcomeNotInstalled:
		return "not_installed"
	case UpgradeOutcomeUnrecognized:
		return "unrecognized"
	case UpgradeOutcomeRefused:
		return "refused"
	case UpgradeOutcomeDryRun:
		return "dry_run"
	case UpgradeOutcomeDeclined:
		return "declined"
	case UpgradeOutcomeFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// UpgradeResult is one target's outcome from a batch PlanUpgrade/
// CheckUpgrades/ExecuteUpgrade/Upgrade call — the shape cliinstall/cliui's
// output writers flatten into cli-install#req:machine-readable-output's
// added upgrade fields ("current, latest, verdict, action, command and
// resolved_path").
type UpgradeResult struct {
	// Target is the catalog id this result describes.
	Target string
	// Host is true exactly for the row describing the running host itself
	// (cli-install#req:host-target-is-running-binary).
	Host bool
	// Outcome classifies what happened to Target — REQ: upgrade-batch-
	// semantics' "action".
	Outcome UpgradeOutcome

	// InstallMethod classifies Target's install (cli-install#req:upgrade-
	// per-target-policy): for a non-host target this follows DetectSelf's
	// own rule (managed on either the PATH-found or symlink-resolved path,
	// otherwise manual or ambiguous judged on the resolved path alone); for
	// the host it is literally selfupdate.Config.DetectSelf's own
	// classification of the running executable (REQ: host-target-is-
	// running-binary — task-22 review S1), not a rebuilt path. Meaningful
	// only when Target was located and Installed.
	InstallMethod selfupdate.InstallMethod
	// Manager identifies the owning package manager when InstallMethod is
	// selfupdate.Managed; nil otherwise.
	Manager *selfupdate.Manager
	// ResolvedPath is the file this upgrade acts on: the symlink-resolved
	// path for a Manual or Ambiguous classification (REQ: upgrade-per-
	// target-policy: "The path passed for replacement MUST be the
	// symlink-resolved path, so a symlink is kept"), or the located path
	// for a Managed one, whose file is never written directly.
	ResolvedPath string

	// Current is Target's installed version: the probed Status.Version for
	// a non-host target, or the host's own Config.CurrentVersion for the
	// host row — never the version a status probe of the host's own PATH
	// copy would have found (REQ: host-target-is-running-binary).
	Current string
	// Latest is the resolved latest stable release's normalized version,
	// set once a lookup has completed.
	Latest string
	// Tag is Latest's exact published tag, passed to
	// selfupdate.Options.ResolvedTag so Execute never independently
	// re-searches for "latest" a second time (cli-install#req:upgrade-
	// resolves-release-once) — UpdateAt's own re-verification that this
	// tag is STILL latest is a separate, deliberate check documented on
	// that call, not a second search.
	Tag string
	// Verdict is the comparison between Current and Latest, set once a
	// lookup has completed. Zero (selfupdate.UpToDate) when no lookup ran.
	Verdict selfupdate.Verdict

	// Command is the manager's display upgrade command, set whenever
	// Manager is non-nil, regardless of Outcome. Empty for a manager that
	// carries only Hint instead — e.g. the built-in system-package manager,
	// which has no single copy-pasteable command (see selfupdate.Manager.
	// UpgradeHint). A renderer MUST NOT print an empty Command after a
	// "Run:"-style prefix.
	Command string
	// Hint is the manager's UpgradeHint — human-readable prose naming how
	// to update when there is no single Command to print — set whenever
	// Manager is non-nil, regardless of Outcome. At most one of Command and
	// Hint is normally non-empty; a renderer shows Hint in a natural
	// sentence, never after a "Run:" prefix the way Command is.
	Hint string
	// AssetURL is the exact release-asset URL a pending manual upgrade
	// would fetch, taken from selfupdate.Outcome.PlannedURL
	// (cli-install#req:upgrade-batch-semantics: "version transition, asset
	// URL and path" — task-22 review S3). Empty for a managed target (its
	// Command already names the action) and for any terminal outcome that
	// never reached a planned replacement.
	AssetURL string
	// NonReleaseBuild is true when Current was classified a non-release
	// build (cli-install#req:upgrade-skips-non-release-builds) and this
	// target was offered anyway because it was named explicitly — the
	// confirmation prompt tags such a target by name (task-22 review S3).
	NonReleaseBuild bool
	// InstallHint names the remedy for UpgradeOutcomeNotInstalled:
	// "<host> install <target>".
	InstallHint string
	// FinishHint names the remedy for a non-host target whose catalog entry
	// declares SelfUpdateHooks and was Upgraded or had its manager command
	// executed: "<target> self-update" (cli-install#req:self-update-hook-
	// hint). Empty otherwise; upgrade never runs another CLI's hooks
	// itself.
	FinishHint string

	// Status is Target's probed install state, as REQ: status-probe-order
	// found it before this upgrade acted (or, for a target this upgrade
	// never touches, its only state). Always zero for the host: the host's
	// classification and version come from DetectSelf/Config, never from a
	// status probe of its own PATH copy (task-22 review M2) — a separate
	// PATH copy, if one exists, is named only in Warnings and OtherPaths.
	Status Status
	// OtherPaths lists additional located copies of Target, exactly as
	// Status.OtherPaths does for a non-host row; for the host row this is
	// the probed PATH copy (if any) instead, since Status itself is zero.
	OtherPaths []string
	// Failure is set exactly when Outcome is UpgradeOutcomeFailed or
	// UpgradeOutcomeRefused.
	Failure *selfupdate.Failure
	// Warnings are human-readable, non-fatal notes: a non-release-build
	// notice, an ambiguous-install guidance message, another-PATH-copy
	// warning for the host, a post-swap or after-update warning, a finish
	// hint's own prose, or Status's own warnings.
	Warnings []string
}

// UpgradeBatchResult is the outcome of one batch PlanUpgrade/CheckUpgrades/
// ExecuteUpgrade/Upgrade call: the running host's own catalog id and one
// UpgradeResult per target, in processing order (host always last per
// cli-install#req:host-upgraded-last).
type UpgradeBatchResult struct {
	Host    string
	Results []UpgradeResult
}

// upgradeResultFailed reports whether r counts as a batch failure:
// UpgradeOutcomeFailed always does, and so does UpgradeOutcomeRefused when
// it carries a Failure (task-22 review B1 — an ambiguous install fails
// `self-update` outright, so it must fail `upgrade` too). A refused row
// with no Failure never occurs in practice — Refused always carries
// selfupdate.KindAmbiguous — but the nil-guard keeps this symmetric with
// BatchResult.Failure's own "only failures with a Failure count" rule.
func upgradeResultFailed(r UpgradeResult) bool {
	if r.Failure == nil {
		return false
	}
	return r.Outcome == UpgradeOutcomeFailed || r.Outcome == UpgradeOutcomeRefused
}

// Failed reports whether at least one Result counts as a failure per
// upgradeResultFailed — mirroring BatchResult.Failed(). An unrecognized,
// not-installed, ahead, or skipped target is a descriptive state, not
// something that went wrong this run; a refused (ambiguous) one is.
func (b UpgradeBatchResult) Failed() bool {
	for _, r := range b.Results {
		if upgradeResultFailed(r) {
			return true
		}
	}
	return false
}

// Failure returns nil when b did not fail, and otherwise a *BatchFailure
// carrying every failed target's typed *selfupdate.Failure (including every
// refused/ambiguous one) — the same aggregate type BatchResult.Failure
// returns, so a host's ErrorMapper handles both install and upgrade batches
// through one code path.
func (b UpgradeBatchResult) Failure() error {
	var failures []*selfupdate.Failure
	for _, r := range b.Results {
		if upgradeResultFailed(r) {
			failures = append(failures, r.Failure)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &BatchFailure{Failures: failures}
}

// UpgradeOptions configures PlanUpgrade/CheckUpgrades/ExecuteUpgrade/
// Upgrade. Every side-effecting dependency is injectable
// (cli-install#req:no-network-in-tests).
type UpgradeOptions struct {
	// HostID is the running host's own catalog id
	// (cli-install#req:host-identity-from-catalog). Must be a valid catalog
	// id; PlanUpgrade panics otherwise, matching Plan's own contract.
	HostID string
	// All selects every catalog id whose status is installed, plus the
	// host, regardless of the relevance matrix (cli-install#req:upgrade-
	// targets). Names given alongside All is a caller/usage-layer concern
	// (cobracmd's own "--all takes no target names" refusal, mirroring
	// install's own usage check) — PlanUpgrade itself treats All as taking
	// priority and simply ignores any names passed alongside it. An empty
	// names list with All false is ALSO treated as the All set
	// (cli-install#req:upgrade-no-args-reports: the bare, no-argument
	// report runs the read-only check over the --all set), so a caller
	// need not special-case the truly-bare invocation.
	All bool
	// Yes skips the confirmation gate, matching Options.Yes.
	Yes bool
	// DryRun walks the full decision path without confirming, replacing,
	// or running any manager command (cli-install#req:upgrade-batch-
	// semantics inheriting cli-install#req:install-dry-run). Upgrade
	// returns PlanUpgrade's own result unchanged when set, exactly as
	// Install does for Options.DryRun.
	DryRun bool
	// Env carries every side-effecting dependency: status probing (via the
	// embedded Env) and the managed command runner (RunManaged) upgrade
	// shares with install. Getenv additionally supplies GH_TOKEN/
	// GITHUB_TOKEN for the bearer-auth HTTPClient default (cli-install#req:
	// upgrade-release-lookups-bounded).
	Env InstallEnv
	// Confirm asks whether to proceed with every target that would be
	// upgraded, called at most once per batch by ExecuteUpgrade, exactly
	// as Options.Confirm — it receives the already-planned pending
	// []UpgradeResult (current, latest, verdict, method, command all
	// already resolved).
	Confirm func(pending []UpgradeResult) (bool, error)
	// ConfigureRelease optionally overrides a non-host target's resolved
	// selfupdate.Config before it is used to look up or install that
	// target's release — the release-endpoint injection point
	// cli-install#req:no-network-in-tests requires. Nil keeps the
	// catalog's own defaults (the real GitHub API, bearer-authenticated
	// per HostConfig's own doc comment).
	ConfigureRelease func(target Entry, cfg selfupdate.Config) selfupdate.Config
	// ProbeOptions tunes status-probe concurrency and per-target time
	// budget; the zero value is production-correct.
	ProbeOptions ProbeOptions

	// HostConfig is the host's OWN self-update selfupdate.Config — built
	// the identical way its `self-update` command builds one, including
	// any manager overrides or extra Managers it adds beyond its catalog
	// entry (cli-install#req:host-target-is-running-binary: "The host MUST
	// use its own self-update Config and options... so upgrade <self> and
	// self-update reach the same library call"). Required whenever the
	// host is a candidate target (named explicitly, or under All/the bare
	// report). When its HTTPClient is left nil, PlanUpgrade/ExecuteUpgrade
	// default it to a bearer-authenticated client exactly as it does for
	// every other target — see the package-level githubHTTPClient doc
	// comment.
	HostConfig selfupdate.Config
	// HostAfterUpdate is the host's own after-update hook, passed straight
	// through to selfupdate.Options.AfterUpdate only for the host's own
	// REAL (non-dry-run) UpdateAt call, in ExecuteUpgrade — the SAME
	// closure its `self-update` command configures, so `upgrade <self>`
	// runs the identical hook `self-update` does
	// (cli-install#req:self-update-equals-upgrade-self), including when
	// the host is already current (task-22 review B2: ExecuteUpgrade makes
	// a second real UpdateAt call for an already-current host specifically
	// because selfupdate.Config.UpdateAt's own runAfterUpdate skips the
	// hook under DryRun, so PlanUpgrade's own DryRun(true) call alone never
	// fires it — see ExecuteUpgrade's own doc comment). Never used for any
	// other target: v1 reports a FinishHint instead of running another
	// CLI's hooks (cli-install#req:self-update-hook-hint).
	HostAfterUpdate selfupdate.AfterUpdateFunc
	// DetectHost resolves the host's own install classification. Nil
	// defaults to opts.HostConfig.DetectSelf — the real running executable
	// — exactly matching what the host's own `self-update` command would
	// call (cli-install#req:host-target-is-running-binary; task-22 review
	// S1: classification MUST come from DetectSelf, not a rebuilt
	// `<hostDir>/<hostID>` path, which can name the wrong file for a
	// renamed or aliased binary). Tests inject a fake to avoid depending on
	// the actual test binary's own location; production callers leave this
	// nil.
	DetectHost func() (selfupdate.Detection, error)
	// VerifyManaged probes an executable managed target after its manager
	// command completes, passed straight through to
	// selfupdate.Options.VerifyManaged for every target including the host
	// (task-22 review S2: this MUST be the same verifier `self-update`
	// itself uses — filtering PATH candidates by the detected manager's own
	// markers — never an ad hoc probe with no manager filter). cliinstall
	// itself has no opinion on how verification works; a Cobra host
	// defaults this to selfcliui.VerifyManagedBinary, exactly as its own
	// self-update command does. Required whenever any candidate target
	// might be an executable managed install; nil makes such an upgrade
	// fail with selfupdate.KindManagedCommand, matching UpdateAt's own
	// "not configured" failure.
	VerifyManaged selfupdate.ManagedBinaryVerifier

	// LookupConcurrency bounds how many targets' latest-release lookups run
	// at once. Zero defaults to 4 (cli-install#req:upgrade-release-
	// lookups-bounded).
	LookupConcurrency int
	// LookupTimeout bounds each individual latest-release lookup. Zero
	// defaults to 15 seconds (cli-install#req:upgrade-release-lookups-
	// bounded).
	LookupTimeout time.Duration
}

func (o UpgradeOptions) withDefaults() UpgradeOptions {
	if o.LookupConcurrency <= 0 {
		o.LookupConcurrency = 4
	}
	if o.LookupTimeout <= 0 {
		o.LookupTimeout = 15 * time.Second
	}
	return o
}

// detectHostFunc returns opts.DetectHost when set, otherwise
// opts.HostConfig.DetectSelf — the production default (task-22 review S1).
func detectHostFunc(opts UpgradeOptions) func() (selfupdate.Detection, error) {
	if opts.DetectHost != nil {
		return opts.DetectHost
	}
	cfg := upgradeHostConfig(opts)
	return cfg.DetectSelf
}

// --- target selection --------------------------------------------------

// upgradeCandidate is one target PlanUpgrade will resolve and (unless
// terminal) offer for confirmation. explicit distinguishes a name the
// caller typed from one this package added under All — the ONLY thing that
// changes for a non-release build (cli-install#req:upgrade-skips-non-
// release-builds: skipped without a lookup when not explicit, offered like
// any other target when it is).
type upgradeCandidate struct {
	id       string
	isHost   bool
	explicit bool
}

// allUpgradeCandidates builds cli-install#req:upgrade-targets' "--all"
// target set: every catalog id whose status is installed, plus the host
// unconditionally, in one Probe call over the whole catalog. Candidate and
// therefore result order is catalog (id-sorted) order with the host moved
// to the end (cli-install#req:host-upgraded-last) — there is no caller-
// given order to preserve for this set, unlike named targets. hostStatus is
// used only to detect and warn about an additional PATH copy of the host —
// never for the host's own classification or version (task-22 review M2).
func allUpgradeCandidates(ctx context.Context, opts UpgradeOptions) ([]upgradeCandidate, map[string]Status, Status) {
	entries := Entries()
	statuses := Probe(ctx, entries, "", opts.Env.Env, opts.ProbeOptions)

	statusByID := make(map[string]Status, len(entries))
	var candidates []upgradeCandidate
	var hostStatus Status
	for i, e := range entries {
		statusByID[e.ID] = statuses[i]
		if e.ID == opts.HostID {
			hostStatus = statuses[i]
			continue
		}
		if statuses[i].State == Installed {
			candidates = append(candidates, upgradeCandidate{id: e.ID})
		}
	}
	candidates = append(candidates, upgradeCandidate{id: opts.HostID, isHost: true})
	return candidates, statusByID, hostStatus
}

// namedUpgradeCandidates builds the target set for explicitly named
// targets (cli-install#req:upgrade-targets), de-duplicated in the order
// given, with the host — if named — moved to the end
// (cli-install#req:host-upgraded-last). Every name MUST be a valid catalog
// id before anything is probed (cli-install#req:unknown-target-refused),
// mirroring Plan's own "refuse the whole batch first" rule.
func namedUpgradeCandidates(ctx context.Context, names []string, opts UpgradeOptions) ([]upgradeCandidate, map[string]Status, Status, error) {
	unique := dedupeNames(names)
	var unknown []string
	for _, n := range unique {
		if _, ok := ByID(n); !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, nil, Status{}, unknownTargetsFailure(unknown)
	}

	entries := make([]Entry, 0, len(unique))
	hostNamed := false
	for _, n := range unique {
		e, _ := ByID(n)
		entries = append(entries, e)
		if n == opts.HostID {
			hostNamed = true
		}
	}

	statuses := Probe(ctx, entries, "", opts.Env.Env, opts.ProbeOptions)
	statusByID := make(map[string]Status, len(entries))
	for i, e := range entries {
		statusByID[e.ID] = statuses[i]
	}

	var candidates []upgradeCandidate
	for _, n := range unique {
		if n == opts.HostID {
			continue
		}
		candidates = append(candidates, upgradeCandidate{id: n, explicit: true})
	}
	var hostStatus Status
	if hostNamed {
		hostStatus = statusByID[opts.HostID]
		candidates = append(candidates, upgradeCandidate{id: opts.HostID, isHost: true, explicit: true})
	}

	return candidates, statusByID, hostStatus, nil
}

// --- classification and non-release detection ---------------------------

// classifyForUpgrade determines a non-host target's upgrade classification
// from its already-probed Status, checked against ITS OWN catalog entry's
// managers — never the whole catalog's deduplicated set allCatalogManagers
// returns (cli-install#req:upgrade-per-target-policy: "Classification of a
// non-host copy MUST match DetectSelf: managed when the found path or its
// symlink-resolved path matches a manager's markers, otherwise manual or
// ambiguous judged on the symlink-resolved path only"; DetectSelf itself
// classifies only against the caller's OWN Config.Managers). Probe's own
// Status.Method/Manager (used for `install` listing) deliberately widen
// this to every catalog manager, so two different CLIs' HomebrewCask
// entries — identical Name and PathMarkers, different UpgradeCommand —
// collapse to whichever one allCatalogManagers happened to keep; reusing
// that value here would pick a foreign target's upgrade command by
// accident, so this recomputes classification from scratch using only
// managers, the target's own list.
//
// The UNRESOLVED, PATH-found status.Path is checked ONLY against manager
// markers, via selfupdate.ClassifyManagers rather than the full
// selfupdate.Classify — this is deliberately narrower than the resolved-
// path check below. It exists for a Snap-dispatched binary
// (`/snap/bin/ingitdb`, itself a symlink to `/usr/bin/snap`), which must be
// recognized as Snap-managed from its unresolved PATH entry before symlink
// resolution obscures it. Running the FULL Classify (which also applies the
// built-in system-package-directory check, self-update#req:system-package-
// dirs-are-managed) against that same unresolved path would diverge from
// DetectSelf, which only ever classifies the RESOLVED path: a shim at
// `/usr/bin/foo` symlinked out to a manual `/opt/foo/bin/foo` would then
// classify Managed here but Manual via self-update for the identical
// binary, breaking cli-install#req:self-update-equals-upgrade-self. The
// trade-off this accepts is the mirror image of that gap: a target whose
// UNRESOLVED PATH entry sits in a system directory but whose REAL file
// lives elsewhere (an `/opt` install, say) is classified by where the file
// actually is, not by the symlink someone happens to have pointed at it —
// exactly what DetectSelf itself would do. The resolved-path check below
// uses the full selfupdate.Classify — including the system-directory check
// — because that IS the resolved path DetectSelf itself classifies.
func classifyForUpgrade(status Status, managers []selfupdate.Manager) selfupdate.Detection {
	if det := selfupdate.ClassifyManagers(status.Path, managers); det.Method == selfupdate.Managed {
		return det
	}
	resolved := status.ResolvedPath
	if resolved == "" {
		resolved = status.Path
	}
	if det := selfupdate.Classify(resolved, managers); det.Method == selfupdate.Managed {
		det.Path = status.Path
		return det
	}
	det := selfupdate.Classify(resolved, managers)
	det.Path = resolved
	return det
}

// releaseVersionPattern matches a strict MAJOR.MINOR.PATCH version with an
// optional -prerelease suffix and an optional leading "v" — REQ: upgrade-
// skips-non-release-builds' own shape. The prerelease token allows hyphens
// (task-22 review M1: a hyphenated prerelease like "1.2.0-rc-1" or
// "0.5.0-beta-2" is valid semver and must not be misclassified as a
// non-release build) alongside alphanumerics and dots. No "+" build
// metadata is permitted by this pattern at all, which is what makes the
// separate literal "+" check below redundant-but-explicit rather than
// load-bearing on its own.
var releaseVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// pseudoVersionPattern matches a Go pseudo-version's own suffix shape: a
// 14-digit timestamp and a 12-hex-digit short commit hash, joined by "-".
// This catches "0.0.0-20230101120000-abcdef123456" and its "-0.<ts>-<hash>"
// and "-pre.0.<ts>-<hash>" prerelease-tagged variants alike, none of which
// releaseVersionPattern alone rejects: a pseudo-version's numeric-looking
// prerelease token is syntactically a valid semver prerelease.
var pseudoVersionPattern = regexp.MustCompile(`[0-9]{14}-[0-9a-f]{12}$`)

// isNonReleaseBuild reports whether version is a non-release build per
// cli-install#req:upgrade-skips-non-release-builds: one of undetermined
// (falling back to selfupdate's own {"dev"} default when empty, mirroring
// Config.withDefaults — unexported in that package, so mirrored here as a
// small, REQ-defined pure function rather than reached into), the literal
// "dev", "(devel)" or "unknown" regardless of the catalog's own declared
// list, carrying "+" build metadata, not strictly MAJOR.MINOR.PATCH with an
// optional -prerelease, or a Go pseudo-version.
func isNonReleaseBuild(version string, undetermined []string) bool {
	switch version {
	case "dev", "(devel)", "unknown":
		return true
	}
	if containsString(effectiveUndetermined(undetermined), version) {
		return true
	}
	if strings.Contains(version, "+") {
		return true
	}
	if !releaseVersionPattern.MatchString(version) {
		return true
	}
	return pseudoVersionPattern.MatchString(version)
}

func effectiveUndetermined(list []string) []string {
	if len(list) == 0 {
		return []string{"dev"}
	}
	return list
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --- ambiguous (shared by CheckUpgrades and PlanUpgrade) -----------------

// ambiguousRefusal calls the REAL selfupdate.Config.UpdateAt with an empty
// Options for an ambiguous detection and returns the UpgradeOutcomeRefused
// row it produces (task-22 review B1). This call makes no network request
// and performs no I/O: UpdateAt's own ambiguous check
// (self-update#req:ambiguous-safe-default) runs before any lookup, so
// calling it here — rather than constructing a matching *selfupdate.Failure
// by hand — is free and guarantees the exact same Failure a real
// `self-update` would produce for this classification, with no drift
// possible between the two. cfg is any Config classified against the same
// detection; its release-endpoint fields are never reached.
func ambiguousRefusal(cfg selfupdate.Config, detection selfupdate.Detection) (UpgradeOutcome, *selfupdate.Failure) {
	_, err := cfg.UpdateAt(context.Background(), detection, selfupdate.Options{})
	var f *selfupdate.Failure
	errors.As(err, &f)
	return UpgradeOutcomeRefused, f
}

// mapAction maps a completed selfupdate Outcome/error onto the terminal (or
// still-pending, for a DryRun call) UpgradeOutcome this package reports —
// the ONLY place cli-install upgrade turns a library decision into its own
// display token (cli-install#req:upgrade-per-target-policy). It never
// decides anything UpdateAt did not already decide.
func mapAction(outcome selfupdate.Outcome, err error) (UpgradeOutcome, *selfupdate.Failure, string, string) {
	if err != nil {
		var f *selfupdate.Failure
		errors.As(err, &f)
		return UpgradeOutcomeFailed, f, "", ""
	}
	switch outcome.Action {
	case selfupdate.ActionUpdated:
		return UpgradeOutcomeUpgraded, nil, "", ""
	case selfupdate.ActionManagerExecuted:
		return UpgradeOutcomeManagerExecuted, nil, "", ""
	case selfupdate.ActionAlreadyCurrent:
		return UpgradeOutcomeAlreadyCurrent, nil, "", ""
	case selfupdate.ActionAhead:
		return UpgradeOutcomeAhead, nil, "", ""
	case selfupdate.ActionRedirected:
		return UpgradeOutcomeRedirected, nil, "", ""
	case selfupdate.ActionPlanned:
		return UpgradeOutcomeDryRun, nil, outcome.PlannedURL, outcome.PlannedCommand
	default:
		// ActionAborted never occurs here: Confirm is always nil for every
		// UpdateAt call this package makes (the batch gate already asked).
		return UpgradeOutcomeFailed, &selfupdate.Failure{
			Kind: selfupdate.KindUnexpected,
			Err:  fmt.Errorf("upgrade: unexpected action %v", outcome.Action),
		}, "", ""
	}
}

// --- read-only report / --check ------------------------------------------

// checkRow is CheckUpgrades' own working state for one candidate.
// Classification is deliberately DEFERRED to resolveCheckRow, run only
// after a successful lookup, to mirror selfupdate/cobracmd's own runCheck
// decision order exactly (task-22 second review D1/D2): that function
// calls checkFunc (Config.Check) FIRST and fails the command immediately
// on its own error, WITHOUT ever calling detectFunc; detectFunc — and
// hence classification — only ever runs after a successful Check, and a
// detectFunc failure there falls back to Ambiguous and still does not fail
// the command. Building this row's classification eagerly and using it to
// decide the outcome BEFORE the lookup (as an earlier revision did) let an
// ambiguous host's failed lookup fall through as a mere warning instead of
// the release-lookup failure self-update itself reports — this ordering
// closes that gap by construction: nothing here decides Refused, Failed or
// any display outcome until resolveCheckRow's own lookup has already
// succeeded or failed.
type checkRow struct {
	result      UpgradeResult
	needsLookup bool
	cfg         selfupdate.Config
	isHost      bool
	// classification resolves this row's install-method classification.
	// For a non-host row it is pre-computed (classifyForUpgrade never
	// errors, since it only reads an already-probed Status) and always
	// succeeds; for the host row it defers to detectHostFunc, whose error
	// resolveCheckRow itself falls back from (D2), exactly as self-update's
	// own detectFunc failure does.
	classification func() (selfupdate.Detection, error)
	// hostStatus/hostID are consulted only for the host's own additional-
	// PATH-copy warning, resolved alongside classification since both need
	// the SAME detected path.
	hostStatus Status
	hostID     string
}

func classificationOf(det selfupdate.Detection) func() (selfupdate.Detection, error) {
	return func() (selfupdate.Detection, error) { return det, nil }
}

// buildCheckTargetRow builds a non-host candidate's row for CheckUpgrades:
// the non-release skip is identical to PlanUpgrade's own
// (isNonReleaseBuild, never a private decision), and classification is
// computed here (it cannot fail, unlike the host's DetectHost) but not
// USED to decide anything until resolveCheckRow's own lookup has run —
// see checkRow's own doc comment. No UpdateAt call ever happens here:
// CheckUpgrades uses selfupdate.Config.Check instead, because Check is
// self-update's OWN --check implementation and, unlike UpdateAt, never
// risks running AfterUpdate (cli-install#req:upgrade-check: "MUST NOT
// download, write, confirm or invoke a manager" — Check() call literally
// cannot).
func buildCheckTargetRow(c upgradeCandidate, target Entry, status Status, opts UpgradeOptions) checkRow {
	r := UpgradeResult{Target: c.id, Status: status, Warnings: append([]string(nil), status.Warnings...)}

	switch status.State {
	case NotInstalled:
		r.Outcome = UpgradeOutcomeNotInstalled
		r.InstallHint = opts.HostID + " install " + c.id
		return checkRow{result: r}
	case Unrecognized:
		r.Outcome = UpgradeOutcomeUnrecognized
		return checkRow{result: r}
	}

	r.Current = status.Version
	nonRelease := isNonReleaseBuild(status.Version, target.UndeterminedVersions)
	if nonRelease && !c.explicit {
		r.Outcome = UpgradeOutcomeSkippedNonRelease
		return checkRow{result: r}
	}
	r.NonReleaseBuild = nonRelease

	det := classifyForUpgrade(status, target.Managers)
	cfg := upgradeTargetConfig(target, status.Version, opts)
	return checkRow{result: r, needsLookup: true, cfg: cfg, classification: classificationOf(det)}
}

// buildCheckHostRow is buildCheckTargetRow's host counterpart: Status
// always stays zero (task-22 review M2); classification defers to
// detectHostFunc (task-22 review S1) inside resolveCheckRow, never
// resolved — and never allowed to fail the row — here (D2).
func buildCheckHostRow(c upgradeCandidate, hostStatus Status, opts UpgradeOptions) checkRow {
	r := UpgradeResult{Target: c.id, Host: true}

	current := opts.HostConfig.CurrentVersion
	r.Current = current
	nonRelease := isNonReleaseBuild(current, opts.HostConfig.UndeterminedVersions)
	if nonRelease && !c.explicit {
		r.Outcome = UpgradeOutcomeSkippedNonRelease
		return checkRow{result: r}
	}
	r.NonReleaseBuild = nonRelease

	cfg := upgradeHostConfig(opts)
	return checkRow{
		result: r, needsLookup: true, cfg: cfg, isHost: true,
		classification: detectHostFunc(opts), hostStatus: hostStatus, hostID: c.id,
	}
}

// checkDisplayOutcome labels a non-ambiguous row for the read-only
// report/--check view from Check's own CheckResult and this target's
// classification. It is DISPLAY ONLY: CheckUpgrades never confirms,
// downloads, writes or runs a manager command regardless of what this
// returns (cli-install#req:upgrade-check), so — unlike PlanUpgrade's
// mapAction — a wrong label here could not cause an unwanted mutation, only
// a misleading report. Ahead is checked first, matching self-update's own
// ordering (self-update#req:ahead-of-latest short-circuits before a
// managed/manual branch is ever considered).
func checkDisplayOutcome(verdict selfupdate.Verdict, method selfupdate.InstallMethod, manager *selfupdate.Manager) UpgradeOutcome {
	if verdict == selfupdate.Ahead {
		return UpgradeOutcomeAhead
	}
	if method == selfupdate.Managed {
		if manager == nil || !manager.CanExecuteUpgrade() {
			return UpgradeOutcomeRedirected
		}
		return UpgradeOutcomeDryRun
	}
	if verdict == selfupdate.UpToDate {
		return UpgradeOutcomeAlreadyCurrent
	}
	return UpgradeOutcomeDryRun
}

// resolveCheckRow calls selfupdate.Config.Check — the exact library call
// self-update's own --check makes — FIRST, and decides row's outcome from
// its own error before classification is ever consulted, mirroring
// selfupdate/cobracmd's own runCheck order exactly (task-22 second review
// D1): checkFunc's error fails the command immediately, and detectFunc
// (classification) is never even called in that case. Only once Check
// succeeds does this resolve row's classification — falling back to
// Ambiguous, without failing the row, on a host detection error (D2),
// exactly as runCheck's own "detectErr != nil" fallback does — and use it
// to decide the display outcome (Refused for Ambiguous, via the real
// ambiguousRefusal call so the Failure matches UpdateAt's own message
// exactly; checkDisplayOutcome for everything else).
func resolveCheckRow(ctx context.Context, row *checkRow, timeout time.Duration) {
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := row.cfg.Check(lookupCtx)
	if err != nil {
		var f *selfupdate.Failure
		errors.As(err, &f)
		row.result.Outcome = UpgradeOutcomeFailed
		row.result.Failure = f
		return
	}
	row.result.Current = result.Current
	row.result.Latest = result.Latest
	row.result.Verdict = result.Verdict

	det, derr := row.classification()
	if derr != nil {
		// D2: a host detection failure never fails the row here — it falls
		// back to Ambiguous and the check still reports current/latest/
		// verdict, exactly as self-update's own runCheck does for its
		// detectFunc failure.
		det = selfupdate.Detection{Method: selfupdate.Ambiguous}
	}
	row.result.InstallMethod = det.Method
	row.result.Manager = det.Manager
	row.result.ResolvedPath = det.Path
	if det.Manager != nil {
		row.result.Command = det.Manager.UpgradeCommand
		row.result.Hint = det.Manager.UpgradeHint
	}
	if row.isHost && row.hostStatus.Path != "" && !samePath(row.hostStatus.Path, det.Path, goosName) {
		row.result.Warnings = append(row.result.Warnings, fmt.Sprintf("another copy of %s is on PATH at %s; it was left untouched", row.hostID, row.hostStatus.Path))
		row.result.OtherPaths = append(row.result.OtherPaths, row.hostStatus.Path)
	}

	if det.Method == selfupdate.Ambiguous {
		row.result.Outcome, row.result.Failure = ambiguousRefusal(row.cfg, det)
		return
	}
	row.result.Outcome = checkDisplayOutcome(result.Verdict, det.Method, det.Manager)
}

func runCheckLookups(ctx context.Context, rows []checkRow, opts UpgradeOptions) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.LookupConcurrency)
	for i := range rows {
		if !rows[i].needsLookup {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			resolveCheckRow(ctx, &rows[i], opts.LookupTimeout)
		}(i)
	}
	wg.Wait()
}

// resolveUpgradeCandidates runs the shared target-selection step both
// CheckUpgrades and PlanUpgrade need: validate/probe names or build the
// --all set, and resolve the host's own directory (used only for
// diagnostics; never for the host's classification — see buildCheckHostRow/
// buildPlanHostRow).
func resolveUpgradeCandidates(ctx context.Context, names []string, opts UpgradeOptions) ([]upgradeCandidate, map[string]Status, Status, error) {
	if opts.All || len(names) == 0 {
		candidates, statusByID, hostStatus := allUpgradeCandidates(ctx, opts)
		return candidates, statusByID, hostStatus, nil
	}
	return namedUpgradeCandidates(ctx, names, opts)
}

// CheckUpgrades is `--check`'s and the bare report's own entry point
// (cli-install#req:upgrade-check, cli-install#req:upgrade-no-args-reports).
// It resolves every candidate's classification and, for every installed,
// not-skipped target, calls selfupdate.Config.Check — self-update's own
// read-only comparison, the SAME call `self-update --check` makes — never
// selfupdate.Config.UpdateAt, so it can never download, write, confirm, run
// a manager command, or invoke an AfterUpdate hook, regardless of Verdict
// or install method. An ambiguous classification is still reported as
// UpgradeOutcomeRefused (task-22 review B1), but — unlike PlanUpgrade/
// ExecuteUpgrade — CheckUpgrades' own caller (cobracmd's read-only report)
// never fails the command merely because a target is refused; only a real
// lookup failure (UpgradeOutcomeFailed) does that (self-update's own
// --check never fails for an ambiguous install either).
func CheckUpgrades(ctx context.Context, names []string, opts UpgradeOptions) (UpgradeBatchResult, error) {
	opts = opts.withDefaults()
	if _, ok := ByID(opts.HostID); !ok {
		panic("cliinstall: host id " + opts.HostID + " is not in the compiled catalog")
	}

	candidates, statusByID, hostStatus, err := resolveUpgradeCandidates(ctx, names, opts)
	if err != nil {
		return UpgradeBatchResult{Host: opts.HostID}, err
	}

	rows := make([]checkRow, len(candidates))
	for i, c := range candidates {
		if c.isHost {
			rows[i] = buildCheckHostRow(c, hostStatus, opts)
			continue
		}
		e, _ := ByID(c.id)
		rows[i] = buildCheckTargetRow(c, e, statusByID[c.id], opts)
	}

	runCheckLookups(ctx, rows, opts)

	results := make([]UpgradeResult, len(rows))
	for i, row := range rows {
		results[i] = row.result
	}
	return UpgradeBatchResult{Host: opts.HostID, Results: results}, nil
}

// --- planning (--dry-run and the confirm/execute pipeline) ---------------

// upgradePlanRow is PlanUpgrade's own working state for one candidate,
// before and after its (possible) release lookup and UpdateAt(DryRun) call.
type upgradePlanRow struct {
	result      UpgradeResult
	needsLookup bool
	cfg         selfupdate.Config
	detection   selfupdate.Detection
}

// buildPlanTargetRow builds a non-host candidate's row up to (but not
// including) its release lookup and UpdateAt call: NotInstalled and
// Unrecognized targets, and an implicitly-selected non-release build, are
// already terminal and never need one (cli-install#req:upgrade-release-
// lookups-bounded); an ambiguous classification is resolved immediately,
// for free, via the real UpdateAt (task-22 review B1).
func buildPlanTargetRow(c upgradeCandidate, target Entry, status Status, opts UpgradeOptions) upgradePlanRow {
	r := UpgradeResult{Target: c.id, Status: status, Warnings: append([]string(nil), status.Warnings...)}

	switch status.State {
	case NotInstalled:
		r.Outcome = UpgradeOutcomeNotInstalled
		r.InstallHint = opts.HostID + " install " + c.id
		return upgradePlanRow{result: r}
	case Unrecognized:
		r.Outcome = UpgradeOutcomeUnrecognized
		return upgradePlanRow{result: r}
	}

	det := classifyForUpgrade(status, target.Managers)
	r.InstallMethod = det.Method
	r.Manager = det.Manager
	r.ResolvedPath = det.Path
	if det.Manager != nil {
		r.Command = det.Manager.UpgradeCommand
		r.Hint = det.Manager.UpgradeHint
	}
	r.Current = status.Version

	nonRelease := isNonReleaseBuild(status.Version, target.UndeterminedVersions)
	if nonRelease && !c.explicit {
		r.Outcome = UpgradeOutcomeSkippedNonRelease
		return upgradePlanRow{result: r}
	}
	r.NonReleaseBuild = nonRelease

	cfg := upgradeTargetConfig(target, status.Version, opts)
	if det.Method == selfupdate.Ambiguous {
		r.Outcome, r.Failure = ambiguousRefusal(cfg, det)
		return upgradePlanRow{result: r}
	}
	return upgradePlanRow{result: r, needsLookup: true, cfg: cfg, detection: det}
}

// buildPlanHostRow is buildPlanTargetRow's host counterpart: detected via
// detectHostFunc (task-22 review S1), never a rebuilt path; Status stays
// zero (task-22 review M2). Its UpdateAt(DryRun) call does NOT wire
// opts.HostAfterUpdate: selfupdate.Config.UpdateAt's own runAfterUpdate
// skips the hook whenever Options.DryRun is set, so passing it here would
// never fire anyway — ExecuteUpgrade wires it for the host's own real,
// non-dry-run call instead, including a second such call for an
// already-current host (task-22 review B2; see ExecuteUpgrade's own doc
// comment).
func buildPlanHostRow(c upgradeCandidate, hostStatus Status, opts UpgradeOptions) upgradePlanRow {
	r := UpgradeResult{Target: c.id, Host: true}

	det, derr := detectHostFunc(opts)()
	if derr != nil {
		r.Outcome = UpgradeOutcomeFailed
		r.Failure = &selfupdate.Failure{Kind: selfupdate.KindUnexpected, Err: fmt.Errorf("resolve running executable: %w", derr)}
		return upgradePlanRow{result: r}
	}
	r.InstallMethod = det.Method
	r.Manager = det.Manager
	r.ResolvedPath = det.Path
	if det.Manager != nil {
		r.Command = det.Manager.UpgradeCommand
		r.Hint = det.Manager.UpgradeHint
	}
	if hostStatus.Path != "" && !samePath(hostStatus.Path, det.Path, goosName) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("another copy of %s is on PATH at %s; it was left untouched", c.id, hostStatus.Path))
		r.OtherPaths = append(r.OtherPaths, hostStatus.Path)
	}

	current := opts.HostConfig.CurrentVersion
	r.Current = current

	nonRelease := isNonReleaseBuild(current, opts.HostConfig.UndeterminedVersions)
	if nonRelease && !c.explicit {
		r.Outcome = UpgradeOutcomeSkippedNonRelease
		return upgradePlanRow{result: r}
	}
	r.NonReleaseBuild = nonRelease

	cfg := upgradeHostConfig(opts)
	if det.Method == selfupdate.Ambiguous {
		r.Outcome, r.Failure = ambiguousRefusal(cfg, det)
		return upgradePlanRow{result: r}
	}
	return upgradePlanRow{result: r, needsLookup: true, cfg: cfg, detection: det}
}

// resolveUpgradeRow resolves row's own single latest-release lookup
// (cli-install#req:upgrade-release-lookups-bounded, cli-install#req:
// upgrade-resolves-release-once), then calls the REAL selfupdate.Config.
// UpdateAt with DryRun set — the same call `self-update --dry-run` makes —
// and maps its Outcome/error onto row's terminal or still-pending
// UpgradeOutcome via mapAction. A lookup failure here is NOT translated
// into a row failure directly: ResolvedTag is simply left empty and
// UpdateAt performs its OWN internal lookup attempt, which fails or warns
// exactly as self-update's own unpinned path would for this classification
// (task-22 review B3: a manual install fails, a managed one proceeds with
// an advisory warning) — cliinstall makes no method-aware judgment of its
// own about lookup-failure severity.
func resolveUpgradeRow(ctx context.Context, row *upgradePlanRow, timeout time.Duration) {
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tag, lookupErr := row.cfg.LatestRelease(lookupCtx)
	if lookupErr != nil {
		// Retry once, within the SAME bounded lookupCtx, before falling
		// back to an empty tag. Without this, a transient failure here
		// that UpdateAt's own internal (unpinned, ResolvedTag="") lookup
		// below would have resolved successfully left row.Tag empty even
		// though a real target WAS planned and shown — ExecuteUpgrade
		// would then re-search for "latest" independently instead of
		// reusing the exact release just confirmed, breaking
		// cli-install#req:upgrade-resolves-release-once (task-22 third
		// review N1). A single retry, reusing this same call, is simpler
		// and strictly more correct than reconstructing a tag from
		// UpdateAt's own Outcome, which exposes only the normalized
		// version, never the raw tag (ambiguous to reconstruct for a
		// target whose real tag carries neither a TagPrefix nor a "v").
		tag, lookupErr = row.cfg.LatestRelease(lookupCtx)
	}
	if lookupErr != nil {
		tag = ""
	}

	// UpdateAt performs its OWN internal lookup/re-verification whenever tag
	// is empty or stale (see its own doc comment), so it MUST run under the
	// SAME bounded lookupCtx as the LatestRelease call above — passing the
	// outer, unbounded ctx here would let a slow or hanging release
	// endpoint block past opts.LookupTimeout entirely
	// (cli-install#req:upgrade-release-lookups-bounded).
	// AfterUpdate is deliberately never wired here, host or not:
	// selfupdate.Config.UpdateAt's own runAfterUpdate skips the hook
	// whenever Options.DryRun is set (see its own guard), so passing one
	// would never fire under this DryRun(true) call — see ExecuteUpgrade's
	// own doc comment for where the host's hook actually runs.
	outcome, err := row.cfg.UpdateAt(lookupCtx, row.detection, selfupdate.Options{
		ResolvedTag:   tag,
		DryRun:        true,
		VerifyManaged: nil, // never reached under DryRun: updateManaged returns ActionPlanned first
	})

	row.result.Outcome, row.result.Failure, row.result.AssetURL, row.result.Command = applyMappedAction(row.result, outcome, err)
	if row.result.Failure != nil {
		return
	}
	row.result.Current = outcome.Result.Current
	row.result.Latest = outcome.Result.Latest
	row.result.Tag = tag
	row.result.Verdict = outcome.Result.Verdict
	// PostSwapWarning and AfterUpdateWarning are set only by ActionUpdated/
	// ActionManagerExecuted/a real AfterUpdate invocation, none of which
	// this DryRun(true) call ever reaches — only ReleaseCheckWarning (a
	// managed target's advisory lookup failure, task-22 review B3) is ever
	// populated here.
	if outcome.ReleaseCheckWarning != nil {
		row.result.Warnings = append(row.result.Warnings, fmt.Sprintf("latest release unavailable: %v", outcome.ReleaseCheckWarning))
	}
}

// applyMappedAction wraps mapAction so a managed row's already-known
// Command survives even when mapAction itself returns none (mapAction only
// returns a command for ActionPlanned's own PlannedCommand).
func applyMappedAction(r UpgradeResult, outcome selfupdate.Outcome, err error) (UpgradeOutcome, *selfupdate.Failure, string, string) {
	action, failure, assetURL, command := mapAction(outcome, err)
	if command == "" {
		command = r.Command
	}
	return action, failure, assetURL, command
}

func runUpgradeLookups(ctx context.Context, rows []upgradePlanRow, opts UpgradeOptions) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.LookupConcurrency)
	for i := range rows {
		if !rows[i].needsLookup {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			resolveUpgradeRow(ctx, &rows[i], opts.LookupTimeout)
		}(i)
	}
	wg.Wait()
}

// PlanUpgrade validates and locates every target and, for each one that is
// installed and not skipped, calls the REAL selfupdate.Config.UpdateAt with
// DryRun set — exactly the call `self-update --dry-run` makes for the same
// classified copy (cli-install#req:upgrade-per-target-policy: "Each target
// MUST be handled by the Self-Update Library's policy"). PlanUpgrade itself
// makes no ambiguous/managed/current/ahead decision: mapAction only
// translates UpdateAt's own Outcome.Action and Failure into this package's
// display vocabulary.
//
// PlanUpgrade's own UpdateAt(DryRun) call never runs the host's after-
// update hook, even for an already-current host: selfupdate.Config.
// UpdateAt's own runAfterUpdate skips it whenever Options.DryRun is set
// (self-update's own, pre-existing behavior — there being nothing to
// preview for a no-op), and PlanUpgrade's call always sets DryRun. A real
// (non-dry-run, non-check) run's after-update hook fires from
// ExecuteUpgrade's own real UpdateAt call instead — including a SECOND
// such call for an already-current host, since that row is otherwise
// terminal after planning and would not reach a real call at all
// (task-22 review B2; see ExecuteUpgrade's own doc comment). `--check` and
// the bare, no-argument report do not call PlanUpgrade at all — they call
// CheckUpgrades, which uses the strictly read-only selfupdate.Config.Check
// instead (cli-install#req:upgrade-check, cli-install#req:upgrade-no-args-
// reports).
//
// PlanUpgrade alone is `--dry-run`'s complete answer, and it is the SAME
// plan ExecuteUpgrade later acts on for whatever is still pending — the
// tag a caller shows before confirming is the SAME tag ExecuteUpgrade
// passes to UpdateAt's own ResolvedTag (cli-install#req:upgrade-resolves-
// release-once).
//
// names selects explicit targets; opts.All, or an empty names with
// opts.All false, selects cli-install#req:upgrade-targets' "--all" set
// instead (every installed catalog id, plus the host). An unknown name
// fails the WHOLE batch before anything is probed or looked up
// (cli-install#req:unknown-target-refused); that case is the returned
// error, with an empty BatchResult. Every other outcome, including every
// per-target failure, is reported only in BatchResult.Results.
func PlanUpgrade(ctx context.Context, names []string, opts UpgradeOptions) (UpgradeBatchResult, error) {
	opts = opts.withDefaults()
	if _, ok := ByID(opts.HostID); !ok {
		panic("cliinstall: host id " + opts.HostID + " is not in the compiled catalog")
	}

	candidates, statusByID, hostStatus, err := resolveUpgradeCandidates(ctx, names, opts)
	if err != nil {
		return UpgradeBatchResult{Host: opts.HostID}, err
	}

	rows := make([]upgradePlanRow, len(candidates))
	for i, c := range candidates {
		if c.isHost {
			rows[i] = buildPlanHostRow(c, hostStatus, opts)
			continue
		}
		e, _ := ByID(c.id) // guaranteed valid: candidates come only from validated names or the catalog itself
		rows[i] = buildPlanTargetRow(c, e, statusByID[c.id], opts)
	}

	runUpgradeLookups(ctx, rows, opts)

	results := make([]UpgradeResult, len(rows))
	for i, row := range rows {
		results[i] = row.result
	}
	return UpgradeBatchResult{Host: opts.HostID, Results: results}, nil
}

// --- execution --------------------------------------------------------

var errNoUpgradeConfirmCallback = errors.New("no confirmation callback configured; pass --yes for non-interactive use")

// ExecuteUpgrade upgrades every still-pending target in plan (Outcome ==
// UpgradeOutcomeDryRun — the rows whose PlanUpgrade UpdateAt(DryRun) call
// returned ActionPlanned) — asking at most one confirmation covering all of
// them (unless opts.Yes) — then calls the REAL selfupdate.Config.UpdateAt
// (DryRun false) for each one, passing EXACTLY the release tag PlanUpgrade
// already resolved via selfupdate.Options.ResolvedTag, with no per-target
// Confirm callback (the batch gate already asked; UpdateAt with a nil
// Confirm proceeds immediately). Every other Result in plan — ahead,
// redirected, refused, skipped, not installed, unrecognized or failed — is
// a terminal outcome PlanUpgrade's own UpdateAt call already decided and
// passes through unchanged. The one exception is an already-current HOST
// row: ExecuteUpgrade makes a SECOND, real UpdateAt call for it — see the
// dedicated paragraph below — every other row never gets a second call
// (task-22 review B3; task-22 third review N3, correcting an earlier
// revision of this comment that claimed no row ever gets one). Targets
// execute in plan's own order, which already carries the host last
// (cli-install#req:host-upgraded-last).
//
// An already-current host's after-update hook runs from a SECOND, real
// (non-dry-run) UpdateAt call, unconditionally, regardless of how the
// pending-confirmation gate above resolved — proceeded, declined, or
// refused outright (task-22 third review N2): selfupdate.Config.UpdateAt's
// own runAfterUpdate skips the hook whenever Options.DryRun is set, so
// PlanUpgrade's own DryRun(true) call, which already decided this row is
// already current, never fires it, exactly as `self-update --dry-run`
// itself never does; `self-update --yes` on an already-current binary
// makes exactly ONE UpdateAt call with DryRun false, and THAT call's
// runAfterUpdate does fire. Gating this second call behind the SAME
// confirmation the pending targets need would be wrong: the host is a
// no-op either way — nothing is downloaded, written, or replaced — so
// REQ: confirmation-gate's own "before any download or write" scope never
// applies to it, and self-update itself never asks before running this
// hook either. A non-interactive refusal or a Confirm error for some OTHER
// pending target must not suppress it, so this call happens on every
// return path, not only the "everything proceeded" one.
//
// ExecuteUpgrade always returns a fully populated UpgradeBatchResult, one
// Result per target, even for a batch-level confirmation refusal — mirroring
// Execute's own "never an empty BatchResult" contract. Its own returned
// error exists only for a caller that wants one classification of a
// batch-level refusal; UpgradeBatchResult.Failure() is the way to see every
// target's own typed failure.
func ExecuteUpgrade(ctx context.Context, plan UpgradeBatchResult, opts UpgradeOptions) (UpgradeBatchResult, error) {
	opts = opts.withDefaults()
	results := make([]UpgradeResult, len(plan.Results))
	copy(results, plan.Results)

	var pendingIdx []int
	for i, r := range results {
		if r.Outcome == UpgradeOutcomeDryRun {
			pendingIdx = append(pendingIdx, i)
		}
	}

	if len(pendingIdx) > 0 {
		proceed := opts.Yes
		if !opts.Yes {
			pending := make([]UpgradeResult, len(pendingIdx))
			for j, i := range pendingIdx {
				pending[j] = results[i]
			}

			if opts.Confirm == nil {
				refusal := &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errNoUpgradeConfirmCallback}
				markUpgradeFailed(results, pendingIdx, refusal)
				runAlreadyCurrentHostHook(ctx, results, opts)
				return UpgradeBatchResult{Host: plan.Host, Results: results}, refusal
			}

			var confirmErr error
			proceed, confirmErr = opts.Confirm(pending)
			if confirmErr != nil {
				var f *selfupdate.Failure
				if !errors.As(confirmErr, &f) {
					f = &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: confirmErr}
				}
				markUpgradeFailed(results, pendingIdx, f)
				runAlreadyCurrentHostHook(ctx, results, opts)
				return UpgradeBatchResult{Host: plan.Host, Results: results}, confirmErr
			}
		}
		if proceed {
			for _, i := range pendingIdx {
				r := results[i]
				if r.Host {
					results[i] = executeHostUpgrade(ctx, r, opts)
					continue
				}
				e, _ := ByID(r.Target) // guaranteed valid: only PlanUpgrade's own rows, all built from a catalog Entry, ever reach here
				results[i] = executeTargetUpgrade(ctx, e, r, opts)
			}
		} else {
			for _, i := range pendingIdx {
				r := results[i]
				r.Outcome = UpgradeOutcomeDeclined
				results[i] = r
			}
		}
	}

	runAlreadyCurrentHostHook(ctx, results, opts)
	return UpgradeBatchResult{Host: plan.Host, Results: results}, nil
}

// runAlreadyCurrentHostHook makes the second, real UpdateAt call an
// already-current host row needs so its after-update hook fires (see
// ExecuteUpgrade's own doc comment) — called on EVERY ExecuteUpgrade return
// path, including a batch-level confirmation refusal or Confirm error for
// some OTHER pending target (task-22 third review N2), because this host
// row is never part of that confirmation set at all: AlreadyCurrent is
// already terminal by the time ExecuteUpgrade runs, downloads or writes
// nothing, and self-update itself never gates it behind a prompt either.
func runAlreadyCurrentHostHook(ctx context.Context, results []UpgradeResult, opts UpgradeOptions) {
	for i, r := range results {
		if r.Host && r.Outcome == UpgradeOutcomeAlreadyCurrent {
			results[i] = executeHostUpgrade(ctx, r, opts)
		}
	}
}

// markUpgradeFailed replaces every results[i] for i in idx with an
// UpgradeOutcomeFailed Result carrying failure, preserving that Result's
// own fields otherwise.
func markUpgradeFailed(results []UpgradeResult, idx []int, failure *selfupdate.Failure) {
	for _, i := range idx {
		r := results[i]
		r.Outcome = UpgradeOutcomeFailed
		r.Failure = failure
		results[i] = r
	}
}

// executeTargetUpgrade applies a non-host target's already-planned,
// already-confirmed upgrade through selfupdate.Config.UpdateAt, reusing the
// exact detection PlanUpgrade classified and the exact release tag it
// resolved (cli-install#req:upgrade-resolves-release-once,
// cli-install#req:upgrade-per-target-policy). UpdateAt performs its OWN
// single re-verification lookup that r.Tag is still the latest stable
// release before replacing anything (self-update#req:update-at-classified-
// copy) — a second, deliberate lookup distinct from PlanUpgrade's own one,
// made only for targets that reach execution, never for a --check or
// --dry-run run (see selfupdate.Options.ResolvedTag's own doc comment).
func executeTargetUpgrade(ctx context.Context, target Entry, r UpgradeResult, opts UpgradeOptions) UpgradeResult {
	cfg := upgradeTargetConfig(target, r.Current, opts)
	detection := selfupdate.Detection{Method: r.InstallMethod, Manager: r.Manager, Path: r.ResolvedPath}
	updateOpts := selfupdate.Options{
		ResolvedTag:   r.Tag,
		RunManaged:    opts.Env.RunManaged,
		VerifyManaged: opts.VerifyManaged,
	}
	outcome, err := cfg.UpdateAt(ctx, detection, updateOpts)
	return finalizeUpgradeResult(r, target.SelfUpdateHooks, outcome, err)
}

// executeHostUpgrade is executeTargetUpgrade's host counterpart: it calls
// UpdateAt on the host's OWN selfupdate.Config with the host's OWN
// AfterUpdate hook, so `upgrade <self>` and `self-update` reach the
// identical library call (cli-install#req:host-target-is-running-binary,
// cli-install#req:self-update-equals-upgrade-self). The host never gets a
// FinishHint: its hook already ran for real, rather than being reported as
// something left to finish.
func executeHostUpgrade(ctx context.Context, r UpgradeResult, opts UpgradeOptions) UpgradeResult {
	cfg := upgradeHostConfig(opts)
	detection := selfupdate.Detection{Method: r.InstallMethod, Manager: r.Manager, Path: r.ResolvedPath}
	updateOpts := selfupdate.Options{
		ResolvedTag:   r.Tag,
		RunManaged:    opts.Env.RunManaged,
		VerifyManaged: opts.VerifyManaged,
		AfterUpdate:   opts.HostAfterUpdate,
	}
	outcome, err := cfg.UpdateAt(ctx, detection, updateOpts)
	return finalizeUpgradeResult(r, false, outcome, err)
}

// finalizeUpgradeResult turns a completed UpdateAt call into r's terminal
// UpgradeOutcome via mapAction, carrying its warnings and — for a non-host
// target whose catalog entry declares SelfUpdateHooks — the finish hint
// (cli-install#req:self-update-hook-hint: "When such a target other than
// the host is upgraded or has its manager command executed").
func finalizeUpgradeResult(r UpgradeResult, hooks bool, outcome selfupdate.Outcome, err error) UpgradeResult {
	r.Outcome, r.Failure, _, _ = applyMappedAction(r, outcome, err)
	if r.Failure != nil {
		return r
	}

	if outcome.PostSwapWarning != nil {
		r.Warnings = append(r.Warnings, outcome.PostSwapWarning.Error())
	}
	if outcome.AfterUpdateWarning != nil {
		r.Warnings = append(r.Warnings, outcome.AfterUpdateWarning.Error())
	}
	if hooks && (r.Outcome == UpgradeOutcomeUpgraded || r.Outcome == UpgradeOutcomeManagerExecuted) {
		r.FinishHint = r.Target + " self-update"
		r.Warnings = append(r.Warnings, fmt.Sprintf("finish updating %s by running `%s self-update`", r.Target, r.Target))
	}
	// AssetURL is meaningful only for a still-pending preview; a terminal
	// execute result never carries one.
	r.AssetURL = ""
	return r
}

// --- convenience entry points -------------------------------------------

// Upgrade is the PlanUpgrade + confirm + ExecuteUpgrade convenience: it
// plans the whole batch, and — unless opts.DryRun — executes it, mirroring
// Install's own Plan + confirm + Execute contract exactly.
func Upgrade(ctx context.Context, names []string, opts UpgradeOptions) (UpgradeBatchResult, error) {
	plan, err := PlanUpgrade(ctx, names, opts)
	if err != nil {
		return plan, err
	}
	if opts.DryRun {
		return plan, nil
	}
	return ExecuteUpgrade(ctx, plan, opts)
}

// --- release Config and GitHub bearer auth -------------------------------

// upgradeTargetConfig builds a non-host target's selfupdate.Config for
// looking up or applying its upgrade: target's own catalog identity at its
// probed currentVersion, opts.ConfigureRelease applied when set (the
// release-endpoint injection point cli-install#req:no-network-in-tests
// requires), then a bearer-authenticated HTTPClient default when the
// caller — the catalog, or ConfigureRelease — left one unset.
func upgradeTargetConfig(target Entry, currentVersion string, opts UpgradeOptions) selfupdate.Config {
	cfg := target.Config(currentVersion)
	if opts.ConfigureRelease != nil {
		cfg = opts.ConfigureRelease(target, cfg)
	}
	return withGitHubAuth(cfg, opts.Env.Getenv)
}

// upgradeHostConfig applies the same bearer-authenticated HTTPClient
// default to opts.HostConfig — self-update and upgrade <self> both reach
// GitHub with the same token policy as every other target.
func upgradeHostConfig(opts UpgradeOptions) selfupdate.Config {
	return withGitHubAuth(opts.HostConfig, opts.Env.Getenv)
}

// withGitHubAuth defaults cfg.HTTPClient to githubHTTPClient's bearer-
// authenticated client, leaving an already-configured one (a test's fake
// release server, or a host's own explicit choice) untouched EXCEPT for
// wrapping its existing Transport so the bearer-auth/rate-limit behavior
// still applies (task-22 review M5: a host or ConfigureRelease that sets
// its own HTTPClient must not silently lose the GH_TOKEN message).
func withGitHubAuth(cfg selfupdate.Config, getenv func(string) string) selfupdate.Config {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = githubHTTPClient(getenv, nil)
		return cfg
	}
	wrapped := *cfg.HTTPClient
	wrapped.Transport = githubAuthTransportFor(getenv, cfg.HTTPClient.Transport)
	cfg.HTTPClient = &wrapped
	return cfg
}

// githubAPIHost is the only host cli-install#req:upgrade-release-lookups-
// bounded permits sending a caller's GH_TOKEN/GITHUB_TOKEN to. A release
// asset download resolves against a Config.DownloadURL host — plain
// "github.com" by default — never this one, so githubAuthTransport's own
// host check is what keeps the token off downloads on other hosts, by
// construction, not by a second, separate check anywhere else.
const githubAPIHost = "api.github.com"

// githubHTTPClient returns an *http.Client whose RoundTripper adds
// GH_TOKEN's (or, if unset, GITHUB_TOKEN's) value as a bearer token on
// requests to api.github.com only, and never logs it — it is added to an
// Authorization header sent over HTTPS and appears nowhere else, matching
// selfupdate's own release lookups' "least exposure" posture for the token
// they don't need. getenv nil, or both variables unset, yields an
// unauthenticated client identical to selfupdate's own http.DefaultClient
// fallback. base wraps an existing RoundTripper (nil for the plain
// http.DefaultTransport fallback) — see githubAuthTransportFor.
func githubHTTPClient(getenv func(string) string, base http.RoundTripper) *http.Client {
	return &http.Client{Transport: githubAuthTransportFor(getenv, base)}
}

func githubAuthTransportFor(getenv func(string) string, base http.RoundTripper) http.RoundTripper {
	var token string
	if getenv != nil {
		token = getenv("GH_TOKEN")
		if token == "" {
			token = getenv("GITHUB_TOKEN")
		}
	}
	return &githubAuthTransport{base: base, token: token}
}

// githubRateLimitError is returned by githubAuthTransport.RoundTrip in
// place of a response GitHub's own rate-limit headers mark as exhausted, so
// the message cli-install#req:upgrade-release-lookups-bounded requires
// ("the message MUST say the API rate limit was reached") survives all the
// way through selfupdate.Config.LatestRelease's own *Failure wrapping. The
// GH_TOKEN remedy is named only when no token was actually sent
// (task-22 review M6): a caller that already set one and is still
// rate-limited needs a different remedy (a higher-limit token, or simply
// waiting), and telling them to "set GH_TOKEN" when they already did is
// actively misleading. That package exposes no response headers to its own
// callers — only a status-code-and-body-derived message — which cannot by
// itself distinguish an exhausted rate limit from any other 403;
// substituting this error at the transport level, before selfupdate's own
// status-code handling ever runs, is what makes the distinction visible to
// cliinstall without changing selfupdate itself.
type githubRateLimitError struct{ hadToken bool }

func (e *githubRateLimitError) Error() string {
	if e.hadToken {
		return "GitHub API rate limit reached even with a bearer token set"
	}
	return "GitHub API rate limit reached; set GH_TOKEN or GITHUB_TOKEN to raise it"
}

// githubAuthTransport wraps base (nil meaning http.DefaultTransport) to add
// the bearer Authorization header described on githubHTTPClient, and to
// translate an exhausted-rate-limit response into githubRateLimitError.
type githubAuthTransport struct {
	base  http.RoundTripper
	token string
}

func (t *githubAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	// task-22 review M4: the token is a bearer credential and MUST NOT be
	// sent in cleartext — only ever attached over https, even though every
	// production ReleasesAPIURL default already is https; a caller that
	// points ConfigureRelease at a plain-http test double should never see
	// the token either.
	if t.token != "" && req.URL.Host == githubAPIHost && req.URL.Scheme == "https" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		resp.Header.Get("X-RateLimit-Remaining") == "0" {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, &githubRateLimitError{hadToken: t.token != ""}
	}
	return resp, nil
}
