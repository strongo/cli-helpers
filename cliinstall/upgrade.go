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
type UpgradeOutcome int

const (
	// UpgradeOutcomeUpgraded means a manual install was verified,
	// downloaded and atomically replaced.
	UpgradeOutcomeUpgraded UpgradeOutcome = iota
	// UpgradeOutcomeManagerExecuted means an executable package-manager
	// update ran to completion (cli-install#req:upgrade-per-target-policy).
	UpgradeOutcomeManagerExecuted
	// UpgradeOutcomeRedirected means a redirect-only managed install's
	// upgrade command was reported without running anything.
	UpgradeOutcomeRedirected
	// UpgradeOutcomeAlreadyCurrent means the running version already equals
	// the latest stable release; nothing changed.
	UpgradeOutcomeAlreadyCurrent
	// UpgradeOutcomeAhead means the installed version orders strictly above
	// the latest stable release (self-update#req:ahead-of-latest); nothing
	// changed and this never counts as an available upgrade.
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
	// UpgradeOutcomeRefused means the install method could not be
	// classified (self-update#req:ambiguous-safe-default); nothing changed.
	UpgradeOutcomeRefused
	// UpgradeOutcomeDryRun means this target would be upgraded: it is
	// either --dry-run's own final answer, or PlanUpgrade's "still pending
	// confirmation" marker that ExecuteUpgrade replaces with a terminal
	// outcome (mirroring Result/OutcomeDryRun's own dual role in plan.go).
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
// CheckUpgrades/ExecuteUpgrade/Upgrade call — the shape a future task-22
// output writer flattens into cli-install#req:machine-readable-output's
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
	// the host it is DetectSelf's own classification of the running
	// executable. Meaningful only when Target was located and Installed.
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
	// selfupdate.Options.ResolvedTag so Execute never re-resolves "latest"
	// a second time (cli-install#req:upgrade-resolves-release-once).
	Tag string
	// Verdict is the comparison between Current and Latest, set once a
	// lookup has completed. Zero (selfupdate.UpToDate) when no lookup ran.
	Verdict selfupdate.Verdict

	// Command is the manager's display upgrade command, set whenever
	// Manager is non-nil, regardless of Outcome.
	Command string
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
	// never touches, its only state). Zero for the host: use Current, not
	// Status.Version, for the host's own version.
	Status Status
	// Failure is set exactly when Outcome is UpgradeOutcomeFailed.
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

// Failed reports whether at least one Result has UpgradeOutcomeFailed —
// mirroring BatchResult.Failed(), the only outcome that counts as a batch
// failure; a refused, unrecognized, not-installed, ahead, or skipped target
// is a descriptive state, not something that went wrong this run.
func (b UpgradeBatchResult) Failed() bool {
	for _, r := range b.Results {
		if r.Outcome == UpgradeOutcomeFailed {
			return true
		}
	}
	return false
}

// Failure returns nil when b did not fail, and otherwise a *BatchFailure
// carrying every failed target's typed *selfupdate.Failure — the same
// aggregate type BatchResult.Failure returns, so a host's ErrorMapper
// handles both install and upgrade batches through one code path.
func (b UpgradeBatchResult) Failure() error {
	var failures []*selfupdate.Failure
	for _, r := range b.Results {
		if r.Outcome == UpgradeOutcomeFailed && r.Failure != nil {
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
	// UpdateAt call — the SAME closure its `self-update` command
	// configures, so `upgrade <self>` runs the identical hook `self-update`
	// does (cli-install#req:self-update-equals-upgrade-self). Never used
	// for any other target: v1 reports a FinishHint instead of running
	// another CLI's hooks (cli-install#req:self-update-hook-hint).
	HostAfterUpdate selfupdate.AfterUpdateFunc

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
// given order to preserve for this set, unlike named targets.
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
func classifyForUpgrade(status Status, managers []selfupdate.Manager) selfupdate.Detection {
	if det := selfupdate.Classify(status.Path, managers); det.Method == selfupdate.Managed {
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
// skips-non-release-builds' own shape. No "+" build metadata is permitted
// by this pattern at all, which is what makes the separate literal "+"
// check below redundant-but-explicit rather than load-bearing on its own.
var releaseVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

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

// --- version comparison (mirrors selfupdate's own unexported checkAgainst,
// using only its exported CompareVersions, so cli-install can resolve a
// target's release EXACTLY ONCE via Config.LatestRelease during planning
// rather than also calling the unexported-equivalent Config.Check, which
// would be a second lookup for the same information) ----------------------

// versionFromTag mirrors selfupdate.Config's own unexported versionFromTag:
// strip tagPrefix, then a leading "v".
func versionFromTag(tag, tagPrefix string) string {
	return normalizeVersion(strings.TrimPrefix(tag, tagPrefix))
}

// normalizeVersion mirrors selfupdate's own unexported normalize: strip a
// single leading "v".
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// compareVersion computes the same CheckResult triple selfupdate's own
// checkAgainst would, given a Config and an already-resolved latest tag —
// current version, latest version, and the verdict comparing them.
func compareVersion(cfg selfupdate.Config, tag string) (current, latest string, verdict selfupdate.Verdict) {
	latest = versionFromTag(tag, cfg.TagPrefix)
	if containsString(effectiveUndetermined(cfg.UndeterminedVersions), cfg.CurrentVersion) {
		return cfg.CurrentVersion, latest, selfupdate.Undetermined
	}
	current = normalizeVersion(cfg.CurrentVersion)
	switch cmp := selfupdate.CompareVersions(current, latest); {
	case cmp == 0:
		verdict = selfupdate.UpToDate
	case cmp > 0:
		verdict = selfupdate.Ahead
	default:
		verdict = selfupdate.UpdateAvailable
	}
	return current, latest, verdict
}

// decidePlan maps a resolved verdict and classification onto the
// UpgradeOutcome PlanUpgrade reports, per cli-install#req:upgrade-per-
// target-policy's table. nonReleaseProceeds is true only for an explicitly
// named non-release build (cli-install#req:upgrade-skips-non-release-
// builds): it is the one case an Undetermined verdict still proceeds
// instead of being treated as a no-op, because there is no "current" to be
// already equal to.
func decidePlan(nonReleaseProceeds bool, verdict selfupdate.Verdict, method selfupdate.InstallMethod, manager *selfupdate.Manager) UpgradeOutcome {
	proceed := verdict == selfupdate.UpdateAvailable || (nonReleaseProceeds && verdict == selfupdate.Undetermined)
	if !proceed {
		if verdict == selfupdate.Ahead {
			return UpgradeOutcomeAhead
		}
		return UpgradeOutcomeAlreadyCurrent
	}
	switch method {
	case selfupdate.Ambiguous:
		return UpgradeOutcomeRefused
	case selfupdate.Managed:
		if manager == nil || !manager.CanExecuteUpgrade() {
			return UpgradeOutcomeRedirected
		}
		return UpgradeOutcomeDryRun
	default: // selfupdate.Manual
		return UpgradeOutcomeDryRun
	}
}

// --- planning -------------------------------------------------------------

// upgradePlanRow is PlanUpgrade's own working state for one candidate,
// before and after its (possible) release lookup — result is the row
// PlanUpgrade ultimately returns; the rest is this function's own
// plan-to-lookup handoff.
type upgradePlanRow struct {
	result      UpgradeResult
	needsLookup bool
	cfg         selfupdate.Config
	explicit    bool
	nonRelease  bool
}

// planTargetRow builds a non-host candidate's row up to (but not including)
// its release lookup: NotInstalled and Unrecognized targets, and an
// implicitly-selected non-release build, are already terminal and never
// need one (cli-install#req:upgrade-release-lookups-bounded: "MUST NOT look
// up releases for targets that are not installed, unrecognized, or
// skipped").
func planTargetRow(c upgradeCandidate, target Entry, status Status, opts UpgradeOptions) upgradePlanRow {
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
	}
	r.Current = status.Version

	nonRelease := isNonReleaseBuild(status.Version, target.UndeterminedVersions)
	if nonRelease && !c.explicit {
		r.Outcome = UpgradeOutcomeSkippedNonRelease
		return upgradePlanRow{result: r}
	}
	if det.Method == selfupdate.Ambiguous {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s's install method is ambiguous at %s; update it manually", c.id, det.Path))
	}
	if nonRelease {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s is a non-release build (%s); upgrading will move it to the latest release", c.id, status.Version))
	}

	return upgradePlanRow{result: r, needsLookup: true, cfg: upgradeTargetConfig(target, status.Version, opts), explicit: c.explicit, nonRelease: nonRelease}
}

// planHostRow is planTargetRow's host counterpart: the host is never
// NotInstalled or Unrecognized (it is, by definition, the running
// executable), is classified by the running executable's own path rather
// than a probed Status, and — when the first PATH copy of the host id
// differs from that running executable — carries a warning naming that
// other copy, which is reported but never upgraded
// (cli-install#req:host-target-is-running-binary).
func planHostRow(c upgradeCandidate, hostStatus Status, hostDir string, opts UpgradeOptions) upgradePlanRow {
	r := UpgradeResult{Target: c.id, Host: true, Status: hostStatus}

	hostPath := installFilePath(goosName, hostDir, opts.HostID)
	det := selfupdate.Classify(hostPath, opts.HostConfig.Managers)
	r.InstallMethod = det.Method
	r.Manager = det.Manager
	r.ResolvedPath = det.Path
	if det.Manager != nil {
		r.Command = det.Manager.UpgradeCommand
	}
	if hostStatus.Path != "" && !samePath(hostStatus.Path, hostPath, goosName) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("another copy of %s is on PATH at %s; it was left untouched", c.id, hostStatus.Path))
	}

	current := opts.HostConfig.CurrentVersion
	r.Current = current
	if det.Method == selfupdate.Ambiguous {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s's install method is ambiguous at %s; update it manually", c.id, det.Path))
	}

	nonRelease := isNonReleaseBuild(current, opts.HostConfig.UndeterminedVersions)
	if nonRelease && !c.explicit {
		r.Outcome = UpgradeOutcomeSkippedNonRelease
		return upgradePlanRow{result: r}
	}
	if nonRelease {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s is a non-release build (%s); upgrading will move it to the latest release", c.id, current))
	}

	return upgradePlanRow{result: r, needsLookup: true, cfg: upgradeHostConfig(opts), explicit: c.explicit, nonRelease: nonRelease}
}

// PlanUpgrade validates and locates every target, and — for each one that
// is installed and not skipped — resolves its latest stable release exactly
// once, at most opts.LookupConcurrency at a time, each bounded by
// opts.LookupTimeout (cli-install#req:upgrade-release-lookups-bounded). It
// asks no confirmation, replaces nothing, and runs no manager command:
// PlanUpgrade alone is both `--check`'s and `--dry-run`'s complete answer
// (cli-install#req:upgrade-check, cli-install#req:upgrade-batch-semantics),
// and it is the SAME plan ExecuteUpgrade later acts on — the version and
// tag a caller shows before confirming are never re-resolved by this
// package a second time (cli-install#req:upgrade-resolves-release-once;
// UpdateAt's own ResolvedTag verification inside ExecuteUpgrade is a
// separate, deliberate re-check documented on that call, not a second
// search).
//
// names selects explicit targets; opts.All, or an empty names with
// opts.All false, selects cli-install#req:upgrade-targets' "--all" set
// instead (every installed catalog id, plus the host). An unknown name
// fails the WHOLE batch before anything is probed or looked up
// (cli-install#req:unknown-target-refused), exactly as Plan does; that
// case is the returned error, with an empty BatchResult. Every other
// outcome, including every per-target failure, is reported only in
// BatchResult.Results.
func PlanUpgrade(ctx context.Context, names []string, opts UpgradeOptions) (UpgradeBatchResult, error) {
	opts = opts.withDefaults()
	if _, ok := ByID(opts.HostID); !ok {
		panic("cliinstall: host id " + opts.HostID + " is not in the compiled catalog")
	}

	var (
		candidates []upgradeCandidate
		statusByID map[string]Status
		hostStatus Status
		err        error
	)
	if opts.All || len(names) == 0 {
		candidates, statusByID, hostStatus = allUpgradeCandidates(ctx, opts)
	} else {
		candidates, statusByID, hostStatus, err = namedUpgradeCandidates(ctx, names, opts)
	}
	if err != nil {
		return UpgradeBatchResult{Host: opts.HostID}, err
	}

	hostDir, herr := opts.Env.HostDir()
	if herr != nil {
		hostDir = ""
	}

	rows := make([]upgradePlanRow, len(candidates))
	for i, c := range candidates {
		if c.isHost {
			rows[i] = planHostRow(c, hostStatus, hostDir, opts)
			continue
		}
		e, _ := ByID(c.id) // guaranteed valid: candidates come only from validated names or the catalog itself
		rows[i] = planTargetRow(c, e, statusByID[c.id], opts)
	}

	runUpgradeLookups(ctx, rows, opts)

	results := make([]UpgradeResult, len(rows))
	for i, row := range rows {
		results[i] = row.result
	}
	return UpgradeBatchResult{Host: opts.HostID, Results: results}, nil
}

// runUpgradeLookups resolves every row that needs one concurrently
// (cli-install#req:upgrade-release-lookups-bounded), mutating each row's
// own result in place.
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

// resolveUpgradeRow performs row's own single latest-release lookup and
// turns it into a terminal or pending UpgradeOutcome.
func resolveUpgradeRow(ctx context.Context, row *upgradePlanRow, timeout time.Duration) {
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tag, err := row.cfg.LatestRelease(lookupCtx)
	if err != nil {
		var f *selfupdate.Failure
		errors.As(err, &f)
		row.result.Outcome = UpgradeOutcomeFailed
		row.result.Failure = f
		return
	}

	current, latest, verdict := compareVersion(row.cfg, tag)
	row.result.Current = current
	row.result.Latest = latest
	row.result.Tag = tag
	row.result.Verdict = verdict
	row.result.Outcome = decidePlan(row.nonRelease && row.explicit, verdict, row.result.InstallMethod, row.result.Manager)
}

// --- execution --------------------------------------------------------

var errNoUpgradeConfirmCallback = errors.New("no confirmation callback configured; pass --yes for non-interactive use")

// ExecuteUpgrade upgrades every still-pending target in plan (Outcome ==
// UpgradeOutcomeDryRun) — asking at most one confirmation covering all of
// them (unless opts.Yes) — then applies EXACTLY the release PlanUpgrade
// already resolved for each one via selfupdate.Options.ResolvedTag, with no
// per-target Confirm callback (the batch gate already asked). Every other
// Result in plan (a terminal outcome PlanUpgrade already decided) passes
// through unchanged. Targets execute in plan's own order, which already
// carries the host last (cli-install#req:host-upgraded-last).
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
				return UpgradeBatchResult{Host: plan.Host, Results: results}, confirmErr
			}
		}
		if !proceed {
			for _, i := range pendingIdx {
				r := results[i]
				r.Outcome = UpgradeOutcomeDeclined
				results[i] = r
			}
			return UpgradeBatchResult{Host: plan.Host, Results: results}, nil
		}
	}

	for _, i := range pendingIdx {
		r := results[i]
		if r.Host {
			results[i] = executeHostUpgrade(ctx, r, opts)
			continue
		}
		e, _ := ByID(r.Target) // guaranteed valid: only PlanUpgrade's own rows, all built from a catalog Entry, ever reach here
		results[i] = executeTargetUpgrade(ctx, e, r, opts)
	}

	return UpgradeBatchResult{Host: plan.Host, Results: results}, nil
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
		VerifyManaged: upgradeVerifyManaged(target, opts),
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
	hostEntry, _ := ByID(opts.HostID) // guaranteed valid: PlanUpgrade already panicked otherwise
	detection := selfupdate.Detection{Method: r.InstallMethod, Manager: r.Manager, Path: r.ResolvedPath}
	updateOpts := selfupdate.Options{
		ResolvedTag:   r.Tag,
		RunManaged:    opts.Env.RunManaged,
		VerifyManaged: upgradeVerifyManaged(hostEntry, opts),
		AfterUpdate:   opts.HostAfterUpdate,
	}
	outcome, err := cfg.UpdateAt(ctx, detection, updateOpts)
	return finalizeUpgradeResult(r, false, outcome, err)
}

