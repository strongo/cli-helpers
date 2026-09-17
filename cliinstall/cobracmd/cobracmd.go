// Package cobracmd builds a ready-made "install" Cobra command from a
// cliinstall catalog host id. It is the ONLY place in the cli-install
// Feature that imports Cobra (cli-install#req:core-framework-neutral) — the
// root cliinstall package has no command-framework dependency, so a CLI
// built on something else, or on nothing, can call cliinstall.Probe and
// cliinstall.Install directly.
//
// Everything this package prints, and every exit code the host process
// eventually uses, is the host's own decision. The command's RunE never
// calls os.Exit and never picks a code itself
// (cli-install#req:host-owned-exit-codes): it returns nil, or whatever
// CommandOptions.Errors.Failure produced, and the host's own top-level
// runner is what turns that into a process exit code — exactly the
// contract selfupdate/cobracmd already establishes for self-update, and
// this package mirrors it deliberately: two hosts with incompatible
// exit-code contracts both build a working "install" command from this
// same adapter.
//
// The formatting and confirmation logic RunE performs lives in the cliui
// subpackage, which imports neither Cobra nor any other command framework —
// this package is only the Cobra flag/wiring layer on top of it, kept as
// the ONE place that logic is implemented so a CLI with no framework at all
// can reuse it directly instead of re-deriving it.
package cobracmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cliui"
	"github.com/strongo/cli-helpers/selfupdate"
	selfcliui "github.com/strongo/cli-helpers/selfupdate/cliui"
)

// UsageError identifies invalid command input before any status probe,
// confirmation, network request or write begins: an invalid --format
// value, or --all combined with target names
// (cli-install#req:host-owned-exit-codes: "pass usage errors (bad flag
// value, --all with names) through a distinguishable usage error type").
// Hosts may use errors.As to distinguish it from a *selfupdate.Failure.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// ErrorMapper translates this package's typed outcomes into the host CLI's
// own error type/exit-code convention, mirroring
// selfupdate/cobracmd.ErrorMapper's own shape and reasoning exactly: every
// host MUST map cli-install's three new failure kinds
// (selfupdate.KindUnknownTarget, KindNoInstallDir, KindDestinationExists)
// explicitly, never through a self-update default branch
// (cli-install#req:host-owned-exit-codes).
type ErrorMapper interface {
	// Failure maps a non-nil command error into the host's own error type:
	// a *UsageError, the confirmation gate's own batch-level
	// *selfupdate.Failure (no interactive terminal and --yes not given), or
	// one target's typed *selfupdate.Failure from BatchResult.Results.
	Failure(err error) error
}

// CommandOptions configures the command New builds. Use and Short default
// to "install [name...]" and a generic short description when left empty.
type CommandOptions struct {
	// Use is the command name, including any argument hint shown in help
	// text. Defaults to "install [name...]".
	Use string
	// Short is the one-line help text. Defaults to a generic description.
	Short string
	// Aliases are additional names the command responds to.
	Aliases []string
	// HostID is the running host's own catalog id
	// (cli-install#req:host-identity-from-catalog). New panics if it is not
	// a valid catalog id — a programming error the host's own tests must
	// catch, never a runtime state a user sees, per that REQ.
	HostID string
	// Errors maps outcomes to the host's own error type. Required for a
	// host that wants distinguishable exit codes; a nil Errors makes every
	// failure return the underlying error unchanged.
	Errors ErrorMapper
	// Interactive reports whether the process is attached to an interactive
	// terminal, used to implement REQ: non-interactive-refusal for the
	// batch confirmation prompt. Passed straight through to
	// cliui.ConfirmOptions.Interactive; nil means that package's own
	// default. Tests should always override this.
	Interactive func() bool
	// HomebrewPrintOnly reports a Homebrew install's command instead of
	// running it, passed straight through to cliinstall.Options
	// (cli-install#req:homebrew-cask-install).
	HomebrewPrintOnly bool
	// ConfigureRelease optionally overrides a target's resolved
	// selfupdate.Config before it is used to resolve or install that
	// target's release — the release-endpoint injection point
	// cli-install#req:no-network-in-tests requires. Nil keeps the
	// catalog's own defaults (the real GitHub API).
	ConfigureRelease func(target cliinstall.Entry, cfg selfupdate.Config) selfupdate.Config
	// Env carries every side-effecting dependency Probe and Install use.
	// Left unset (a zero cliinstall.InstallEnv), New uses
	// cliinstall.DefaultInstallEnv() — the real host. Tests always set
	// this, exactly as cli-install#req:no-network-in-tests requires.
	Env cliinstall.InstallEnv
	// ProbeOptions tunes status-probe concurrency and per-target time
	// budget; the zero value is production-correct (see
	// cliinstall.ProbeOptions).
	ProbeOptions cliinstall.ProbeOptions
}

