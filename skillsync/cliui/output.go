// Package cliui renders skillsync reports for any command framework.
package cliui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/skillsync"
)

// TargetReport is one adapter target outcome. Err remains a real error for
// command handling and is represented as text only in JSON output.
type TargetReport struct {
	Harness string
	Dir     string
	Report  skillsync.Report
	Err     error
}

type targetJSON struct {
	Harness string `json:"harness,omitempty"`
	Dir     string `json:"dir"`
	Error   string `json:"error,omitempty"`
	*skillsync.Report
}

// WriteTargetJSON emits one JSON value and nothing else.
func WriteTargetJSON(out io.Writer, reports []TargetReport) error {
	items := make([]targetJSON, 0, len(reports))
	for _, item := range reports {
		payload := targetJSON{Harness: item.Harness, Dir: item.Dir, Report: &item.Report}
		if item.Err != nil {
			payload.Error = item.Err.Error()
		}
		items = append(items, payload)
	}
	if len(items) == 1 {
		return json.NewEncoder(out).Encode(items[0])
	}
	return json.NewEncoder(out).Encode(struct {
		Targets []targetJSON `json:"targets"`
	}{items})
}

// WriteJSON is retained for callers with core reports but no target identity.
func WriteJSON(out io.Writer, reports []skillsync.Report) error {
	items := make([]TargetReport, 0, len(reports))
	for _, report := range reports {
		items = append(items, TargetReport{Dir: report.Dir, Report: report})
	}
	return WriteTargetJSON(out, items)
}

// WriteTargetText renders compact, deterministic target summaries. It deliberately
// names only changed/conflicted work while counting unchanged skills.
func WriteTargetText(out io.Writer, reports []TargetReport) error {
	for _, item := range reports {
		harness := item.Harness
		if harness == "" {
			harness = "directory"
		}
		verb := "synced"
		if item.Err != nil {
			verb = "failed"
		} else if item.Report.DryRun {
			verb = "preview"
		}
		if _, err := fmt.Fprintf(out, "%s %s: %s\n", harness, verb, item.Dir); err != nil {
			return err
		}
		if item.Err != nil {
			if _, err := fmt.Fprintf(out, "  failed: %v\n", item.Err); err != nil {
				return err
			}
		}
		if err := writeChanges(out, item.Report); err != nil {
			return err
		}
	}
	return nil
}

// WriteText renders one core report through the outcome-aware target renderer.
func WriteText(out io.Writer, report skillsync.Report) error {
	return WriteTargetText(out, []TargetReport{{Dir: report.Dir, Report: report}})
}

func writeChanges(out io.Writer, report skillsync.Report) error {
	for _, group := range []struct {
		label    string
		action   skillsync.Action
		outcomes []skillsync.Outcome
	}{
		{"planned", skillsync.Added, []skillsync.Outcome{skillsync.Planned}},
		{"planned", skillsync.Updated, []skillsync.Outcome{skillsync.Planned}},
		{"planned removal", skillsync.Removed, []skillsync.Outcome{skillsync.Planned}},
		{"added", skillsync.Added, []skillsync.Outcome{skillsync.Applied}},
		{"updated", skillsync.Updated, []skillsync.Outcome{skillsync.Applied}},
		{"removed", skillsync.Removed, []skillsync.Outcome{skillsync.Applied}},
		{"restored", skillsync.Added, []skillsync.Outcome{skillsync.Restored}},
		{"restored", skillsync.Updated, []skillsync.Outcome{skillsync.Restored}},
		{"restored removal", skillsync.Removed, []skillsync.Outcome{skillsync.Restored}},
		{"incomplete", skillsync.Added, []skillsync.Outcome{skillsync.Incomplete}},
		{"incomplete", skillsync.Updated, []skillsync.Outcome{skillsync.Incomplete}},
		{"incomplete removal", skillsync.Removed, []skillsync.Outcome{skillsync.Incomplete}},
		{"conflicts", skillsync.Conflict, nil},
	} {
		names := report.NamesFor(group.action, group.outcomes...)
		if len(names) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(out, "  %s: %s\n", group.label, strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	if unchanged := report.NamesFor(skillsync.Unchanged); len(unchanged) > 0 {
		if _, err := fmt.Fprintf(out, "  unchanged: %d\n", len(unchanged)); err != nil {
			return err
		}
	}
	return nil
}
