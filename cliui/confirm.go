// Package cliui holds the framework-neutral parts of a self-update CLI's
// user interaction: the confirmation prompt (REQ: non-interactive-refusal),
// the terminal check it relies on, and the text/JSON writers for
// selfupdate.Outcome and selfupdate.CheckResult. It imports neither Cobra
// nor any other command framework — a hand-rolled CLI with no framework at
// all can build its own command straight from selfupdate.Config.Check/
// .Update plus this package, and get the exact same prompting, formatting,
// and refusal behavior the cobracmd subpackage gives a Cobra-based CLI,
// including this package's own already-fixed bugs: a character device is
// not a terminal (see IsTerminal), and an empty read is a refusal, not a
// decline (see Confirm).
//
// cobracmd is the Cobra-specific flag/wiring layer built on top of this
// package; this package has no dependency on it, or on Cobra, at all.
package cliui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/strongo/selfupdate"
)

// IsTerminal reports whether stdin is a real terminal.
//
// The obvious test — os.Stdin.Stat() and a check for os.ModeCharDevice — is
// wrong, and wrong in the direction that matters: /dev/null IS a character
// device, so a command run as `cmd < /dev/null` (how agents, cron and CI
// habitually invoke things) was classified interactive, prompted into the
// void, read EOF, and reported "aborted" with exit 0. A caller reading that
// exit code sees success where nothing happened, which is precisely what
// REQ: non-interactive-refusal exists to prevent. Ask the terminal driver
// instead: an ioctl either answers for a tty or it does not.
func IsTerminal() bool {
	return terminalCheck(int(os.Stdin.Fd()))
}

// terminalCheck is a seam over the tty probe so the interactive and
// non-interactive branches are both exercisable without a pty.
var terminalCheck = term.IsTerminal

// ConfirmOptions configures the callback Confirm builds.
type ConfirmOptions struct {
	// In is read for the user's y/N answer once a prompt is actually shown.
	In io.Reader
	// Out receives the transition line always, and the "Proceed? [y/N] "
	// prompt text when one is shown.
	Out io.Writer
	// Yes skips the prompt and proceeds immediately without consulting
	// Interactive at all — wire this from a --yes/-y style flag.
	Yes bool
	// Interactive reports whether an interactive terminal is available to
	// ask on. Nil defaults to IsTerminal.
	Interactive func() bool
}

// Confirm builds a selfupdate.Options.Confirm callback: it prints the
// transition, honours a --yes-style skip via ConfirmOptions.Yes, refuses
// with a *selfupdate.Failure{Kind: selfupdate.KindNonInteractive} when no
// interactive terminal is available (REQ: non-interactive-refusal), and
// otherwise reads a y/N answer from ConfirmOptions.In.
//
// An empty read — the terminal check said interactive but nothing came
// back, e.g. stdin is closed or redirected from an empty pipe — is treated
// the same as the non-interactive case: a refusal, not the user declining.
// Nobody was actually asked, so reporting success (as a plain decline
// would, via ActionAborted with a nil error) would let a script read exit 0
// and believe an update ran. A decline the user actually typed ("n", or
// anything but y/yes) is reported as (false, nil), which
// selfupdate.Config.Update turns into ActionAborted, not a failure.
func Confirm(opts ConfirmOptions) func(transition string) (bool, error) {
	interactive := opts.Interactive
	if interactive == nil {
		interactive = IsTerminal
	}
	return func(transition string) (bool, error) {
		_, _ = fmt.Fprintln(opts.Out, transition)
		if opts.Yes {
			return true, nil
		}
		if !interactive() {
			return false, &selfupdate.Failure{
				Kind: selfupdate.KindNonInteractive,
				Err:  fmt.Errorf("--yes is required for non-interactive use; refusing to replace the binary"),
			}
		}
		_, _ = fmt.Fprint(opts.Out, "Proceed? [y/N] ")
		reader := bufio.NewReader(opts.In)
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
}
