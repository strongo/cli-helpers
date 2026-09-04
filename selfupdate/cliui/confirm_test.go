package cliui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

// --- Confirm ---

func TestConfirm_YesSkipsPromptAndInteractiveCheck(t *testing.T) {
	var out bytes.Buffer
	interactiveCalled := false
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &out,
		Yes:         true,
		Interactive: func() bool { interactiveCalled = true; return false },
	})

	proceed, err := confirm("1.0.0 → 1.1.0")
	if err != nil || !proceed {
		t.Fatalf("Confirm with Yes = (%v, %v), want (true, nil)", proceed, err)
	}
	if interactiveCalled {
		t.Error("Interactive() was called despite Yes; must be skipped")
	}
	if !strings.Contains(out.String(), "1.0.0 → 1.1.0") {
		t.Errorf("transition not printed: %q", out.String())
	}
}

// REQ: non-interactive-refusal.
func TestConfirm_NonInteractiveRefusal(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return false },
	})

	proceed, err := confirm("1.0.0 → 1.1.0")
	if proceed {
		t.Error("Confirm proceeded without an interactive terminal")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
}

func TestConfirm_InteractivePromptReadsStdin(t *testing.T) {
	var out bytes.Buffer
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader("y\n"),
		Out:         &out,
		Interactive: func() bool { return true },
	})

	proceed, err := confirm("1.0.0 → 1.1.0")
	if err != nil || !proceed {
		t.Fatalf("Confirm with 'y' = (%v, %v), want (true, nil)", proceed, err)
	}
	if !strings.Contains(out.String(), "Proceed?") {
		t.Errorf("stdout %q does not contain a confirmation prompt", out.String())
	}
}

func TestConfirm_YesWordAlsoProceeds(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader("yes\n"),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})

	proceed, err := confirm("1.0.0 → 1.1.0")
	if err != nil || !proceed {
		t.Fatalf("Confirm with 'yes' = (%v, %v), want (true, nil)", proceed, err)
	}
}

// A user who is asked and answers "n" is a different outcome from a refusal:
// (false, nil), which selfupdate.Config.Update reports as ActionAborted, and
// a host exits 0 on.
func TestConfirm_ExplicitNoIsDeclineNotFailure(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader("n\n"),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})

	proceed, err := confirm("1.0.0 → 1.1.0")
	if proceed || err != nil {
		t.Errorf("Confirm with 'n' = (%v, %v), want (false, nil)", proceed, err)
	}
}

// When the terminal probe says interactive but the read yields nothing,
// nobody was actually asked. That is a non-interactive refusal — non-zero
// exit — and never a user declining, which would exit 0.
func TestConfirm_EmptyStdinRefusesInsteadOfAborting(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})

	proceed, err := confirm("1.0.0 → 1.1.0")
	if proceed {
		t.Error("Confirm proceeded on empty stdin")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("Confirm on empty stdin returned %v (kind %v), want a KindNonInteractive failure",
			err, selfupdate.KindOf(err))
	}
}

// A nil ConfirmOptions.Interactive defaults to IsTerminal.
func TestConfirm_NilInteractiveDefaultsToIsTerminal(t *testing.T) {
	orig := terminalCheck
	t.Cleanup(func() { terminalCheck = orig })
	terminalCheck = func(int) bool { return false }

	confirm := Confirm(ConfirmOptions{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	proceed, err := confirm("1.0.0 → 1.1.0")
	if proceed {
		t.Error("Confirm proceeded with a nil Interactive and a non-terminal stdin")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
}

// --- IsTerminal ---

func TestIsTerminal_Runs(t *testing.T) {
	_ = IsTerminal()
}

// IsTerminal reports whatever the terminal probe says, both ways.
func TestIsTerminal_FollowsTerminalCheck(t *testing.T) {
	orig := terminalCheck
	t.Cleanup(func() { terminalCheck = orig })
	for _, want := range []bool{true, false} {
		terminalCheck = func(int) bool { return want }
		if got := IsTerminal(); got != want {
			t.Errorf("IsTerminal() = %v with a terminal probe returning %v", got, want)
		}
	}
}

// Regression: /dev/null is a character device, so the ModeCharDevice test
// this function used to perform (before the fix this behavior was ported
// from) called `cmd < /dev/null` interactive — the exact shape an agent, a
// cron job, or a CI runner uses. This asserts the real probe (not the seam)
// against the real device.
func TestIsTerminal_DevNullIsNotATerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	os.Stdin = devNull

	if IsTerminal() {
		t.Errorf("IsTerminal() = true for %s; a character device is not a terminal", os.DevNull)
	}
}
