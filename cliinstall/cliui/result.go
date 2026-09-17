package cliui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/cliinstall"
)

// resultDocument is the --format json shape of WriteResultJSON's single
// document (cli-install#req:machine-readable-output). It carries the same
// per-target facts as listDocument, plus the install-specific facts named
// by REQ: multi-target-batch and REQ: install-dry-run: the outcome, the
// method actually chosen, the destination or brew argv, and, for a
// failure, its typed kind and message.
type resultDocument struct {
	Host    string             `json:"host"`
	Targets []resultTargetJSON `json:"targets"`
}

type resultTargetJSON struct {
	Name          string   `json:"name"`
	Relevant      bool     `json:"relevant"`
	Description   string   `json:"description,omitempty"`
	Details       string   `json:"details,omitempty"`
	Relevance     string   `json:"relevance,omitempty"`
	Status        string   `json:"status"`
	Version       string   `json:"version,omitempty"`
	Commit        string   `json:"commit,omitempty"`
	Date          string   `json:"date,omitempty"`
	DateSource    string   `json:"date_source,omitempty"`
	Path          string   `json:"path,omitempty"`
	OtherPaths    []string `json:"other_paths,omitempty"`
	InstallMethod string   `json:"install_method,omitempty"`
	Manager       string   `json:"manager,omitempty"`
	VersionSource string   `json:"version_source,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`

	// Outcome and the fields below describe row.Plan — REQ:
	// machine-readable-output's field list does not name these
	// individually, but "Every fact shown in text MUST be present in JSON
	// with full values" does, and the planned/completed action is exactly
	// such a fact (cli-install#req:details-before-install,
	// cli-install#req:install-dry-run).
	Outcome        string   `json:"outcome,omitempty"`
	Method         string   `json:"method,omitempty"`
	Destination    string   `json:"destination,omitempty"`
	CaskArgv       []string `json:"cask_argv,omitempty"`
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
	}
	if s.State != cliinstall.NotInstalled && s.Manager != nil {
		t.Manager = s.Manager.Name
	}
	if row.Plan != nil {
		p := row.Plan
		t.Outcome = p.Outcome.String()
		t.Method = p.Method.String()
		t.Destination = p.Destination
		t.CaskArgv = p.CaskArgv
		t.PlannedVersion = p.Version
		t.Tag = p.Tag
		t.UpdateHint = p.UpdateHint
		if p.Failure != nil {
			t.FailureKind = p.Failure.Kind.String()
			t.Error = p.Failure.Error()
		}
	}
	return t
}

// WriteResult writes rows' human-readable details/dry-run/install-result
// view to out (cli-install#req:details-before-install,
// cli-install#req:install-dry-run, cli-install#req:multi-target-batch), and
// every row's warnings to errOut. A row with a nil Plan is rendered as a
// bare status/relevance preview (no planned or completed action line); a
// row with Plan set adds that action, rendered per its Outcome.
//
// As with WriteList, individual terminal write errors are not propagated —
// see that function's own doc comment.
func WriteResult(out, errOut io.Writer, host string, rows []Row) {
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

// planLine renders row.Plan's Outcome as the one-line planned or completed
// action cli-install#req:details-before-install and
// cli-install#req:install-dry-run both require: the exact brew command for
// a Homebrew plan or redirect, or the release version, tag and destination
// path for a direct one.
func planLine(r cliinstall.Result) string {
	switch r.Outcome {
	case cliinstall.OutcomeDryRun:
		if r.Method == cliinstall.MethodHomebrew {
			return "Plan: " + strings.Join(r.CaskArgv, " ")
		}
		return fmt.Sprintf("Plan: direct install, release v%s (tag %s), destination %s", r.Version, r.Tag, r.Destination)
	case cliinstall.OutcomeAlreadyInstalled:
		return fmt.Sprintf("Result: already installed (v%s); update with `%s`", r.Version, r.UpdateHint)
	case cliinstall.OutcomeRedirected:
		return "Result: redirected; run: " + strings.Join(r.CaskArgv, " ")
	case cliinstall.OutcomeDeclined:
		return "Result: declined; nothing installed"
	case cliinstall.OutcomeInstalled:
		if r.Method == cliinstall.MethodHomebrew {
			return fmt.Sprintf("Result: installed via Homebrew (v%s)", r.Version)
		}
		return fmt.Sprintf("Result: installed v%s at %s", r.Version, r.Destination)
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
func WriteResultJSON(out, errOut io.Writer, host string, rows []Row) error {
	doc := resultDocument{Host: host, Targets: make([]resultTargetJSON, len(rows))}
	for i, row := range rows {
		doc.Targets[i] = rowToResultJSON(row)
	}
	if err := json.NewEncoder(out).Encode(doc); err != nil {
		return err
	}
	writeWarnings(errOut, rows)
	return nil
}
