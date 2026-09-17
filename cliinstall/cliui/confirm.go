package cliui

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/selfupdate"
	selfcliui "github.com/strongo/cli-helpers/selfupdate/cliui"
)

// ConfirmOptions configures the callback Confirm builds for
// cliinstall.Options.Confirm.
type ConfirmOptions struct {
	// In is read for the user's y/N answer once a prompt is actually shown.
	In io.Reader
	// Out receives the "Install <names>? [y/N] " prompt text when one is
	// shown.
	Out io.Writer
	// Interactive reports whether an interactive terminal is available to
	// ask on. Nil defaults to selfupdate/cliui.IsTerminal — this package
	// reuses that check rather than a second one, since a character device
	// (e.g. /dev/null) must not be misread as an interactive terminal for
	// this confirmation any more than for self-update's own (see that
	// function's own doc comment).
	Interactive func() bool
}

// Confirm builds a cliinstall.Options.Confirm callback
// (cli-install#req:confirmation-gate: "one confirmation covering every
// target that would be installed"). cliinstall.Install calls it at most
// once per batch, only when at least one target still needs confirming and
// Options.Yes is false, so this callback has no --yes concern of its own.
//
// It refuses with a *selfupdate.Failure{Kind: selfupdate.KindNonInteractive}
// when no interactive terminal is available
// (self-update#req:non-interactive-refusal, reused verbatim by
// cli-install#req:confirmation-gate's "Without --yes and without an
// interactive terminal the command MUST refuse... before any download or
// write"), and otherwise reads a y/N answer. An empty read — the terminal
// check said interactive but nothing came back — is treated the same as
// the non-interactive case, for the identical reason
// selfupdate/cliui.Confirm documents: nobody was actually asked, so
// reporting a decline (which callers may read as "nothing failed") would
// let a script believe the refusal never happened.
func Confirm(opts ConfirmOptions) func(names []string) (bool, error) {
	interactive := opts.Interactive
	if interactive == nil {
		interactive = selfcliui.IsTerminal
	}
	return func(names []string) (bool, error) {
		if !interactive() {
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("--yes is required for non-interactive use; refusing to install %s", strings.Join(names, ", ")),
			}
		}
		_, _ = fmt.Fprintf(opts.Out, "Install %s? [y/N] ", strings.Join(names, ", "))
		reader := bufio.NewReader(opts.In)
		line, readErr := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if readErr != nil && answer == "" {
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("no answer read from stdin; pass --yes to install without confirmation"),
			}
		}
		return answer == "y" || answer == "yes", nil
	}
}
