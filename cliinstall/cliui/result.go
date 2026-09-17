package cliui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// resultDocument is the --format json shape of WriteResultJSON's single
// document (cli-install#req:machine-readable-output). It carries the same
// per-target facts as listDocument, plus the install-specific facts named
// by REQ: multi-target-batch and REQ: install-dry-run: the outcome, the
// method actually chosen, the destination or brew argv, and, for a
// failure, its typed kind and message.
//
// Error/FailureKind are set only for a BATCH-level failure — an unknown
// name refused before any target was probed, or the confirmation gate's
// own non-interactive refusal — so the document stays exactly one JSON
// object even then, with Targets present (possibly empty) alongside it
// (task-5 review S2: "A batch-level refusal... stdout gets nothing").
type resultDocument struct {
	Host        string             `json:"host"`
	Error       string             `json:"error,omitempty"`
	FailureKind string             `json:"failure_kind,omitempty"`
	Targets     []resultTargetJSON `json:"targets"`
}

// resultTargetJSON is one target's details/dry-run/install-result row.
// Every field REQ: machine-readable-output names MUST be present, even
// when empty (task-5 review M5) — see listTargetJSON's own doc comment for
// the same rule. "details" is present here (unlike listTargetJSON), per
// that REQ's "details and install only" scoping.
type resultTargetJSON struct {
	Name          string   `json:"name"`
	Relevant      bool     `json:"relevant"`
	Description   string   `json:"description"`
	Details       string   `json:"details"`
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
	// Output is the observed probe output for an unrecognized copy
	// (cli-install#req:status-probe-order — task-5 review S4).
	Output string `json:"output"`

	// Outcome and the fields below describe row.Plan — REQ:
	// machine-readable-output's field list does not name these
	// individually, but "Every fact shown in text MUST be present in JSON
	// with full values" does, and the planned/completed action is exactly
	// such a fact (cli-install#req:details-before-install,
	// cli-install#req:install-dry-run). They are left absent (via
	// omitempty) ONLY for a target that was never planned at all — an
	// unknown name — where every one of them would otherwise be a
	// meaningless zero value, e.g. a phantom "method":"direct" for a name
	// that was refused before planning ever ran (task-5 review M6).
	Outcome        string   `json:"outcome,omitempty"`
	Method         string   `json:"method,omitempty"`
	Destination    string   `json:"destination,omitempty"`
	CaskArgv       []string `json:"cask_argv,omitempty"`
	AssetURL       string   `json:"asset_url,omitempty"`
	PlannedVersion string   `json:"planned_version,omitempty"`
	Tag            string   `json:"tag,omitempty"`
	UpdateHint     string   `json:"update_hint,omitempty"`
	FailureKind    string   `json:"failure_kind,omitempty"`
	Error          string   `json:"error,omitempty"`
}

func rowToResultJSON(row Row) resultTargetJSON {
	s := row.Status
	t := resultTargetJSON{
		Name:          RowName(row),
		Relevant:      row.Relevant,
		Description:   row.Entry.Description,
		Details:       row.Entry.Details,
		Relevance:     row.Relevance,
		Status:        s.State.String(),
		Version:       s.Version,
		Commit:        s.Commit,
		Date:          s.Date,
		DateSource:    s.DateSource,
		Path:          s.Path,
		OtherPaths:    s.OtherPaths,
		InstallMethod: installMethodJSON(s),
		VersionSource: s.VersionSource.String(),
		Warnings:      dedupedWarnings(row),
		Output:        s.Output,
	}
	if s.State != cliinstall.NotInstalled && s.Manager != nil {
		t.Manager = s.Manager.Name
	}
	if p := row.Plan; p != nil {
		t.Outcome = p.Outcome.String()
		if p.Failure != nil {
			t.FailureKind = p.Failure.Kind.String()
			t.Error = p.Failure.Error()
		}
		if isUnknownTarget(p) {
			// No method/destination/cask/version/tag was ever planned for
			// a name that was never a catalog id — Outcome and the
			// failure fields above are the whole story (task-5 review M6).
			return t
		}
		t.Method = p.Method.String()
		t.Destination = p.Destination
		t.CaskArgv = p.CaskArgv
		t.AssetURL = p.AssetURL
		t.PlannedVersion = p.Version
		t.Tag = p.Tag
		t.UpdateHint = p.UpdateHint
	}
	return t
}

// isUnknownTarget reports whether p is a name that was never a catalog id
// at all (cli-install#req:unknown-target-refused) — a target that was
// refused before Plan ever chose a method or destination for it, so those
// fields would otherwise be meaningless zero values (task-5 review M6).
func isUnknownTarget(p *cliinstall.Result) bool {
	return p.Failure != nil && p.Failure.Kind == selfupdate.KindUnknownTarget
}

// WriteResult writes rows' human-readable details/dry-run/install-result
// view to out (cli-install#req:details-before-install,
// cli-install#req:install-dry-run, cli-install#req:multi-target-batch), and
// every row's warnings to errOut. A row with a nil Plan is rendered as a
// bare status/relevance preview (no planned or completed action line); a
// row with Plan set adds that action, rendered per its Outcome. batchErr is
// a batch-level failure that happened before, or independent of, any
// per-target row (an unknown name, or the confirmation gate's own
// non-interactive refusal) — reported as its own line so a batch-level
// refusal is never silent in text mode either (task-5 review S2).
//
// As with WriteList, individual terminal write errors are not propagated —
// see that function's own doc comment.
func WriteResult(out, errOut io.Writer, host string, rows []Row, batchErr error) {
	if batchErr != nil {
		fmt.Fprintf(out, "Refused: %s\n", batchErr) //nolint:errcheck
		if len(rows) > 0 {
			fmt.Fprintln(out) //nolint:errcheck
		}
	}
	for i, row := range rows {
		if i > 0 {
			fmt.Fprintln(out) //nolint:errcheck
		}
		writeResultRow(out, host, row)
	}
	writeWarnings(errOut, rows)
}

