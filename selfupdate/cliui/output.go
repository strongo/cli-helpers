package cliui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/cli-helpers/selfupdate"
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
	Action              string `json:"action"`
	Manager             string `json:"manager,omitempty"`
	Command             string `json:"command,omitempty"`
	Current             string `json:"current,omitempty"`
	Latest              string `json:"latest,omitempty"`
	Target              string `json:"target,omitempty"`
	Downgrade           bool   `json:"downgrade,omitempty"`
	PlannedURL          string `json:"planned_url,omitempty"`
	ReleaseCheckWarning string `json:"release_check_warning,omitempty"`
	Warning             string `json:"warning,omitempty"`
	AfterUpdateWarning  string `json:"after_update_warning,omitempty"`
}

// WriteOutcomeJSON writes outcome's --format json shape to out.
func WriteOutcomeJSON(out io.Writer, outcome selfupdate.Outcome) error {
	oj := outcomeJSON{Action: outcome.Action.String()}
	if m := outcome.Detection.Manager; m != nil {
		oj.Manager = m.Name
		oj.Command = m.UpgradeCommand
	}
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
	if outcome.ReleaseCheckWarning != nil {
		oj.ReleaseCheckWarning = outcome.ReleaseCheckWarning.Error()
	}
	if outcome.PostSwapWarning != nil {
		oj.Warning = outcome.PostSwapWarning.Error()
	}
	if outcome.AfterUpdateWarning != nil {
		oj.AfterUpdateWarning = outcome.AfterUpdateWarning.Error()
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
			fmt.Fprintf(out, "%s is managed by %s. Run: %s\n", cfg.BinaryName, m.Name, m.UpgradeCommand) //nolint:errcheck
		}
	case selfupdate.ActionAlreadyCurrent:
		writeStyled(out, successStyle, fmt.Sprintf("[OK] %s is already up to date (%s).\n", cfg.BinaryName, outcome.Result.Current))
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
		writeStyled(out, successStyle, fmt.Sprintf("[OK] %s updated to %s.\n", cfg.BinaryName, outcome.Target))
	case selfupdate.ActionManagerExecuted:
		if m := outcome.Detection.Manager; m != nil {
			writeStyled(out, successStyle, fmt.Sprintf("[OK] %s upgrade command completed for %s.\n", m.Name, cfg.BinaryName))
		}
	}
	WriteAvailabilityWarning(errOut, outcome)
	if outcome.PostSwapWarning != nil {
		fmt.Fprintf(errOut, "self-update: warning: %v\n", outcome.PostSwapWarning) //nolint:errcheck
	}
	if outcome.AfterUpdateWarning != nil {
		fmt.Fprintf(errOut, "self-update: post-update warning: %v\n", outcome.AfterUpdateWarning) //nolint:errcheck
	}
}

// WriteAvailabilityWarning writes an advisory managed-release lookup warning
// once, separately from the preview and any machine-readable outcome.
func WriteAvailabilityWarning(errOut io.Writer, outcome selfupdate.Outcome) {
	if outcome.ReleaseCheckWarning != nil {
		writeStyled(errOut, warningStyle, fmt.Sprintf("self-update: warning: latest release unavailable: %v\n", outcome.ReleaseCheckWarning))
	}
}

// WriteAvailabilityPreview writes one compact, terminal-aware summary before
// confirmation or a package-manager command. It deliberately uses only ASCII
// border characters so redirected output remains readable in logs and pipes.
func WriteAvailabilityPreview(out io.Writer, cfg selfupdate.Config, availability selfupdate.Availability) {
	writeTerminal(out, renderAvailabilityPreview(cfg, availability))
}

