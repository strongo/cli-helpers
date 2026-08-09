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
package cobracmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/strongo/selfupdate"
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
)

// ErrorMapper translates this package's typed outcomes into the host CLI's
// own error type/exit-code convention. Both methods may return the error
// unchanged (or nil) — the mapper exists for hosts that need to wrap or
// reclassify, not because every host must.
type ErrorMapper interface {
	// Failure maps a non-nil error from Config.Update or Config.Check —
	// ordinarily a *selfupdate.Failure, unwrap-able via
	// selfupdate.KindOf(err) — into the host's own error type.
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
	// terminal, used to implement REQ: non-interactive-refusal. Defaults to
	// checking whether os.Stdin is a character device. Tests should always
	// override this — it is the seam that makes the confirmation-prompt and
	// non-interactive-refusal paths exercisable without a real TTY.
	Interactive func() bool
}

// New builds the "self-update" command for cfg. It registers --check,
// --yes/-y, --version (a pinned release tag, distinct from any root-level
// --version the host itself defines), --allow-downgrade, --dry-run, and —
// when opts.JSONFormat is set — --format.
//
// RunE performs no work itself beyond flag parsing, calling
// selfupdate.Config.Check or .Update, and formatting the result: all
// decision logic lives in the core package, per REQ: no-io-side-effects-in-
// core — this adapter is where the I/O those decisions require (prompting,
// printing, choosing an output format) is allowed to live.
func New(cfg selfupdate.Config, opts CommandOptions) *cobra.Command {
	use := opts.Use
	if use == "" {
		use = "self-update"
	}
	short := opts.Short
	if short == "" {
		short = "Update the installed binary in place"
	}
	interactive := opts.Interactive
	if interactive == nil {
		interactive = defaultIsInteractive
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
			}

			if check, _ := cmd.Flags().GetBool("check"); check {
				return runCheck(cmd, cfg, opts, format)
			}
			return runUpdate(cmd, cfg, opts, interactive, format)
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

// runUpdate handles every non-check invocation: the managed redirect, the
// already-current no-op, a dry run, a declined confirmation, and an actual
// swap.
func runUpdate(cmd *cobra.Command, cfg selfupdate.Config, opts CommandOptions, interactive func() bool, format string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	pinned, _ := cmd.Flags().GetString("version")
	allowDowngrade, _ := cmd.Flags().GetBool("allow-downgrade")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	out := cmd.OutOrStdout()
	confirm := func(transition string) (bool, error) {
		_, _ = fmt.Fprintln(out, transition)
		if yes {
			return true, nil
		}
		if !interactive() {
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("--yes is required for non-interactive use; refusing to replace the binary"),
			}
		}
		_, _ = fmt.Fprint(out, "Proceed? [y/N] ")
		reader := bufio.NewReader(cmd.InOrStdin())
		line, readErr := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			// The terminal check said interactive and the read still returned
			// nothing: stdin is closed or empty, so nobody was ever asked.
			// "Nobody answered" is a refusal to proceed, not a user declining
			// — and it must exit non-zero like every other non-interactive
			// refusal, or a script reads exit 0 and believes an update ran.
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("no answer read from stdin; pass --yes to replace the binary without confirmation"),
			}
		}
		return answer == "y" || answer == "yes", nil
	}

	outcome, err := updateFunc(cfg, cmd.Context(), selfupdate.Options{
		PinnedVersion:  pinned,
		AllowDowngrade: allowDowngrade,
		DryRun:         dryRun,
		Confirm:        confirm,
	})
	if err != nil {
		if selfupdate.KindOf(err) == selfupdate.KindAmbiguous {
			printAmbiguousGuidance(out, cfg)
		}
		return mapFailure(opts, err)
	}

	if format == "json" {
		return writeOutcomeJSON(out, outcome)
	}
	writeOutcomeText(out, cmd.ErrOrStderr(), cfg, outcome)
	return nil
}

// runCheck implements the read-only --check mode: it resolves availability
// and reports it, performing no download or write on any branch.
func runCheck(cmd *cobra.Command, cfg selfupdate.Config, opts CommandOptions, format string) error {
	result, err := checkFunc(cfg, cmd.Context())
	if err != nil {
		return mapFailure(opts, err)
	}

	out := cmd.OutOrStdout()
	if format == "json" {
		if err := json.NewEncoder(out).Encode(checkJSON{
			Current: result.Current,
			Latest:  result.Latest,
			Verdict: result.Verdict.String(),
		}); err != nil {
			return err
		}
	} else {
		writeCheckText(out, cfg, result)
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

// printAmbiguousGuidance prints the manual-update guidance REQ: ambiguous-
// safe-default assigns to the consumer: the core package only reports that
// classification failed, not what the user should do about it.
func printAmbiguousGuidance(out io.Writer, cfg selfupdate.Config) {
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

// defaultIsInteractive reports whether stdin is a real terminal.
//
// The obvious test — os.Stdin.Stat() and a check for os.ModeCharDevice — is
// wrong, and wrong in the direction that matters: /dev/null IS a character
// device, so a command run as `cmd < /dev/null` (how agents, cron and CI
// habitually invoke things) was classified interactive, prompted into the
// void, read EOF, and reported "aborted" with exit 0. A caller reading that
// exit code sees success where nothing happened, which is precisely what
// REQ: non-interactive-refusal exists to prevent. Ask the terminal driver
// instead: an ioctl either answers for a tty or it does not.
func defaultIsInteractive() bool {
	return terminalCheck(int(os.Stdin.Fd()))
}

// terminalCheck is a seam over the tty probe so the interactive and
// non-interactive branches are both exercisable without a pty.
var terminalCheck = term.IsTerminal