// New builds the "install" command for opts.HostID. It registers --all,
// --yes/-y, --dry-run, --dir and --format text|json.
//
// With no target names, RunE lists the host's relevant targets (--all lists
// every other catalog entry) and returns — a pure status probe
// (cli-install#req:list-offline-read-only). With one or more names, RunE
// first prints, for each target, its description, details, homepage,
// relevance and planned action — a dry run pass, walked before any
// confirmation regardless of whether --dry-run itself was given
// (cli-install#req:details-before-install) — then, unless --dry-run, asks
// one confirmation covering every target that would actually be installed
// and installs them (cli-install#req:confirmation-gate,
// cli-install#req:multi-target-batch). In --format json, the pre-
// confirmation preview is skipped and interactive prompts move to stderr,
// so stdout carries exactly one JSON document
// (cli-install#req:machine-readable-output).
func New(opts CommandOptions) *cobra.Command {
	if _, ok := cliinstall.ByID(opts.HostID); !ok {
		panic("cliinstall/cobracmd: host id " + opts.HostID + " is not in the compiled catalog")
	}

	use := opts.Use
	if use == "" {
		use = "install [name...]"
	}
	short := opts.Short
	if short == "" {
		short = "List and install fleet CLIs relevant to this one"
	}

	cmd := &cobra.Command{
		Use:     use,
		Aliases: opts.Aliases,
		Short:   short,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, _ := cmd.Flags().GetString("format")
			if format != "text" && format != "json" {
				return mapFailure(opts, &UsageError{Err: fmt.Errorf("invalid --format %q: expected text or json", format)})
			}
			all, _ := cmd.Flags().GetBool("all")
			if all && len(args) > 0 {
				return mapFailure(opts, &UsageError{Err: fmt.Errorf("--all takes no target names")})
			}
			dir, _ := cmd.Flags().GetString("dir")

			if len(args) == 0 {
				return runList(cmd, opts, all, dir, format)
			}

			yes, _ := cmd.Flags().GetBool("yes")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			return runInstall(cmd, opts, args, dir, yes, dryRun, format)
		},
	}

	cmd.Flags().Bool("all", false, "list every catalog entry, not just those relevant to this host")
	cmd.Flags().BoolP("yes", "y", false, "skip the interactive confirmation prompt")
	cmd.Flags().Bool("dry-run", false, "report what would happen without downloading, writing, or running brew")
	cmd.Flags().String("dir", "", "install into this directory instead of the default destination")
	cmd.Flags().String("format", "text", "output format: text|json")
	return cmd
}

// resolveEnv returns opts.Env when the host configured one (tests always
// do), otherwise the real cliinstall.DefaultInstallEnv() — the same
// "required, DefaultEnv unless a caller supplies one" contract
// cliinstall.Env itself documents.
func resolveEnv(opts CommandOptions) cliinstall.InstallEnv {
	if opts.Env.PathDirs == nil {
		return cliinstall.DefaultInstallEnv()
	}
	return opts.Env
}

// relevanceIndex maps hostID's relevant target ids to their relevance text
// (cli-install#req:relevance-matrix).
func relevanceIndex(hostID string) map[string]string {
	idx := make(map[string]string)
	for _, r := range cliinstall.Relevant(hostID) {
		idx[r.Target] = r.Text
	}
	return idx
}

// listRows builds the ordered rows a bare listing shows: the host's
// relevant targets in matrix order (cli-install#req:list-relevant), plus,
// when all is set, every other catalog entry appended alphabetically
// (cli-install#req:list-all). Status is probed once, offline
// (cli-install#req:list-offline-read-only).
func listRows(ctx context.Context, hostID string, all bool, dir string, env cliinstall.Env, probeOpts cliinstall.ProbeOptions) []cliui.Row {
	idx := relevanceIndex(hostID)

	order := make([]string, 0, len(idx))
	for _, r := range cliinstall.Relevant(hostID) {
		order = append(order, r.Target)
	}
	if all {
		for _, e := range cliinstall.Entries() {
			if e.ID == hostID {
				continue
			}
			if _, ok := idx[e.ID]; ok {
				continue
			}
			order = append(order, e.ID)
		}
	}

	entries := make([]cliinstall.Entry, len(order))
	for i, id := range order {
		entries[i], _ = cliinstall.ByID(id)
	}
	statuses := cliinstall.Probe(ctx, entries, dir, env, probeOpts)

	rows := make([]cliui.Row, len(entries))
	for i, e := range entries {
		text, relevant := idx[e.ID]
		rows[i] = cliui.Row{Entry: e, Relevant: relevant, Relevance: text, Status: statuses[i]}
	}
	return rows
}

