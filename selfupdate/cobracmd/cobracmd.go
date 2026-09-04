// Package cobracmd builds a ready-made self-update Cobra command from a
// selfupdate.Config. It is the ONLY place in this module that imports
// Cobra (REQ: cobra-adapter-optional) — the root selfupdate package has no
// command-framework dependency, so a CLI built on something else, or on
// nothing, can call Config.Update and Config.Check directly.
//
// Everything this package prints, and every exit code the host process
// eventually uses, is the host's own decision. The command's RunE never
// calls os.Exit and never picks a code itself (REQ: host-owned-exit-codes):
// it returns nil, or whatever CommandOptions.Errors.Failure or
// .UpdateAvailable produced, and the host's own top-level runner is what
// turns that into a process exit code — which is exactly what lets two
// consumers with incompatible exit-code contracts (one reserving a
// dedicated code for "update available", one folding it into a general
// findings code) both build a working command from this same adapter.
//
// The prompting, refusal, and output-formatting logic RunE actually
// performs lives in the cliui subpackage, which imports neither Cobra nor
// any other command framework — this package is only the Cobra flag/wiring
// layer on top of it, kept as the ONE place that logic is implemented so a
// CLI with no framework at all (see cliui's own doc comment) can reuse it
// directly instead of re-deriving it.
package cobracmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/selfupdate/cliui"
)

// Test seams: overridable indirections over selfupdate.Config's own methods,
// so this package's tests can exercise flag parsing, ErrorMapper dispatch,
// and output formatting — this package's actual responsibility — with a
// stubbed Outcome/CheckResult, independent of DetectSelf resolving the real
// test binary's own path or a live GitHub endpoint. The default
// implementations are the real thing; only tests override these.
var (
	checkFunc = func(cfg selfupdate.Config, ctx context.Context) (selfupdate.CheckResult, error) {
		return cfg.Check(ctx)
	}
	updateFunc = func(cfg selfupdate.Config, ctx context.Context, opts selfupdate.Options) (selfupdate.Outcome, error) {
		return cfg.Update(ctx, opts)
	}
	detectFunc = func(cfg selfupdate.Config) (selfupdate.Detection, error) {
		return cfg.DetectSelf()
	}
)

// UsageError identifies invalid command input before any update work begins.
// Hosts may use errors.As to distinguish it from operational failures.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// ErrorMapper translates this package's typed outcomes into the host CLI's
// own error type/exit-code convention. Both methods may return the error
// unchanged (or nil) — the mapper exists for hosts that need to wrap or
// reclassify, not because every host must.
type ErrorMapper interface {
	// Failure maps a non-nil command error into the host's own error type.
	// Invalid output formats return *UsageError; update and check failures
	// ordinarily return *selfupdate.Failure. Output writer errors are also
	// mapped so hosts can preserve their operational-failure exit codes.
	Failure(err error) error
	// UpdateAvailable is called after a successful --check whose verdict is
	// not UpToDate — that covers both selfupdate.UpdateAvailable and
	// selfupdate.Undetermined, since neither is "up to date" (a consumer
	// that wants a dedicated exit code for "update available" typically
	// wants it for both). Returning nil reports success (exit 0) despite an
	// update being available; returning an error is how a consumer
	// reserves e.g. a dedicated exit code for this case.
	UpdateAvailable(res selfupdate.CheckResult) error
}

// CommandOptions configures the command New builds. Use and Short default to
// "self-update" and a generic short description when left empty.
type CommandOptions struct {
	// Use is the command name. Defaults to "self-update".
	Use string
	// Short is the one-line help text. Defaults to a generic description.
	Short string
	// Aliases are additional names the command responds to, e.g.
	// []string{"update"}.
	Aliases []string
	// Errors maps outcomes to the host's own error type. Required for a
	// host that wants distinguishable exit codes; a nil Errors makes every
	// failure and every "update available" --check both return the
	// underlying error/nil unchanged.
	Errors ErrorMapper
	// JSONFormat registers a --format text|json flag when true. When false,
	// output is always the human-readable text form.
	JSONFormat bool
	// Interactive reports whether the process is attached to an interactive
	// terminal, used to implement REQ: non-interactive-refusal. Passed
	// straight through to cliui.ConfirmOptions.Interactive; nil means that
	// package's own default (cliui.IsTerminal, a term.IsTerminal check on
	// stdin — never an os.ModeCharDevice check, which /dev/null also
	// satisfies). Tests should always override this — it is the seam that
	// makes the confirmation-prompt and non-interactive-refusal paths
	// exercisable without a real TTY.
	Interactive func() bool
	// AfterUpdate runs after a successful update outcome. It is passed through
	// to selfupdate.Options so non-Cobra and Cobra consumers share the same
	// post-update contract. Its errors are warnings on the returned Outcome,
	// never command failures after the binary update completed.
	AfterUpdate selfupdate.AfterUpdateFunc
}

