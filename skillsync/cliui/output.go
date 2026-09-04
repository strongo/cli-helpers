// Package cliui renders skillsync reports for any command framework.
package cliui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/skillsync"
)

// WriteJSON emits one JSON value and nothing else.
func WriteJSON(out io.Writer, reports []skillsync.Report) error {
	if len(reports) == 1 {
		return json.NewEncoder(out).Encode(reports[0])
	}
	return json.NewEncoder(out).Encode(struct {
		Targets []skillsync.Report `json:"targets"`
	}{reports})
}

// WriteText renders deterministic human output for any caller.
func WriteText(out io.Writer, report skillsync.Report) error {
	if _, err := fmt.Fprintf(out, "skills synced %s\n", report.Dir); err != nil {
		return err
	}
	for _, action := range []skillsync.Action{skillsync.Added, skillsync.Updated, skillsync.Unchanged, skillsync.Removed, skillsync.Conflict} {
		names := report.Names(action)
		if len(names) > 0 {
			if _, err := fmt.Fprintf(out, "  %s: %s\n", action, strings.Join(names, ", ")); err != nil {
				return err
			}
		}
	}
	return nil
}
