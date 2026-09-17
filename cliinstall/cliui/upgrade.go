package cliui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
	selfcliui "github.com/strongo/cli-helpers/selfupdate/cliui"
)

// UpgradeRow is one target's combined catalog, relevance and upgrade-result
// data for the report, dry-run preview or executed-result view. Unlike
// install's Row, Result is never nil: cliinstall.PlanUpgrade/CheckUpgrades/
// ExecuteUpgrade/Upgrade always produce one cliinstall.UpgradeResult per
// candidate — there is no bare-status-only row upgrade ever shows, since
// REQ: upgrade-no-args-reports' own bare report IS a full plan over the
// --all set, not a separate, cheaper listing.
type UpgradeRow struct {
	// Entry is the target's catalog entry (its own, or the host's own entry
	// for the host row).
	Entry cliinstall.Entry
	// Relevant reports whether Entry is one of the host's relevant targets
	// (cli-install#req:relevance-matrix). Always false for the host row
	// itself — a target is never relevant to itself.
	Relevant bool
	// Relevance is the host -> target relevance text; empty when Relevant
	// is false.
	Relevance string
	// Result is this target's planned or executed upgrade outcome.
	Result cliinstall.UpgradeResult
}

// UpgradeRowName is the id this row is about.
func UpgradeRowName(row UpgradeRow) string {
	return row.Result.Target
}

// upgradeLocated reports whether row.Result's InstallMethod/Manager/
// ResolvedPath/Command are meaningful: always for the host row (the host is
// by definition the running, located executable), and for a non-host row
// whenever classification actually ran — every outcome except
// UpgradeOutcomeNotInstalled and UpgradeOutcomeUnrecognized, which return
// from planTargetRow before classifyForUpgrade is ever called.
func upgradeLocated(row UpgradeRow) bool {
	if row.Result.Host {
		return true
	}
	switch row.Result.Outcome {
	case cliinstall.UpgradeOutcomeNotInstalled, cliinstall.UpgradeOutcomeUnrecognized:
		return false
	default:
		return true
	}
}

// upgradeStatusToken renders the base "status" field REQ: machine-readable-
// output already requires (extended, not replaced, by upgrade's own added
// fields): the probed cliinstall.State token for a non-host row, or
// "installed" for the host row, whose Status is always zero (Current, not
// Status.Version, carries its version — see UpgradeResult.Status's own doc
// comment) since the host is by definition installed and running.
func upgradeStatusToken(row UpgradeRow) string {
	if row.Result.Host {
		return cliinstall.Installed.String()
	}
	return row.Result.Status.State.String()
}

// upgradeMethodLabel renders row's install method the way statusSummary's
// own methodLabel does for install, decoupled from a cliinstall.Status so it
// works identically for the host row, which carries no Status at all.
func upgradeMethodLabel(row UpgradeRow) string {
	switch row.Result.InstallMethod {
	case selfupdate.Managed:
		if row.Result.Manager != nil {
			return row.Result.Manager.Name
		}
		return "managed"
	case selfupdate.Manual:
		return "manual"
	default:
		return "ambiguous"
	}
}

// --- text -------------------------------------------------------------

// WriteUpgradeReport writes rows' terse, one-line-per-target upgrade view to
// out, and every row's warnings to errOut. It is the ONE writer for the
// read-only report/--check, the pre-confirmation preview REQ: details-
// before-install requires, and the executed/dry-run result alike: the
// founder's own brief asks for terse, scannable output, and every fact
// those REQs require (current/latest version, verdict, manager command,
// resolved path) already fits on one line, so there is no separate verbose
// form to keep in sync with it (unlike install's WriteResult/WriteOutcome
// pair). batchErr is a batch-level refusal (an unknown name, or the
// confirmation gate's own non-interactive refusal) reported on its own line
// first, exactly as WriteResult reports one for install.
func WriteUpgradeReport(out, errOut io.Writer, rows []UpgradeRow, batchErr error) {
	if batchErr != nil {
		fmt.Fprintf(out, "Refused: %s\n", batchErr) //nolint:errcheck
	}
	for _, row := range rows {
		fmt.Fprintf(out, "%s  %s\n", UpgradeRowName(row), upgradeLine(row)) //nolint:errcheck
	}
	writeUpgradeWarnings(errOut, rows)
}