// New builds the "self-update" command for cfg. It registers --check,
// --yes/-y, --version (a pinned release tag, distinct from any root-level
// --version the host itself defines), --allow-downgrade, --dry-run, and —
// when opts.JSONFormat is set — --format.
//
// RunE performs no work itself beyond flag parsing, calling
// selfupdate.Config.Check or .Update, and formatting the result via cliui:
// all decision logic lives in the core package, per REQ: no-io-side-effects-
// in-core — this adapter is where the I/O those decisions require
// (prompting, printing, choosing an output format) is allowed to live.
func New(cfg selfupdate.Config, opts CommandOptions) *cobra.Command {
	use := opts.Use
	if use == "" {
		use = "self-update"
	}
	short := opts.Short
	if short == "" {
		short = "Update the installed binary in place"
	}

	cmd := &cobra.Command{
		Use:     use,
		Aliases: opts.Aliases,
		Short:   short,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format := "text"
			if opts.JSONFormat {
				format, _ = cmd.Flags().GetString("format")
				if format != "text" && format != "json" {
					return mapFailure(opts, &UsageError{Err: fmt.Errorf("invalid --format %q: expected text or json", format)})
				}
			}

			if check, _ := cmd.Flags().GetBool("check"); check {
				return runCheck(cmd, cfg, opts, format)
			}
			return runUpdate(cmd, cfg, opts, format)
		},
	}

	cmd.Flags().Bool("check", false, "report whether a newer release is available without applying it")
	cmd.Flags().BoolP("yes", "y", false, "skip the interactive confirmation prompt")
	cmd.Flags().String("version", "", `install a specific release tag (leading "v" optional) instead of the latest`)
	cmd.Flags().Bool("allow-downgrade", false, "permit installing a --version older than the running build")
	cmd.Flags().Bool("dry-run", false, "report what would happen without downloading or writing anything")
	if opts.JSONFormat {
		cmd.Flags().String("format", "text", "output format: text|json")
	}
	return cmd
}

// runUpdate handles every non-check invocation: a managed redirect or manager
// command, the already-current no-op, a dry run, a declined confirmation, and
// an actual swap. The confirmation callback and every line it, and the outcome, print
// are cliui's — this function is only flag parsing and dispatch.
func runUpdate(cmd *cobra.Command, cfg selfupdate.Config, opts CommandOptions, format string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	pinned, _ := cmd.Flags().GetString("version")
	allowDowngrade, _ := cmd.Flags().GetBool("allow-downgrade")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	out := cmd.OutOrStdout()
	interactionOut := out
	if format == "json" {
		// Keep stdout valid JSON while still streaming prompts and package-
		// manager progress to the caller's stderr.
		interactionOut = cmd.ErrOrStderr()
	}
	confirm := cliui.Confirm(cliui.ConfirmOptions{
		In:          cmd.InOrStdin(),
		Out:         interactionOut,
		Yes:         yes,
		Interactive: opts.Interactive,
	})

	outcome, err := updateFunc(cfg, cmd.Context(), selfupdate.Options{
		PinnedVersion:  pinned,
		AllowDowngrade: allowDowngrade,
		DryRun:         dryRun,
		Confirm:        confirm,
		RunManaged:     cliui.ManagedCommandRunner(cmd.InOrStdin(), interactionOut, cmd.ErrOrStderr()),
		VerifyManaged:  cliui.VerifyManagedBinary,
		AfterUpdate:    opts.AfterUpdate,
	})
	if err != nil {
		if selfupdate.KindOf(err) == selfupdate.KindAmbiguous {
			cliui.WriteAmbiguousGuidance(out, cfg)
		}
		return mapFailure(opts, err)
	}

	if format == "json" {
		if err := cliui.WriteOutcomeJSON(out, outcome); err != nil {
			return mapFailure(opts, err)
		}
		return nil
	}
	cliui.WriteOutcome(out, cmd.ErrOrStderr(), cfg, outcome)
	return nil
}

// runCheck implements the read-only --check mode: it resolves availability
// and reports it, performing no download or write on any branch.
//
// It also classifies the install, because "an update exists" is only half an
// answer — what the user does next differs entirely between a Homebrew
// install (run brew) and a manual one (run this command), and a check that
// withholds that forces them to run the real thing to find out. Detection is
// a pure path classification: it reads no network and writes nothing, so it
// costs the read-only guarantee nothing. A detection failure is deliberately
// not fatal here; the version comparison is still worth reporting on its own.
func runCheck(cmd *cobra.Command, cfg selfupdate.Config, opts CommandOptions, format string) error {
	result, err := checkFunc(cfg, cmd.Context())
	if err != nil {
		return mapFailure(opts, err)
	}
	detection, detectErr := detectFunc(cfg)
	if detectErr != nil {
		detection = selfupdate.Detection{Method: selfupdate.Ambiguous}
	}

	out := cmd.OutOrStdout()
	if format == "json" {
		if err := cliui.WriteCheckJSON(out, cfg, result, detection); err != nil {
			return mapFailure(opts, err)
		}
	} else {
		cliui.WriteCheck(out, cfg, result)
		if result.Verdict != selfupdate.UpToDate {
			cliui.WriteNextStep(out, cfg, detection, cmd.CommandPath())
		}
	}

	if result.Verdict != selfupdate.UpToDate && opts.Errors != nil {
		return opts.Errors.UpdateAvailable(result)
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