func writeResultRow(out io.Writer, host string, row Row) {
	name := RowName(row)
	fmt.Fprintf(out, "== %s ==\n", name) //nolint:errcheck
	if row.Entry.Description != "" {
		fmt.Fprintln(out, row.Entry.Description) //nolint:errcheck
	}
	if row.Entry.Details != "" {
		fmt.Fprintln(out)                    //nolint:errcheck
		fmt.Fprintln(out, row.Entry.Details) //nolint:errcheck
	}
	if row.Entry.Homepage != "" {
		fmt.Fprintf(out, "Homepage: %s\n", row.Entry.Homepage) //nolint:errcheck
	}
	if row.Entry.ID != "" {
		if row.Relevant {
			fmt.Fprintf(out, "Why relevant to %s: %s\n", host, row.Relevance) //nolint:errcheck
		} else {
			fmt.Fprintf(out, "Not listed as relevant to %s.\n", host) //nolint:errcheck
		}
		fmt.Fprintf(out, "Status: %s\n", statusSummary(row.Status)) //nolint:errcheck
	}
	if row.Plan != nil {
		fmt.Fprintln(out, planLine(*row.Plan)) //nolint:errcheck
	}
}

// WriteOutcome writes rows' TERSE post-execution report to out — one line
// per target, its id and outcome only — and every row's warnings to
// errOut. This is the report Execute's own result gets, printed AFTER the
// full WriteResult preview a caller already showed once before confirming:
// repeating every description, details, homepage and relevance block a
// second time would be exactly the duplicated output task-5 review B1
// found ("every description and details block prints twice"). batchErr is
// the same batch-level failure WriteResult accepts.
func WriteOutcome(out, errOut io.Writer, rows []Row, batchErr error) {
	if batchErr != nil {
		fmt.Fprintf(out, "Refused: %s\n", batchErr) //nolint:errcheck
	}
	for _, row := range rows {
		name := RowName(row)
		if row.Plan == nil {
			fmt.Fprintf(out, "%s: %s\n", name, statusSummary(row.Status)) //nolint:errcheck
			continue
		}
		line := planLine(*row.Plan)
		line = strings.TrimPrefix(strings.TrimPrefix(line, "Result: "), "Plan: ")
		fmt.Fprintf(out, "%s: %s\n", name, line) //nolint:errcheck
	}
	writeWarnings(errOut, rows)
}

// planLine renders row.Plan's Outcome as the one-line planned or completed
// action cli-install#req:details-before-install and
// cli-install#req:install-dry-run both require: the exact brew command for
// a Homebrew plan or redirect, or the release version, tag and asset URL
// and destination path for a direct one.
func planLine(r cliinstall.Result) string {
	switch r.Outcome {
	case cliinstall.OutcomeDryRun:
		if r.Method == cliinstall.MethodHomebrew {
			return "Plan: " + strings.Join(r.CaskArgv, " ")
		}
		return fmt.Sprintf("Plan: direct install, release %s (tag %s), asset %s, destination %s", versionLabel(r.Version), r.Tag, r.AssetURL, r.Destination)
	case cliinstall.OutcomeAlreadyInstalled:
		return fmt.Sprintf("Result: already installed (%s); update with `%s`", versionLabel(r.Version), r.UpdateHint)
	case cliinstall.OutcomeRedirected:
		return "Result: redirected; run: " + strings.Join(r.CaskArgv, " ")
	case cliinstall.OutcomeDeclined:
		return "Result: declined; nothing installed"
	case cliinstall.OutcomeInstalled:
		if r.Method == cliinstall.MethodHomebrew {
			return fmt.Sprintf("Result: installed via Homebrew (%s)", versionLabel(r.Version))
		}
		return fmt.Sprintf("Result: installed %s at %s", versionLabel(r.Version), r.Destination)
	case cliinstall.OutcomeFailed:
		msg := ""
		kind := "unexpected"
		if r.Failure != nil {
			msg = r.Failure.Error()
			kind = r.Failure.Kind.String()
		}
		return fmt.Sprintf("Result: failed (%s): %s", kind, msg)
	default:
		return "Result: unknown"
	}
}

// WriteResultJSON writes rows' cli-install#req:machine-readable-output
// details/dry-run/install-result document — one JSON object with "host"
// and one "targets" entry per row, each carrying the "details" field the
// bare listing document omits — to out, and every row's warnings to errOut.
// batchErr is a batch-level failure (an unknown name, or the confirmation
// gate's own non-interactive refusal) that happened before, or independent
// of, any per-target row; nil for an ordinary result. Either way exactly
// one JSON document is written (task-5 review S2).
func WriteResultJSON(out, errOut io.Writer, host string, rows []Row, batchErr error) error {
	doc := resultDocument{Host: host, Targets: make([]resultTargetJSON, len(rows))}
	if batchErr != nil {
		doc.Error = batchErr.Error()
		doc.FailureKind = selfupdate.KindOf(batchErr).String()
	}
	for i, row := range rows {
		doc.Targets[i] = rowToResultJSON(row)
	}
	if err := json.NewEncoder(out).Encode(doc); err != nil {
		return err
	}
	writeWarnings(errOut, rows)
	return nil
}