// WriteUpgradeNextStep writes REQ: upgrade-no-args-reports' own closing
// line — the bare report's "end with the next step" requirement — naming
// both ways to actually act on what the report just showed. It is never
// called for --check or a named/--all invocation: those are already the
// "next step" the bare report points to.
func WriteUpgradeNextStep(out io.Writer, host string) {
	fmt.Fprintf(out, "Next: %s upgrade --all   or   %s upgrade <name>\n", host, host) //nolint:errcheck
}

// upgradeLine renders row.Result as the one-line planned or completed
// action, switched on Outcome, matching the founder's own examples: bare
// current/latest tokens (no "v" prefix, unlike install's versionLabel), a
// parenthetical naming the method or manager, and a trailing hint where one
// exists.
func upgradeLine(row UpgradeRow) string {
	r := row.Result
	switch r.Outcome {
	case cliinstall.UpgradeOutcomeDryRun:
		if r.InstallMethod == selfupdate.Managed && r.Manager != nil {
			return fmt.Sprintf("%s → %s  upgrade available (%s: %s)%s", r.Current, r.Latest, r.Manager.Name, r.Command, nonReleaseTag(r))
		}
		return fmt.Sprintf("%s → %s  upgrade available (%s, %s%s)%s", r.Current, r.Latest, upgradeMethodLabel(row), r.ResolvedPath, assetSuffix(r), nonReleaseTag(r))
	case cliinstall.UpgradeOutcomeRedirected:
		manager := "package manager"
		if r.Manager != nil {
			manager = r.Manager.Name
		}
		if r.Command == "" && r.Hint != "" {
			// r.Hint is prose (e.g. the built-in system-package manager's
			// "the package manager that installed it..."), never a
			// copy-pasteable command — "run:" would make that prose lie.
			return fmt.Sprintf("%s → %s  managed by %s — update it with %s", r.Current, r.Latest, manager, r.Hint)
		}
		return fmt.Sprintf("%s → %s  managed by %s — run: %s", r.Current, r.Latest, manager, r.Command)
	case cliinstall.UpgradeOutcomeAlreadyCurrent:
		return fmt.Sprintf("%s  up to date", r.Current)
	case cliinstall.UpgradeOutcomeAhead:
		return fmt.Sprintf("%s  ahead of latest %s", r.Current, r.Latest)
	case cliinstall.UpgradeOutcomeSkippedNonRelease:
		return fmt.Sprintf("%s  skipped: not a release build (name it to upgrade)", r.Current)
	case cliinstall.UpgradeOutcomeNotInstalled:
		return fmt.Sprintf("not installed — run '%s'", r.InstallHint)
	case cliinstall.UpgradeOutcomeUnrecognized:
		return fmt.Sprintf("unrecognized copy at %s — never touched", r.Status.Path)
	case cliinstall.UpgradeOutcomeRefused:
		// Latest is only ever known here when a separate lookup ran (--check
		// via CheckUpgrades, which still resolves it for display even for a
		// refused target); PlanUpgrade/ExecuteUpgrade's own ambiguous path
		// never reaches a lookup at all (selfupdate.Config.UpdateAt's own
		// ambiguous check fails before one), so Latest is empty there —
		// shown as a bare current version rather than a dangling "→ ".
		if r.Latest == "" {
			return fmt.Sprintf("%s  ambiguous install at %s — update manually", r.Current, r.ResolvedPath)
		}
		return fmt.Sprintf("%s → %s  ambiguous install at %s — update manually", r.Current, r.Latest, r.ResolvedPath)
	case cliinstall.UpgradeOutcomeDeclined:
		return "declined; nothing upgraded"
	case cliinstall.UpgradeOutcomeUpgraded:
		line := fmt.Sprintf("upgraded %s → %s at %s", r.Current, r.Tag, r.ResolvedPath)
		return withFinishHint(line, r.FinishHint)
	case cliinstall.UpgradeOutcomeManagerExecuted:
		manager := "package manager"
		if r.Manager != nil {
			manager = r.Manager.Name
		}
		line := fmt.Sprintf("upgrade command completed via %s", manager)
		return withFinishHint(line, r.FinishHint)
	case cliinstall.UpgradeOutcomeFailed:
		kind := "unexpected"
		msg := ""
		if r.Failure != nil {
			kind = r.Failure.Kind.String()
			msg = r.Failure.Error()
		}
		return fmt.Sprintf("failed (%s): %s", kind, msg)
	default:
		return "unknown"
	}
}

func withFinishHint(line, hint string) string {
	if hint == "" {
		return line
	}
	return line + " (finish: `" + hint + "`)"
}

