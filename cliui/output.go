package cliui

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/strongo/selfupdate"
)

// checkJSON is the --format json shape for a Check/--check result.
// InstallMethod and the manager fields are what let a machine caller decide
// the next step the same way the text output states it — without them the
// caller learns an update exists but not whether this install may be
// replaced at all.
type checkJSON struct {
	Current                 string `json:"current"`
	Latest                  string `json:"latest"`
	Verdict                 string `json:"verdict"`
	InstallMethod           string `json:"install_method,omitempty"`
	Manager                 string `json:"manager,omitempty"`
	UpgradeCommand          string `json:"upgrade_command,omitempty"`
	ManagedUpdateExecutable bool   `json:"managed_update_executable,omitempty"`
}

// outcomeJSON is the --format json shape for an Update outcome. Fields are
// omitted when not meaningful for the reported Action, matching the same
// "only meaningful for some actions" contract documented on
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

// WriteOutcomeJSON writes outcome's --format json shape to out.
func WriteOutcomeJSON(out io.Writer, outcome selfupdate.Outcome) error {
	oj := outcomeJSON{Action: outcome.Action.String()}
	if m := outcome.Detection.Manager; m != nil {
		oj.Manager = m.Name
		oj.Command = m.UpgradeCommand
	}
	switch outcome.Action {
	case selfupdate.ActionRedirected:
	case selfupdate.ActionAlreadyCurrent:
		oj.Current = outcome.Result.Current
	case selfupdate.ActionUpdated, selfupdate.ActionAborted, selfupdate.ActionPlanned, selfupdate.ActionManagerExecuted:
		oj.Current = outcome.Result.Current
		oj.Latest = outcome.Result.Latest
		oj.Target = outcome.Target
		oj.Downgrade = outcome.Downgrade
		if outcome.Action == selfupdate.ActionPlanned {
			oj.PlannedURL = outcome.PlannedURL
			if outcome.PlannedCommand != "" {
				oj.Command = outcome.PlannedCommand
			}
		}
		if outcome.PostSwapWarning != nil {
			oj.Warning = outcome.PostSwapWarning.Error()
		}
	}
	return json.NewEncoder(out).Encode(oj)
}

// WriteOutcome writes outcome's human-readable text form to out, and a
// post-swap version-probe warning (REQ: post-swap-version-check), when one
// is present, to errOut.
func WriteOutcome(out, errOut io.Writer, cfg selfupdate.Config, outcome selfupdate.Outcome) {
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
		if outcome.PlannedCommand != "" {
			manager := "package manager"
			if outcome.Detection.Manager != nil {
				manager = outcome.Detection.Manager.Name
			}
			fmt.Fprintf(out, "dry run: would run via %s:\n\n    %s\n", manager, outcome.PlannedCommand) //nolint:errcheck
			break
		}
		verb := "update"
		if outcome.Downgrade {
			verb = "downgrade"
		}
		fmt.Fprintf(out, "dry run: would %s %s from %s to %s\n  asset: %s\n", //nolint:errcheck
			verb, cfg.BinaryName, outcome.Result.Current, outcome.Target, outcome.PlannedURL)
	case selfupdate.ActionUpdated:
		fmt.Fprintf(out, "%s updated to %s.\n", cfg.BinaryName, outcome.Target) //nolint:errcheck
	case selfupdate.ActionManagerExecuted:
		if m := outcome.Detection.Manager; m != nil {
			fmt.Fprintf(out, "%s upgrade completed for %s.\n", m.Name, cfg.BinaryName) //nolint:errcheck
		}
	}
	if outcome.PostSwapWarning != nil {
		fmt.Fprintf(errOut, "self-update: warning: %v\n", outcome.PostSwapWarning) //nolint:errcheck
	}
}

// WriteCheck writes result's human-readable text form to out.
func WriteCheck(out io.Writer, cfg selfupdate.Config, result selfupdate.CheckResult) {
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

// WriteCheckJSON writes result's --format json shape (checkJSON) to out,
// including detection's install-method classification so a machine caller
// can decide the next step the same way WriteNextStep states it in text.
// cfg is accepted for signature symmetry with WriteCheck/WriteOutcome — the
// JSON shape itself carries no field derived from it today.
func WriteCheckJSON(out io.Writer, cfg selfupdate.Config, result selfupdate.CheckResult, detection selfupdate.Detection) error {
	_ = cfg
	payload := checkJSON{
		Current:       result.Current,
		Latest:        result.Latest,
		Verdict:       result.Verdict.String(),
		InstallMethod: detection.Method.String(),
	}
	if m := detection.Manager; m != nil {
		payload.Manager = m.Name
		payload.UpgradeCommand = m.UpgradeCommand
		payload.ManagedUpdateExecutable = m.CanExecuteUpgrade()
	}
	return json.NewEncoder(out).Encode(payload)
}

// WriteNextStep states what to actually do about an available update, which
// depends entirely on how the binary was installed: an executable managed
// install can run this command, a redirect-only managed install must run the
// manager command directly, a manual one can run this command, and an ambiguous
// one gets the same refusal guidance the update path would print.
// commandPath is the fully-qualified invocation (e.g. "wb self-update") so
// the instruction is copy-pasteable in whatever CLI embeds this command.
func WriteNextStep(out io.Writer, cfg selfupdate.Config, detection selfupdate.Detection, commandPath string) {
	switch detection.Method {
	case selfupdate.Managed:
		if m := detection.Manager; m != nil {
			if m.CanExecuteUpgrade() {
				fmt.Fprintf(out, "To upgrade through %s, run: %s\n", m.Name, commandPath) //nolint:errcheck
				return
			}
			fmt.Fprintf(out, "%s was installed via %s. Run the following to upgrade:\n\n    %s\n", //nolint:errcheck
				cfg.BinaryName, m.Name, m.UpgradeCommand)
			return
		}
		WriteAmbiguousGuidance(out, cfg)
	case selfupdate.Manual:
		fmt.Fprintf(out, "To upgrade, run: %s\n", commandPath) //nolint:errcheck
	default:
		WriteAmbiguousGuidance(out, cfg)
	}
}

// WriteAmbiguousGuidance prints the manual-update guidance REQ: ambiguous-
// safe-default leaves to the consumer: the core selfupdate package only
// reports that classification failed, not what the user should do about it.
func WriteAmbiguousGuidance(out io.Writer, cfg selfupdate.Config) {
	fmt.Fprintf(out, //nolint:errcheck
		"%s could not determine how this binary was installed, so the install method is ambiguous.\n"+
			"To avoid replacing a binary that may be managed by a package manager, self-update will not modify it.\n\n"+
			"To update manually, re-download the latest release from https://github.com/%s/releases",
		cfg.BinaryName, cfg.Repository)
	for _, m := range cfg.Managers {
		fmt.Fprintf(out, ", or run: %s", m.UpgradeCommand) //nolint:errcheck
	}
	fmt.Fprintln(out, ".") //nolint:errcheck
}
