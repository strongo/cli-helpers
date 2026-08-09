package cobracmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/strongo/selfupdate"
)

// checkJSON is the --format json shape for --check.
type checkJSON struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Verdict string `json:"verdict"`
}

// outcomeJSON is the --format json shape for a non-check invocation. Fields
// are omitted when not meaningful for the reported Action, matching the
// same "only meaningful for some actions" contract documented on
// selfupdate.Outcome.
type outcomeJSON struct {
	Action     string `json:"action"`
	Manager    string `json:"manager,omitempty"`
	Command    string `json:"command,omitempty"`
	Current    string `json:"current,omitempty"`
	Latest     string `json:"latest,omitempty"`
	Target     string `json:"target,omitempty"`
	Downgrade  bool   `json:"downgrade,omitempty"`
	PlannedURL string `json:"planned_url,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

func writeOutcomeJSON(out io.Writer, outcome selfupdate.Outcome) error {
	oj := outcomeJSON{Action: outcome.Action.String()}
	switch outcome.Action {
	case selfupdate.ActionRedirected:
		if outcome.Detection.Manager != nil {
			oj.Manager = outcome.Detection.Manager.Name
			oj.Command = outcome.Detection.Manager.UpgradeCommand
		}
	case selfupdate.ActionAlreadyCurrent:
		oj.Current = outcome.Result.Current
	case selfupdate.ActionUpdated, selfupdate.ActionAborted, selfupdate.ActionPlanned:
		oj.Current = outcome.Result.Current
		oj.Latest = outcome.Result.Latest
		oj.Target = outcome.Target
		oj.Downgrade = outcome.Downgrade
		if outcome.Action == selfupdate.ActionPlanned {
			oj.PlannedURL = outcome.PlannedURL
		}
		if outcome.PostSwapWarning != nil {
			oj.Warning = outcome.PostSwapWarning.Error()
		}
	}
	return json.NewEncoder(out).Encode(oj)
}

func writeOutcomeText(out, errOut io.Writer, cfg selfupdate.Config, outcome selfupdate.Outcome) {
	switch outcome.Action {
	case selfupdate.ActionRedirected:
		if m := outcome.Detection.Manager; m != nil {
			fmt.Fprintf(out, "%s was installed via %s. Run the following to upgrade:\n\n    %s\n", //nolint:errcheck
				cfg.BinaryName, m.Name, m.UpgradeCommand)
		}
	case selfupdate.ActionAlreadyCurrent:
		fmt.Fprintf(out, "%s is already up to date (%s).\n", cfg.BinaryName, outcome.Result.Current) //nolint:errcheck
	case selfupdate.ActionAborted:
		fmt.Fprintln(out, "self-update: aborted; binary left unchanged.") //nolint:errcheck
	case selfupdate.ActionPlanned:
		verb := "update"
		if outcome.Downgrade {
			verb = "downgrade"
		}
		fmt.Fprintf(out, "dry run: would %s %s from %s to %s\n  asset: %s\n", //nolint:errcheck
			verb, cfg.BinaryName, outcome.Result.Current, outcome.Target, outcome.PlannedURL)
	case selfupdate.ActionUpdated:
		fmt.Fprintf(out, "%s updated to %s.\n", cfg.BinaryName, outcome.Target) //nolint:errcheck
		if outcome.PostSwapWarning != nil {
			fmt.Fprintf(errOut, "self-update: warning: %v\n", outcome.PostSwapWarning) //nolint:errcheck
		}
	}
}

func writeCheckText(out io.Writer, cfg selfupdate.Config, result selfupdate.CheckResult) {
	switch result.Verdict {
	case selfupdate.UpToDate:
		fmt.Fprintf(out, "%s is up to date (%s).\n", cfg.BinaryName, result.Current) //nolint:errcheck
	case selfupdate.Undetermined:
		fmt.Fprintf(out, "current %s version is undetermined (%s); latest stable is %s.\n", //nolint:errcheck
			cfg.BinaryName, result.Current, result.Latest)
	default:
		fmt.Fprintf(out, "update available: %s → %s\n", result.Current, result.Latest) //nolint:errcheck
	}
}