// assetSuffix appends the exact release-asset URL a pending manual
// replacement would fetch, when one is known (cli-install#req:upgrade-
// batch-semantics: "version transition, asset URL and path" — task-22
// review S3). Empty for a managed pending row, which names its Command
// instead, and for any row that never reached a planned manual
// replacement.
func assetSuffix(r cliinstall.UpgradeResult) string {
	if r.AssetURL == "" {
		return ""
	}
	return ", asset " + r.AssetURL
}

// nonReleaseTag marks a row that is only being offered because it was
// explicitly named despite being a non-release build (cli-install#req:
// upgrade-skips-non-release-builds: the confirmation "names it as a
// non-release build" — task-22 review S3).
func nonReleaseTag(r cliinstall.UpgradeResult) string {
	if !r.NonReleaseBuild {
		return ""
	}
	return " (non-release build)"
}

// writeUpgradeWarnings writes every row's warnings to errOut, one per line,
// prefixed with the target's own id, mirroring install's own writeWarnings
// (cli-install#req:machine-readable-output's stderr convention, applied to
// text output too). Unlike install's Row, UpgradeResult already folds its
// Status's own warnings in at plan time (see planTargetRow/planHostRow), so
// there is no second source to deduplicate against here.
func writeUpgradeWarnings(errOut io.Writer, rows []UpgradeRow) {
	for _, row := range rows {
		name := UpgradeRowName(row)
		for _, w := range row.Result.Warnings {
			fmt.Fprintf(errOut, "upgrade: warning: %s: %s\n", name, w) //nolint:errcheck
		}
	}
}

// --- JSON ---------------------------------------------------------------

// upgradeDocument is the --format json shape of WriteUpgradeReportJSON's
// single document, matching resultDocument's own batch-level-failure shape.
type upgradeDocument struct {
	Host        string              `json:"host"`
	Error       string              `json:"error,omitempty"`
	FailureKind string              `json:"failure_kind,omitempty"`
	Targets     []upgradeTargetJSON `json:"targets"`
}

