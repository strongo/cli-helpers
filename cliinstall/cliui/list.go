package cliui

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/strongo/cli-helpers/cliinstall"
)

// listDocument is the --format json shape of WriteListJSON's single
// document (cli-install#req:machine-readable-output).
type listDocument struct {
	Host    string           `json:"host"`
	Targets []listTargetJSON `json:"targets"`
}

// listTargetJSON is one target's listing row. Every field REQ:
// machine-readable-output names MUST be present, even when empty — no
// "omitempty" on any of them (task-5 review M5: "omitempty drops keys the
// spec lists as MUST-present"). "details" is the one field that REQ
// deliberately scopes to "details and install" output, not a bare listing,
// and is the only one genuinely absent from this shape (not merely empty).
type listTargetJSON struct {
	Name          string   `json:"name"`
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
	// Output is the observed probe output for an unrecognized copy
	// (cli-install#req:status-probe-order: "reported with its path and the
	// output that was seen" — task-5 review S4); empty for every other
	// state, including a listing row that omits it structurally the same
	// way as any other zero field above.
	Output string `json:"output"`
}

func rowToListJSON(row Row) listTargetJSON {
	s := row.Status
	t := listTargetJSON{
		Name:          RowName(row),
		Relevant:      row.Relevant,
		Description:   row.Entry.Description,
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
	return t
}

// WriteList writes rows' human-readable listing to out
// (cli-install#req:list-relevant, cli-install#req:list-all) and every row's
// warnings to errOut (cli-install#req:machine-readable-output's stderr
// convention, applied to text output too). rows is exactly what the caller
// wants listed, in the order it should appear: the host's relevant targets
// in matrix order for a bare listing, or every other catalog entry appended
// alphabetically for --all (cli-install#req:list-all) — this package makes
// no catalog decision of its own.
//
// Like selfupdate/cliui's own text writers, individual terminal write
// errors are not propagated — a terminal or pipe write essentially never
// fails in practice, and threading that failure through every line would
// bloat every caller for no real benefit.
func WriteList(out, errOut io.Writer, host string, rows []Row) {
	for i, row := range rows {
		if i > 0 {
			fmt.Fprintln(out) //nolint:errcheck
		}
		writeListRow(out, host, row)
	}
	if len(rows) > 0 {
		fmt.Fprintln(out) //nolint:errcheck
	}
	fmt.Fprintf(out, "Run '%s install <name>' to see details and install.\n", host) //nolint:errcheck
	writeWarnings(errOut, rows)
}

func writeListRow(out io.Writer, host string, row Row) {
	fmt.Fprintf(out, "%s: %s\n", RowName(row), statusSummary(row.Status)) //nolint:errcheck
	if row.Entry.Description != "" {
		fmt.Fprintf(out, "  %s\n", row.Entry.Description) //nolint:errcheck
	}
	if row.Relevant {
		fmt.Fprintf(out, "  Why: %s\n", row.Relevance) //nolint:errcheck
	} else {
		fmt.Fprintf(out, "  Not listed as relevant to %s.\n", host) //nolint:errcheck
	}
}

// WriteListJSON writes rows' cli-install#req:machine-readable-output
// listing document — one JSON object with "host" and one "targets" entry
// per row — to out, and every row's warnings to errOut.
func WriteListJSON(out, errOut io.Writer, host string, rows []Row) error {
	doc := listDocument{Host: host, Targets: make([]listTargetJSON, len(rows))}
	for i, row := range rows {
		doc.Targets[i] = rowToListJSON(row)
	}
	if err := json.NewEncoder(out).Encode(doc); err != nil {
		return err
	}
	writeWarnings(errOut, rows)
	return nil
}