// finalizeUpgradeResult turns a completed UpdateAt call into r's terminal
// UpgradeOutcome, carrying its warnings and — for a non-host target whose
// catalog entry declares SelfUpdateHooks — the finish hint
// (cli-install#req:self-update-hook-hint: "When such a target other than
// the host is upgraded or has its manager command executed").
func finalizeUpgradeResult(r UpgradeResult, hooks bool, outcome selfupdate.Outcome, err error) UpgradeResult {
	if err != nil {
		var f *selfupdate.Failure
		errors.As(err, &f)
		r.Outcome = UpgradeOutcomeFailed
		r.Failure = f
		return r
	}

	switch outcome.Action {
	case selfupdate.ActionUpdated:
		r.Outcome = UpgradeOutcomeUpgraded
	case selfupdate.ActionManagerExecuted:
		r.Outcome = UpgradeOutcomeManagerExecuted
	case selfupdate.ActionAlreadyCurrent:
		r.Outcome = UpgradeOutcomeAlreadyCurrent
	case selfupdate.ActionAhead:
		r.Outcome = UpgradeOutcomeAhead
	case selfupdate.ActionRedirected:
		r.Outcome = UpgradeOutcomeRedirected
	default:
		// ActionAborted/ActionPlanned never occur here: Confirm is always
		// nil and DryRun is always false for this call.
		r.Outcome = UpgradeOutcomeFailed
		r.Failure = &selfupdate.Failure{Kind: selfupdate.KindUnexpected, Err: fmt.Errorf("upgrade: unexpected action %v", outcome.Action)}
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
	return r
}

// upgradeVerifyManaged builds the selfupdate.ManagedBinaryVerifier UpdateAt
// requires alongside RunManaged for an executable managed update: it
// re-probes target via this package's own Probe (never a second, ad hoc
// process check) and confirms the located copy reports expectedVersion,
// mirroring install.go's own verifyInstalled for the identical purpose.
// Status.ResolvedPath is always populated alongside Status.Path for an
// Installed copy (probeOne sets both together — see status.go's own
// resolvePath), so there is no empty-ResolvedPath case to fall back on
// here.
func upgradeVerifyManaged(target Entry, opts UpgradeOptions) selfupdate.ManagedBinaryVerifier {
	return func(ctx context.Context, _ selfupdate.Detection, _ string, _ []string, expectedVersion string) (selfupdate.ExecutableIdentity, error) {
		statuses := Probe(ctx, []Entry{target}, "", opts.Env.Env, opts.ProbeOptions)
		status := statuses[0]
		if status.State != Installed {
			return selfupdate.ExecutableIdentity{}, fmt.Errorf("could not confirm %s after the manager command completed", target.ID)
		}
		if expectedVersion != "" && status.Version != expectedVersion {
			return selfupdate.ExecutableIdentity{}, fmt.Errorf("%s reports version %s, expected the newly installed %s", target.ID, status.Version, expectedVersion)
		}
		return selfupdate.ExecutableIdentity{Path: status.Path, ResolvedPath: status.ResolvedPath}, nil
	}
}

// --- convenience entry points -------------------------------------------

// CheckUpgrades is `--check`'s own entry point: PlanUpgrade's read-only
// report, unchanged (cli-install#req:upgrade-check: "MUST NOT download,
// write, confirm or invoke a manager").
func CheckUpgrades(ctx context.Context, names []string, opts UpgradeOptions) (UpgradeBatchResult, error) {
	return PlanUpgrade(ctx, names, opts)
}

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
// release server, or a host's own explicit choice) untouched.
func withGitHubAuth(cfg selfupdate.Config, getenv func(string) string) selfupdate.Config {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = githubHTTPClient(getenv)
	}
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
// fallback.
func githubHTTPClient(getenv func(string) string) *http.Client {
	var token string
	if getenv != nil {
		token = getenv("GH_TOKEN")
		if token == "" {
			token = getenv("GITHUB_TOKEN")
		}
	}
	return &http.Client{Transport: &githubAuthTransport{token: token}}
}

// githubRateLimitError is returned by githubAuthTransport.RoundTrip in
// place of a response GitHub's own rate-limit headers mark as exhausted, so
// the message cli-install#req:upgrade-release-lookups-bounded requires
// ("the message MUST say the API rate limit was reached" and name
// GH_TOKEN) survives all the way through selfupdate.Config.LatestRelease's
// own *Failure wrapping. That package exposes no response headers to its
// own callers — only a status-code-and-body-derived message — which cannot
// by itself distinguish an exhausted rate limit from any other 403;
// substituting this error at the transport level, before selfupdate's own
// status-code handling ever runs, is what makes the distinction visible to
// cliinstall without changing selfupdate itself (out of this task's own
// scope: "cliinstall upgrade files only").
type githubRateLimitError struct{}

func (*githubRateLimitError) Error() string {
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
	if t.token != "" && req.URL.Host == githubAPIHost {
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
		return nil, &githubRateLimitError{}
	}
	return resp, nil
}