func renderAvailabilityPreview(cfg selfupdate.Config, availability selfupdate.Availability) string {
	title := cfg.BinaryName + " self-update"
	install := "Direct"
	command := ""
	if m := availability.Detection.Manager; m != nil {
		install = m.Name
		command = m.UpgradeCommand
	}

	rows := []previewRow{{label: "Current", value: availability.Result.Current}}
	if availability.Pinned {
		rows = append(rows, previewRow{label: "Target", value: availability.Target, style: previewSuccess})
	} else if availability.Warning != nil {
		rows = append(rows, previewRow{label: "Latest", value: "unavailable", style: previewWarning})
	} else {
		rows = append(rows, previewRow{label: "Latest", value: availability.Result.Latest, style: previewSuccess})
	}
	rows = append(rows, previewRow{label: "Install", value: install})
	if command != "" {
		rows = append(rows, previewRow{label: "Command", value: command})
	}
	width := lipgloss.Width(title)
	for _, row := range rows {
		width = max(width, previewLabelWidth+2+min(lipgloss.Width(row.value), 76-previewLabelWidth-2))
	}
	width = min(max(width, 28), 76)
	border := borderStyle.Render("+" + strings.Repeat("-", width+2) + "+")
	var b strings.Builder
	b.WriteString(border)
	b.WriteByte('\n')
	b.WriteString(renderPreviewLine(titleStyle.Render(title), width))
	b.WriteByte('\n')
	b.WriteString(border)
	b.WriteByte('\n')
	for _, row := range rows {
		b.WriteString(renderPreviewRow(row, width))
		b.WriteByte('\n')
	}
	b.WriteString(border)
	b.WriteByte('\n')
	return b.String()
}

type previewRow struct {
	label string
	value string
	style previewStyle
}

type previewStyle int

const (
	previewPlain previewStyle = iota
	previewSuccess
	previewWarning
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	borderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

func renderPreviewLine(content string, width int) string {
	content = truncatePreviewValue(content, width)
	return borderStyle.Render("| ") + content + strings.Repeat(" ", max(0, width-lipgloss.Width(content))) + borderStyle.Render(" |")
}

func renderPreviewRow(row previewRow, width int) string {
	values := wrapPreviewValue(row.value, width-previewLabelWidth-2)
	var lines []string
	for i, rawValue := range values {
		label := ""
		if i == 0 {
			label = row.label
		}
		prefix := fmt.Sprintf("%-*s: ", previewLabelWidth, label)
		value := rawValue
		switch row.style {
		case previewSuccess:
			value = successStyle.Render(value)
		case previewWarning:
			value = warningStyle.Render(value)
		}
		contentWidth := lipgloss.Width(prefix) + lipgloss.Width(value)
		lines = append(lines, borderStyle.Render("| ")+prefix+value+strings.Repeat(" ", max(0, width-contentWidth))+borderStyle.Render(" |"))
	}
	return strings.Join(lines, "\n")
}

const previewLabelWidth = 7

func wrapPreviewValue(value string, width int) []string {
	return strings.Split(ansi.Wrap(value, width, " "), "\n")
}

func truncatePreviewValue(value string, width int) string {
	return ansi.Truncate(value, width, "...")
}

func writeStyled(out io.Writer, style lipgloss.Style, value string) {
	writeTerminal(out, style.Render(value))
}

func writeTerminal(out io.Writer, value string) {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" || os.Getenv("TERM") == "dumb" {
		_, _ = fmt.Fprint(out, ansi.Strip(value))
		return
	}
	writer := terminalWriter(out)
	_, _ = fmt.Fprint(writer, value)
}

func terminalWriter(out io.Writer) *colorprofile.Writer {
	env := os.Environ()
	writer := colorprofile.NewWriter(out, env)
	// colorprofile's ASCII profile retains text decoration. The preview emits
	// ANSI-styled strings, so use NoTTY for NO_COLOR to guarantee its colors
	// are stripped even when CLICOLOR_FORCE is also present.
	if colorprofile.Env(env) == colorprofile.ASCII {
		writer.Profile = colorprofile.NoTTY
	}
	return writer
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
