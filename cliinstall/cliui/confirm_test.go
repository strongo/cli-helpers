package cliui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// plannedResults builds the []cliinstall.Result Confirm's callback now
// receives, one per name, matching cliinstall.Plan's own OutcomeDryRun
// shape closely enough for these tests (only Target matters here).
func plannedResults(names ...string) []cliinstall.Result {
	out := make([]cliinstall.Result, len(names))
	for i, n := range names {
		out[i] = cliinstall.Result{Target: n, Outcome: cliinstall.OutcomeDryRun}
	}
	return out
}

func TestConfirm_InteractivePromptReadsStdin(t *testing.T) {
	var out bytes.Buffer
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader("y\n"),
		Out:         &out,
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedResults("ovdb", "ingitdb"))
	if err != nil || !proceed {
		t.Fatalf("Confirm with 'y' = (%v, %v), want (true, nil)", proceed, err)
	}
	if !strings.Contains(out.String(), "Install ovdb, ingitdb? [y/N] ") {
		t.Errorf("prompt = %q", out.String())
	}
}

func TestConfirm_YesWordAlsoProceeds(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader("yes\n"),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedResults("ovdb"))
	if err != nil || !proceed {
		t.Fatalf("Confirm with 'yes' = (%v, %v), want (true, nil)", proceed, err)
	}
}

func TestConfirm_ExplicitNoIsDeclineNotFailure(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader("n\n"),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedResults("ovdb"))
	if proceed || err != nil {
		t.Errorf("Confirm with 'n' = (%v, %v), want (false, nil)", proceed, err)
	}
}

func TestConfirm_NonInteractiveRefusal(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return false },
	})
	proceed, err := confirm(plannedResults("ovdb"))
	if proceed {
		t.Error("Confirm proceeded without an interactive terminal")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
	if !strings.Contains(err.Error(), "ovdb") {
		t.Errorf("error %q does not name the pending target", err.Error())
	}
}

func TestConfirm_EmptyStdinRefusesInsteadOfDeclining(t *testing.T) {
	confirm := Confirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedResults("ovdb"))
	if proceed {
		t.Error("Confirm proceeded on empty stdin")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
}

// A nil ConfirmOptions.Interactive defaults to selfupdate/cliui.IsTerminal —
// reused rather than a second terminal check. Forcing stdin to /dev/null
// makes that default deterministic under `go test`.
//
// In is deliberately "y\n", NOT empty (task-5 review M11: an empty reader
// makes this test vacuous — refusal follows from stdin being empty on
// EITHER branch of a bug in the default, so it never actually proves
// selfupdate/cliui.IsTerminal was consulted at all). With a real answer
// queued, a broken default that resolved to "always interactive" would
// read it and proceed, failing this test; only the correct default (which
// sees /dev/null and refuses before ever reading In) passes.
func TestConfirm_NilInteractiveDefaultsToSelfupdateIsTerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	os.Stdin = devNull

	confirm := Confirm(ConfirmOptions{In: strings.NewReader("y\n"), Out: &bytes.Buffer{}})
	proceed, cErr := confirm(plannedResults("ovdb"))
	if proceed {
		t.Error("Confirm proceeded with a nil Interactive and /dev/null stdin")
	}
	if selfupdate.KindOf(cErr) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(cErr))
	}
}
