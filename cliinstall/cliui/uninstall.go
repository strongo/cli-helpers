package cliui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/cliinstall"
)

// UninstallRow is one target's combined catalog, relevance, and uninstall result.
type UninstallRow struct {
	Entry     cliinstall.Entry
	Relevant  bool
	Relevance string
	Result    cliinstall.UninstallResult
}

// UninstallRowName returns the target name.
func UninstallRowName(row UninstallRow) string {
	if row.Entry.ID != "" {
		return row.Entry.ID
	}
	return row.Result.Target
}

// WriteUninstallReport writes the uninstall summary or dry-run preview to out, and warnings to errOut.
func WriteUninstallReport(out, errOut io.Writer, hostID string, rows []UninstallRow, dryRun bool) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "No matching installed tools to uninstall.")
		return err
	}

	for _, row := range rows {
		name := UninstallRowName(row)
		r := row.Result
		switch r.Outcome {
		case cliinstall.UninstallOutcomeUninstalled:
			if r.Method == cliinstall.UninstallMethodHomebrew {
				fmt.Fprintf(out, "✓ Uninstalled %s (via brew cask %s)\n", name, row.Entry.CaskToken) //nolint:errcheck
			} else {
				fmt.Fprintf(out, "✓ Uninstalled %s (%s)\n", name, r.Path) //nolint:errcheck
			}
		case cliinstall.UninstallOutcomeDryRun:
			if r.Method == cliinstall.UninstallMethodHomebrew {
				fmt.Fprintf(out, "• Would uninstall %s via %s\n", name, strings.Join(r.CaskArgv, " ")) //nolint:errcheck
			} else {
				fmt.Fprintf(out, "• Would remove %s (%s)\n", name, r.Path) //nolint:errcheck
			}
		case cliinstall.UninstallOutcomeNotInstalled:
			fmt.Fprintf(out, "- %s is not installed\n", name) //nolint:errcheck
		case cliinstall.UninstallOutcomeRedirected:
			fmt.Fprintf(out, "! %s: to uninstall, run: %s\n", name, strings.Join(r.CaskArgv, " ")) //nolint:errcheck
		case cliinstall.UninstallOutcomeFailed:
			if r.Failure != nil {
				fmt.Fprintf(out, "✗ Failed to uninstall %s: %v\n", name, r.Failure.Err) //nolint:errcheck
			} else {
				fmt.Fprintf(out, "✗ Failed to uninstall %s\n", name) //nolint:errcheck
			}
		default:
			fmt.Fprintf(out, "? %s: %s\n", name, r.Outcome.String()) //nolint:errcheck
		}

		for _, w := range r.Warnings {
			fmt.Fprintf(errOut, "warning (%s): %s\n", name, w) //nolint:errcheck
		}
	}
	return nil
}

type uninstallTargetJSON struct {
	Name     string   `json:"name"`
	Outcome  string   `json:"outcome"`
	Method   string   `json:"method,omitempty"`
	Path     string   `json:"path,omitempty"`
	CaskArgv []string `json:"cask_argv,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type uninstallDocumentJSON struct {
	Host    string                `json:"host"`
	Targets []uninstallTargetJSON `json:"targets"`
}

// WriteUninstallJSON writes the uninstall outcome formatted as JSON.
func WriteUninstallJSON(out io.Writer, hostID string, rows []UninstallRow) error {
	doc := uninstallDocumentJSON{
		Host:    hostID,
		Targets: make([]uninstallTargetJSON, 0, len(rows)),
	}
	for _, row := range rows {
		r := row.Result
		target := uninstallTargetJSON{
			Name:     UninstallRowName(row),
			Outcome:  r.Outcome.String(),
			Method:   r.Method.String(),
			Path:     r.Path,
			CaskArgv: r.CaskArgv,
			Warnings: r.Warnings,
		}
		if r.Failure != nil && r.Failure.Err != nil {
			target.Error = r.Failure.Err.Error()
		}
		doc.Targets = append(doc.Targets, target)
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