// upgradeTargetJSON is one target's report/dry-run/upgrade-result row: every
// base field REQ: machine-readable-output already requires (task-5), plus
// REQ: upgrade-batch-semantics' own added fields — current, latest, verdict,
// action, command and resolved_path — using "action" (not "outcome",
// install's own field name) exactly as that REQ spells it. The base fields
// reflect Result.Status, which is zero for the host row by design (see
// UpgradeResult.Status's own doc comment); Current/Latest/Verdict/Action are
// what a caller should read for the host, matching upgradeStatusToken and
// upgradeLine's own text-side handling.
type upgradeTargetJSON struct {
	Name          string   `json:"name"`
	Host          bool     `json:"host"`
	Relevant      bool     `json:"relevant"`
	Description   string   `json:"description"`
	Relevance     string   `json:"relevance"`
	Status        string   `json:"status"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	Date          string   `json:"date"`
	DateSource    string   `json:"date_source"`
	Path          string   `json:"path"`
	OtherPaths    []string `json:"other_paths"`
	InstallMethod string   `json:"install_method"`
	Manager       string   `json:"manager"`
	VersionSource string   `json:"version_source"`
	Warnings      []string `json:"warnings"`

	Current      string `json:"current"`
	Latest       string `json:"latest,omitempty"`
	Tag          string `json:"tag,omitempty"`
	Verdict      string `json:"verdict,omitempty"`
	Action       string `json:"action"`
	Command      string `json:"command,omitempty"`
	Hint         string `json:"hint,omitempty"`
	ResolvedPath string `json:"resolved_path,omitempty"`
	// AssetURL and NonReleaseBuild carry the same facts the text preview
	// shows (task-22 review S3): the exact asset URL a pending manual
	// replacement would fetch, and whether this target is offered only
	// because it was explicitly named despite being a non-release build.
	AssetURL        string `json:"asset_url,omitempty"`
	NonReleaseBuild bool   `json:"non_release_build,omitempty"`
	InstallHint     string `json:"install_hint,omitempty"`
	FinishHint      string `json:"finish_hint,omitempty"`
	FailureKind     string `json:"failure_kind,omitempty"`
	Error           string `json:"error,omitempty"`
}

func rowToUpgradeJSON(row UpgradeRow) upgradeTargetJSON {
	r := row.Result
	s := r.Status
	t := upgradeTargetJSON{
		Name:        UpgradeRowName(row),
		Host:        r.Host,
		Relevant:    row.Relevant,
		Description: row.Entry.Description,
		Relevance:   row.Relevance,
		Status:      upgradeStatusToken(row),
		Version:     s.Version,
		Commit:      s.Commit,
		Date:        s.Date,
		DateSource:  s.DateSource,
		Path:        s.Path,
		// Exactly one of s.OtherPaths (a non-host target's own probed
		// Status) or r.OtherPaths (the host's own additional-PATH-copy
		// list, Status being always zero for the host — task-22 review M2)
		// is ever non-empty, so concatenating both carries whichever one
		// applies.
		OtherPaths:      append(append([]string(nil), s.OtherPaths...), r.OtherPaths...),
		VersionSource:   s.VersionSource.String(),
		Warnings:        append([]string(nil), r.Warnings...),
		Current:         r.Current,
		Latest:          r.Latest,
		Tag:             r.Tag,
		Action:          r.Outcome.String(),
		Command:         r.Command,
		Hint:            r.Hint,
		ResolvedPath:    r.ResolvedPath,
		AssetURL:        r.AssetURL,
		NonReleaseBuild: r.NonReleaseBuild,
		InstallHint:     r.InstallHint,
		FinishHint:      r.FinishHint,
	}
	if r.Latest != "" {
		// Latest is set only once resolveUpgradeRow's lookup actually
		// succeeded, so this is exactly "a lookup ran" — the same gate
		// REQ: upgrade-release-lookups-bounded uses to decide whether a
		// target was looked up at all.
		t.Verdict = r.Verdict.String()
	}
	if upgradeLocated(row) {
		t.InstallMethod = r.InstallMethod.String()
		if r.Manager != nil {
			t.Manager = r.Manager.Name
		}
	}
	if r.Failure != nil {
		t.FailureKind = r.Failure.Kind.String()
		t.Error = r.Failure.Error()
	}
	return t
}

// WriteUpgradeReportJSON writes rows' cli-install#req:machine-readable-
// output report/dry-run/upgrade-result document — one JSON object with
// "host" and one "targets" entry per row — to out, and every row's warnings
// to errOut. batchErr is a batch-level failure (an unknown name, or the
// confirmation gate's own non-interactive refusal); nil for an ordinary
// result. Either way exactly one JSON document is written, mirroring
// WriteResultJSON's own "never an empty document" rule.
func WriteUpgradeReportJSON(out, errOut io.Writer, host string, rows []UpgradeRow, batchErr error) error {
	doc := upgradeDocument{Host: host, Targets: make([]upgradeTargetJSON, len(rows))}
	if batchErr != nil {
		doc.Error = batchErr.Error()
		doc.FailureKind = selfupdate.KindOf(batchErr).String()
	}
	for i, row := range rows {
		doc.Targets[i] = rowToUpgradeJSON(row)
	}
	if err := json.NewEncoder(out).Encode(doc); err != nil {
		return err
	}
	writeUpgradeWarnings(errOut, rows)
	return nil
}

// --- confirmation ---------------------------------------------------------

// UpgradeConfirm builds a cliinstall.UpgradeOptions.Confirm callback
// (cli-install#req:upgrade-batch-semantics inheriting cli-install#req:
// confirmation-gate: "one confirmation covering every target that would be
// replaced or have a manager command executed"), mirroring Confirm's own
// install-side shape and refusal rules exactly — see that function's own
// doc comment for the interactive/non-interactive/empty-answer contract,
// which this callback reuses verbatim, only asking "Upgrade" instead of
// "Install".
func UpgradeConfirm(opts ConfirmOptions) func(pending []cliinstall.UpgradeResult) (bool, error) {
	interactive := opts.Interactive
	if interactive == nil {
		interactive = selfcliui.IsTerminal
	}
	return func(pending []cliinstall.UpgradeResult) (bool, error) {
		names := make([]string, len(pending))
		for i, r := range pending {
			// task-22 review S3: the confirmation itself, not only a
			// separate stderr warning, names a non-release build so the
			// person answering "y" knows this target is being moved onto
			// the latest release rather than merely refreshed.
			names[i] = r.Target + nonReleaseTag(r)
		}
		if !interactive() {
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("--yes is required for non-interactive use; refusing to upgrade %s", strings.Join(names, ", ")),
			}
		}
		_, _ = fmt.Fprintf(opts.Out, "Upgrade %s? [y/N] ", strings.Join(names, ", "))
		reader := bufio.NewReader(opts.In)
		line, readErr := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("no answer read from stdin; pass --yes to upgrade without confirmation"),
			}
		}
		return answer == "y" || answer == "yes", nil
	}
}