// resultRows builds one cliui.Row per BatchResult.Results entry, adding
// each target's catalog entry (zero for an unknown name) and relevance so
// cliui's writers can render description/details/relevance alongside the
// plan or outcome.
func resultRows(hostID string, results []cliinstall.Result) []cliui.Row {
	idx := relevanceIndex(hostID)
	rows := make([]cliui.Row, len(results))
	for i := range results {
		res := results[i]
		e, _ := cliinstall.ByID(res.Target)
		text, relevant := idx[res.Target]
		rows[i] = cliui.Row{Entry: e, Relevant: relevant, Relevance: text, Status: res.Status, Plan: &results[i]}
	}
	return rows
}

func runList(cmd *cobra.Command, opts CommandOptions, all bool, dir, format string) error {
	env := resolveEnv(opts)
	rows := listRows(cmd.Context(), opts.HostID, all, dir, env.Env, opts.ProbeOptions)

	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	if format == "json" {
		if err := cliui.WriteListJSON(out, errOut, opts.HostID, rows); err != nil {
			return mapFailure(opts, err)
		}
		return nil
	}
	cliui.WriteList(out, errOut, opts.HostID, rows)
	return nil
}

// runInstall implements `install <name>...`: a pre-confirmation preview
// (text format only, and only when --dry-run itself was not given, since a
// real --dry-run run's own output already is that preview), then the batch
// install itself, then the final report — installed, already installed,
// redirected, declined, dry run, or failed, one per target
// (cli-install#req:multi-target-batch).
func runInstall(cmd *cobra.Command, opts CommandOptions, names []string, dir string, yes, dryRun bool, format string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	interactionOut := out
	if format == "json" {
		// Keep stdout valid JSON while still streaming the confirmation
		// prompt and any Homebrew command's own output to stderr.
		interactionOut = errOut
	}

	env := resolveEnv(opts)
	env.RunManaged = selfcliui.ManagedCommandRunner(cmd.InOrStdin(), interactionOut, errOut)

	core := cliinstall.Options{
		HostID:            opts.HostID,
		Dir:               dir,
		Yes:               yes,
		DryRun:            dryRun,
		HomebrewPrintOnly: opts.HomebrewPrintOnly,
		Env:               env,
		ConfigureRelease:  opts.ConfigureRelease,
		ProbeOptions:      opts.ProbeOptions,
		Confirm: cliui.Confirm(cliui.ConfirmOptions{
			In:          cmd.InOrStdin(),
			Out:         interactionOut,
			Interactive: opts.Interactive,
		}),
	}

	if !dryRun && format == "text" {
		writePreview(cmd, opts.HostID, names, core, out, errOut)
	}

	result, err := cliinstall.Install(cmd.Context(), names, core)
	if err != nil {
		return mapFailure(opts, err)
	}

	rows := resultRows(opts.HostID, result.Results)
	if format == "json" {
		if err := cliui.WriteResultJSON(out, errOut, opts.HostID, rows); err != nil {
			return mapFailure(opts, err)
		}
	} else {
		cliui.WriteResult(out, errOut, opts.HostID, rows)
	}

	return failureFromResult(opts, result)
}

// writePreview walks the full decision path with a DryRun pass — never
// asking for confirmation, downloading, or writing anything
// (cli-install#req:install-dry-run) — purely to print
// cli-install#req:details-before-install's required preview ahead of the
// real pass's own confirmation prompt. names is always non-empty here (the
// caller only reaches runInstall, and so writePreview, with one or more
// target names) and core.HostID is always a valid catalog id (New panics
// otherwise), so — unlike the real pass right after it, which does ask a
// confirmation that can refuse — cliinstall.Install can never return a
// batch-level error for a DryRun call: every target either resolves
// (possibly to OutcomeFailed) or is skipped, and a DryRun target is never
// added to Install's own pending-confirmation list at all.
func writePreview(cmd *cobra.Command, hostID string, names []string, core cliinstall.Options, out, errOut io.Writer) {
	preview := core
	preview.DryRun = true
	previewResult, _ := cliinstall.Install(cmd.Context(), names, preview)
	rows := resultRows(hostID, previewResult.Results)
	cliui.WriteResult(out, errOut, hostID, rows)
}

// failureFromResult maps the first OutcomeFailed target's typed Failure
// through opts.Errors, so the process exit code reflects it
// (cli-install#req:multi-target-batch: "the command fails when at least
// one target failed"). Every target's own outcome was already reported in
// full by the caller before this runs.
func failureFromResult(opts CommandOptions, result cliinstall.BatchResult) error {
	for _, r := range result.Results {
		if r.Outcome == cliinstall.OutcomeFailed {
			return mapFailure(opts, r.Failure)
		}
	}
	return nil
}

// mapFailure routes a non-nil error through opts.Errors when configured,
// otherwise returns it unchanged — either way, this package never picks the
// resulting exit code itself.
func mapFailure(opts CommandOptions, err error) error {
	if opts.Errors != nil {
		return opts.Errors.Failure(err)
	}
	return err
}
