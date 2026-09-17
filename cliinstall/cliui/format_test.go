package cliui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestDateOnly(t *testing.T) {
	cases := map[string]string{
		"2026-08-01T12:34:56Z": "2026-08-01",
		"2026-08-01":           "2026-08-01",
		"":                     "",
	}
	for in, want := range cases {
		if got := dateOnly(in); got != want {
			t.Errorf("dateOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDateLabel(t *testing.T) {
	cases := map[string]string{
		"build":  "built",
		"commit": "committed",
		"":       "date",
		"weird":  "date",
	}
	for in, want := range cases {
		if got := dateLabel(in); got != want {
			t.Errorf("dateLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortCommit(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"ab12cd3":         "ab12cd3",
		"ab12cd345678901": "ab12cd3",
		"ab12cd3+dirty":   "ab12cd3+dirty",
		"ab12+dirty":      "ab12+dirty",
	}
	for in, want := range cases {
		if got := shortCommit(in); got != want {
			t.Errorf("shortCommit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMethodLabel(t *testing.T) {
	if got := methodLabel(cliinstall.Status{Method: selfupdate.Managed, Manager: &selfupdate.Manager{Name: "Homebrew"}}); got != "Homebrew" {
		t.Errorf("managed with manager = %q", got)
	}
	if got := methodLabel(cliinstall.Status{Method: selfupdate.Managed}); got != "managed" {
		t.Errorf("managed without manager = %q", got)
	}
	if got := methodLabel(cliinstall.Status{Method: selfupdate.Manual}); got != "direct" {
		t.Errorf("manual = %q", got)
	}
	if got := methodLabel(cliinstall.Status{Method: selfupdate.Ambiguous}); got != "ambiguous" {
		t.Errorf("ambiguous = %q", got)
	}
}

func TestPluralCopies(t *testing.T) {
	if pluralCopies(1) != "copy" {
		t.Error("pluralCopies(1) != copy")
	}
	if pluralCopies(2) != "copies" {
		t.Error("pluralCopies(2) != copies")
	}
}

func TestStatusSummary_NotInstalled(t *testing.T) {
	got := statusSummary(cliinstall.Status{State: cliinstall.NotInstalled})
	if got != "not installed" {
		t.Errorf("statusSummary(NotInstalled) = %q", got)
	}
}

func TestStatusSummary_Unrecognized(t *testing.T) {
	s := cliinstall.Status{
		State:      cliinstall.Unrecognized,
		Path:       "/opt/x/foo",
		Method:     selfupdate.Manual,
		OtherPaths: []string{"/other/foo"},
	}
	got := statusSummary(s)
	want := "unrecognized copy at /opt/x/foo (direct); 1 other copy"
	if got != want {
		t.Errorf("statusSummary(Unrecognized) = %q, want %q", got, want)
	}
}

func TestStatusSummary_InstalledFull(t *testing.T) {
	s := cliinstall.Status{
		State:      cliinstall.Installed,
		Version:    "1.2.3",
		Date:       "2026-08-01T00:00:00Z",
		DateSource: "build",
		Commit:     "abcdef1234567",
		Path:       "/home/x/.local/bin/ovdb",
		Method:     selfupdate.Manual,
		OtherPaths: []string{"/a", "/b"},
	}
	got := statusSummary(s)
	want := "installed v1.2.3, built 2026-08-01, abcdef1 at /home/x/.local/bin/ovdb (direct); 2 other copies"
	if got != want {
		t.Errorf("statusSummary(Installed) = %q, want %q", got, want)
	}
}

func TestStatusSummary_InstalledMinimal(t *testing.T) {
	s := cliinstall.Status{
		State:   cliinstall.Installed,
		Path:    "/bin/wb",
		Method:  selfupdate.Managed,
		Manager: &selfupdate.Manager{Name: "Homebrew"},
	}
	got := statusSummary(s)
	want := "installed at /bin/wb (Homebrew)"
	if got != want {
		t.Errorf("statusSummary(Installed minimal) = %q, want %q", got, want)
	}
}

func TestStatusSummary_UnknownState(t *testing.T) {
	got := statusSummary(cliinstall.Status{State: cliinstall.State(99)})
	if got != "unknown" {
		t.Errorf("statusSummary(invalid state) = %q, want unknown", got)
	}
}

func TestDedupedWarnings(t *testing.T) {
	row := Row{
		Status: cliinstall.Status{Warnings: []string{"a", "b"}},
		Plan:   &cliinstall.Result{Warnings: []string{"b", "c"}},
	}
	got := dedupedWarnings(row)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupedWarnings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dedupedWarnings[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDedupedWarnings_NoPlan(t *testing.T) {
	row := Row{Status: cliinstall.Status{Warnings: []string{"only"}}}
	got := dedupedWarnings(row)
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("dedupedWarnings (no plan) = %v", got)
	}
}

func TestWriteWarnings(t *testing.T) {
	var errOut bytes.Buffer
	rows := []Row{
		{Entry: cliinstall.Entry{ID: "ovdb"}, Status: cliinstall.Status{Warnings: []string{"not on PATH"}}},
		{Entry: cliinstall.Entry{ID: "ingitdb"}},
	}
	writeWarnings(&errOut, rows)
	out := errOut.String()
	if !strings.Contains(out, "install: warning: ovdb: not on PATH\n") {
		t.Errorf("errOut = %q", out)
	}
	if strings.Contains(out, "ingitdb") {
		t.Errorf("errOut contains a line for a target with no warnings: %q", out)
	}
}

func TestInstallMethodJSON(t *testing.T) {
	if got := installMethodJSON(cliinstall.Status{State: cliinstall.NotInstalled}); got != "" {
		t.Errorf("installMethodJSON(NotInstalled) = %q, want empty", got)
	}
	if got := installMethodJSON(cliinstall.Status{State: cliinstall.Installed, Method: selfupdate.Manual}); got != "manual" {
		t.Errorf("installMethodJSON(Installed) = %q, want manual", got)
	}
}
