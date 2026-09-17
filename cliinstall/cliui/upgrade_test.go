package cliui

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- upgradeLine / WriteUpgradeReport (text) -------------------------------

func TestUpgradeLine_AllOutcomes(t *testing.T) {
	homebrew := &selfupdate.Manager{Name: "Homebrew", UpgradeCommand: "brew upgrade --cask wb"}
	executable := &selfupdate.Manager{Name: "Homebrew", UpgradeCommand: "brew upgrade --cask wb"}
	*executable = executable.WithExecutableUpgrade("brew", "upgrade", "--cask", "wb")

	tests := []struct {
		name string
		row  UpgradeRow
		want string
	}{
		{
			name: "dry_run manual",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "datatug", Outcome: cliinstall.UpgradeOutcomeDryRun,
				InstallMethod: selfupdate.Manual, Current: "0.29.0", Latest: "0.30.1", ResolvedPath: "/home/alex/.local/bin/datatug",
			}},
			want: "0.29.0 → 0.30.1  upgrade available (manual, /home/alex/.local/bin/datatug)",
		},
		{
			name: "dry_run managed executable",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "wb", Outcome: cliinstall.UpgradeOutcomeDryRun,
				InstallMethod: selfupdate.Managed, Manager: executable, Command: executable.UpgradeCommand,
				Current: "1.2.0", Latest: "1.3.0",
			}},
			want: "1.2.0 → 1.3.0  upgrade available (Homebrew: brew upgrade --cask wb)",
		},
		{
			name: "redirected",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeRedirected,
				InstallMethod: selfupdate.Managed, Manager: homebrew, Command: homebrew.UpgradeCommand,
				Current: "0.14.1", Latest: "0.15.0",
			}},
			want: "0.14.1 → 0.15.0  managed by Homebrew — run: brew upgrade --cask wb",
		},
		{
			name: "already current",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeAlreadyCurrent, Current: "0.14.1", Latest: "0.14.1",
			}},
			want: "0.14.1  up to date",
		},
		{
			name: "ahead",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "synchestra", Outcome: cliinstall.UpgradeOutcomeAhead, Current: "0.20.4-dev", Latest: "0.20.3",
			}},
			want: "0.20.4-dev  ahead of latest 0.20.3",
		},
		{
			name: "skipped non-release",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "ingitdb", Outcome: cliinstall.UpgradeOutcomeSkippedNonRelease, Current: "dev",
			}},
			want: "dev  skipped: not a release build (name it to upgrade)",
		},
		{
			name: "not installed",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "specscore", Outcome: cliinstall.UpgradeOutcomeNotInstalled, InstallHint: "ovdb install specscore",
			}},
			want: "not installed — run 'ovdb install specscore'",
		},
		{
			name: "unrecognized",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "datatug", Outcome: cliinstall.UpgradeOutcomeUnrecognized,
				Status: cliinstall.Status{Path: "/opt/weird/datatug"},
			}},
			want: "unrecognized copy at /opt/weird/datatug — never touched",
		},
		{
			name: "refused ambiguous",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "foo", Outcome: cliinstall.UpgradeOutcomeRefused,
				Current: "1.0.0", Latest: "1.1.0", ResolvedPath: "/src/foo/foo",
			}},
			want: "1.0.0 → 1.1.0  ambiguous install at /src/foo/foo — update manually",
		},
		{
			name: "declined",
			row:  UpgradeRow{Result: cliinstall.UpgradeResult{Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeDeclined}},
			want: "declined; nothing upgraded",
		},
		{
			name: "upgraded",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeUpgraded,
				Current: "0.14.1", Tag: "v0.15.0", ResolvedPath: "/home/alex/.local/bin/ovdb",
			}},
			want: "upgraded 0.14.1 → v0.15.0 at /home/alex/.local/bin/ovdb",
		},
		{
			name: "upgraded with finish hint",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "wb", Outcome: cliinstall.UpgradeOutcomeUpgraded,
				Current: "1.0.0", Tag: "v2.0.0", ResolvedPath: "/opt/homebrew/bin/wb", FinishHint: "wb self-update",
			}},
			want: "upgraded 1.0.0 → v2.0.0 at /opt/homebrew/bin/wb (finish: `wb self-update`)",
		},
		{
			name: "manager executed with finish hint",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "wb", Outcome: cliinstall.UpgradeOutcomeManagerExecuted,
				Manager: homebrew, FinishHint: "wb self-update",
			}},
			want: "upgrade command completed via Homebrew (finish: `wb self-update`)",
		},
		{
			name: "failed",
			row: UpgradeRow{Result: cliinstall.UpgradeResult{
				Target: "wb", Outcome: cliinstall.UpgradeOutcomeFailed,
				Failure: &selfupdate.Failure{Kind: selfupdate.KindReleaseLookup, Err: errors.New("rate limit reached")},
			}},
			want: "failed (release_lookup): rate limit reached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upgradeLine(tt.row); got != tt.want {
				t.Errorf("upgradeLine =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestUpgradeLine_UnknownOutcomeDefault(t *testing.T) {
	row := UpgradeRow{Result: cliinstall.UpgradeResult{Target: "x", Outcome: cliinstall.UpgradeOutcome(999)}}
	if got := upgradeLine(row); got != "unknown" {
		t.Errorf("upgradeLine(unknown outcome) = %q, want unknown", got)
	}
}

func TestUpgradeMethodLabel(t *testing.T) {
	tests := []struct {
		name string
		row  UpgradeRow
		want string
	}{
		{"manual", UpgradeRow{Result: cliinstall.UpgradeResult{InstallMethod: selfupdate.Manual}}, "manual"},
		{"managed no manager", UpgradeRow{Result: cliinstall.UpgradeResult{InstallMethod: selfupdate.Managed}}, "managed"},
		{"managed with manager", UpgradeRow{Result: cliinstall.UpgradeResult{InstallMethod: selfupdate.Managed, Manager: &selfupdate.Manager{Name: "Scoop"}}}, "Scoop"},
		{"ambiguous", UpgradeRow{Result: cliinstall.UpgradeResult{InstallMethod: selfupdate.Ambiguous}}, "ambiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upgradeMethodLabel(tt.row); got != tt.want {
				t.Errorf("upgradeMethodLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteUpgradeReport_NameThenLinePerRow(t *testing.T) {
	rows := []UpgradeRow{
		{Result: cliinstall.UpgradeResult{Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeAlreadyCurrent, Current: "0.14.1", Latest: "0.14.1"}},
		{Result: cliinstall.UpgradeResult{Target: "specscore", Outcome: cliinstall.UpgradeOutcomeNotInstalled, InstallHint: "ovdb install specscore"}},
	}
	var out, errOut bytes.Buffer
	WriteUpgradeReport(&out, &errOut, rows, nil)

	want := "ovdb  0.14.1  up to date\n" +
		"specscore  not installed — run 'ovdb install specscore'\n"
	if out.String() != want {
		t.Errorf("WriteUpgradeReport =\n%s\nwant\n%s", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("errOut = %q, want empty (no warnings)", errOut.String())
	}
}

func TestWriteUpgradeReport_BatchErrorAndWarnings(t *testing.T) {
	rows := []UpgradeRow{
		{Result: cliinstall.UpgradeResult{
			Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeDryRun, Current: "0.14.1", Latest: "0.15.0",
			InstallMethod: selfupdate.Manual, ResolvedPath: "/bin/ovdb",
			Warnings: []string{"ovdb's install method is ambiguous at /bin/ovdb; update it manually"},
		}},
	}
	var out, errOut bytes.Buffer
	batchErr := errors.New("boom")
	WriteUpgradeReport(&out, &errOut, rows, batchErr)

	if !strings.HasPrefix(out.String(), "Refused: boom\n") {
		t.Errorf("out = %q, want it to start with the batch refusal", out.String())
	}
	wantWarning := "upgrade: warning: ovdb: ovdb's install method is ambiguous at /bin/ovdb; update it manually\n"
	if errOut.String() != wantWarning {
		t.Errorf("errOut = %q, want %q", errOut.String(), wantWarning)
	}
}

func TestWriteUpgradeNextStep(t *testing.T) {
	var out bytes.Buffer
	WriteUpgradeNextStep(&out, "ovdb")
	if !strings.Contains(out.String(), "ovdb upgrade --all") || !strings.Contains(out.String(), "ovdb upgrade <name>") {
		t.Errorf("WriteUpgradeNextStep = %q", out.String())
	}
}

// --- host row special-casing ------------------------------------------------

func TestUpgradeStatusToken_HostAlwaysInstalled(t *testing.T) {
	row := UpgradeRow{Result: cliinstall.UpgradeResult{Target: "ovdb", Host: true}}
	if got := upgradeStatusToken(row); got != "installed" {
		t.Errorf("upgradeStatusToken(host) = %q, want installed", got)
	}
}

func TestUpgradeStatusToken_NonHostUsesProbedState(t *testing.T) {
	row := UpgradeRow{Result: cliinstall.UpgradeResult{Target: "ovdb", Status: cliinstall.Status{State: cliinstall.NotInstalled}}}
	if got := upgradeStatusToken(row); got != "not_installed" {
		t.Errorf("upgradeStatusToken(non-host) = %q, want not_installed", got)
	}
}

func TestUpgradeLocated(t *testing.T) {
	tests := []struct {
		name string
		row  UpgradeRow
		want bool
	}{
		{"host always located", UpgradeRow{Result: cliinstall.UpgradeResult{Host: true}}, true},
		{"not installed unlocated", UpgradeRow{Result: cliinstall.UpgradeResult{Outcome: cliinstall.UpgradeOutcomeNotInstalled}}, false},
		{"unrecognized unlocated", UpgradeRow{Result: cliinstall.UpgradeResult{Outcome: cliinstall.UpgradeOutcomeUnrecognized}}, false},
		{"skipped non-release still located", UpgradeRow{Result: cliinstall.UpgradeResult{Outcome: cliinstall.UpgradeOutcomeSkippedNonRelease}}, true},
		{"dry run located", UpgradeRow{Result: cliinstall.UpgradeResult{Outcome: cliinstall.UpgradeOutcomeDryRun}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upgradeLocated(tt.row); got != tt.want {
				t.Errorf("upgradeLocated = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- JSON -------------------------------------------------------------------

func TestRowToUpgradeJSON_HostRow(t *testing.T) {
	manager := &selfupdate.Manager{Name: "Homebrew"}
	row := UpgradeRow{
		Entry: cliinstall.Entry{ID: "wb", Description: "Fleet orchestrator"},
		Result: cliinstall.UpgradeResult{
			Target: "wb", Host: true, Outcome: cliinstall.UpgradeOutcomeUpgraded,
			InstallMethod: selfupdate.Managed, Manager: manager, ResolvedPath: "/opt/homebrew/bin/wb",
			Current: "1.0.0", Latest: "2.0.0", Tag: "v2.0.0", Verdict: selfupdate.UpdateAvailable,
			Warnings: []string{"another copy of wb is on PATH at /usr/local/bin/wb; it was left untouched"},
		},
	}
	got := rowToUpgradeJSON(row)

	if !got.Host {
		t.Error("Host = false, want true")
	}
	if got.Status != "installed" {
		t.Errorf("Status = %q, want installed", got.Status)
	}
	if got.Version != "" || got.Commit != "" || got.Date != "" {
		t.Errorf("base probe fields not empty for host row: %+v", got)
	}
	if got.Current != "1.0.0" || got.Latest != "2.0.0" || got.Tag != "v2.0.0" {
		t.Errorf("Current/Latest/Tag = %q/%q/%q", got.Current, got.Latest, got.Tag)
	}
	if got.Verdict != "update_available" {
		t.Errorf("Verdict = %q, want update_available", got.Verdict)
	}
	if got.Action != "upgraded" {
		t.Errorf("Action = %q, want upgraded", got.Action)
	}
	if got.InstallMethod != "managed" || got.Manager != "Homebrew" {
		t.Errorf("InstallMethod/Manager = %q/%q", got.InstallMethod, got.Manager)
	}
	if len(got.Warnings) != 1 {
		t.Errorf("Warnings = %v", got.Warnings)
	}
}

func TestRowToUpgradeJSON_NotInstalled_OmitsLocationFields(t *testing.T) {
	row := UpgradeRow{
		Result: cliinstall.UpgradeResult{Target: "specscore", Outcome: cliinstall.UpgradeOutcomeNotInstalled, InstallHint: "ovdb install specscore"},
	}
	got := rowToUpgradeJSON(row)
	if got.InstallMethod != "" || got.Manager != "" {
		t.Errorf("InstallMethod/Manager = %q/%q, want empty for a not-installed target", got.InstallMethod, got.Manager)
	}
	if got.Verdict != "" {
		t.Errorf("Verdict = %q, want empty (no lookup ran)", got.Verdict)
	}
	if got.InstallHint != "ovdb install specscore" {
		t.Errorf("InstallHint = %q", got.InstallHint)
	}
	if got.Action != "not_installed" {
		t.Errorf("Action = %q, want not_installed", got.Action)
	}
}

func TestRowToUpgradeJSON_FailedIncludesFailureKind(t *testing.T) {
	row := UpgradeRow{
		Result: cliinstall.UpgradeResult{
			Target: "wb", Outcome: cliinstall.UpgradeOutcomeFailed,
			Failure: &selfupdate.Failure{Kind: selfupdate.KindReleaseLookup, Err: errors.New("GitHub API rate limit reached; set GH_TOKEN or GITHUB_TOKEN to raise it")},
		},
	}
	got := rowToUpgradeJSON(row)
	if got.FailureKind != "release_lookup" {
		t.Errorf("FailureKind = %q, want release_lookup", got.FailureKind)
	}
	if !strings.Contains(got.Error, "GH_TOKEN") {
		t.Errorf("Error = %q, want it to name GH_TOKEN", got.Error)
	}
}

func TestWriteUpgradeReportJSON_SingleDocument(t *testing.T) {
	rows := []UpgradeRow{
		{Result: cliinstall.UpgradeResult{Target: "ovdb", Outcome: cliinstall.UpgradeOutcomeAlreadyCurrent, Current: "0.14.1", Latest: "0.14.1", Verdict: selfupdate.UpToDate}},
	}
	var out, errOut bytes.Buffer
	if err := WriteUpgradeReportJSON(&out, &errOut, "datatug", rows, nil); err != nil {
		t.Fatalf("WriteUpgradeReportJSON error = %v", err)
	}
	if n := strings.Count(strings.TrimSpace(out.String()), "\n"); n != 0 {
		t.Errorf("stdout has %d extra newlines, want exactly one JSON document:\n%s", n, out.String())
	}
	var doc struct {
		Host    string `json:"host"`
		Targets []struct {
			Name    string `json:"name"`
			Action  string `json:"action"`
			Verdict string `json:"verdict"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v (raw %s)", err, out.String())
	}
	if doc.Host != "datatug" || len(doc.Targets) != 1 || doc.Targets[0].Action != "already_current" || doc.Targets[0].Verdict != "up_to_date" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestWriteUpgradeReportJSON_BatchErrorSetsErrorAndFailureKind(t *testing.T) {
	var out, errOut bytes.Buffer
	batchErr := &selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: errors.New("nosuchcli: not a known install target")}
	if err := WriteUpgradeReportJSON(&out, &errOut, "datatug", nil, batchErr); err != nil {
		t.Fatalf("WriteUpgradeReportJSON error = %v", err)
	}
	var doc struct {
		Error       string `json:"error"`
		FailureKind string `json:"failure_kind"`
		Targets     []any  `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v (raw %s)", err, out.String())
	}
	if doc.FailureKind != "unknown_target" || !strings.Contains(doc.Error, "nosuchcli") {
		t.Errorf("doc = %+v", doc)
	}
	if doc.Targets == nil {
		t.Error("Targets is nil (must be present, possibly empty) even for a batch-level failure")
	}
}

func TestWriteUpgradeReportJSON_WriteErrorPropagates(t *testing.T) {
	err := WriteUpgradeReportJSON(failingWriter{}, &bytes.Buffer{}, "datatug", nil, nil)
	if err == nil {
		t.Fatal("expected the encode error to propagate")
	}
}

// failingWriter is defined once, in list_test.go, and reused here.

// --- UpgradeRowName -----------------------------------------------------

func TestUpgradeRowName(t *testing.T) {
	row := UpgradeRow{Result: cliinstall.UpgradeResult{Target: "ovdb"}}
	if got := UpgradeRowName(row); got != "ovdb" {
		t.Errorf("UpgradeRowName = %q", got)
	}
}

// --- UpgradeConfirm -----------------------------------------------------

func plannedUpgradeResults(names ...string) []cliinstall.UpgradeResult {
	out := make([]cliinstall.UpgradeResult, len(names))
	for i, n := range names {
		out[i] = cliinstall.UpgradeResult{Target: n, Outcome: cliinstall.UpgradeOutcomeDryRun}
	}
	return out
}

func TestUpgradeConfirm_InteractivePromptReadsStdin(t *testing.T) {
	var out bytes.Buffer
	confirm := UpgradeConfirm(ConfirmOptions{
		In:          strings.NewReader("y\n"),
		Out:         &out,
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedUpgradeResults("ovdb", "wb"))
	if err != nil || !proceed {
		t.Fatalf("UpgradeConfirm with 'y' = (%v, %v), want (true, nil)", proceed, err)
	}
	if !strings.Contains(out.String(), "Upgrade ovdb, wb? [y/N] ") {
		t.Errorf("prompt = %q", out.String())
	}
}

func TestUpgradeConfirm_ExplicitNoIsDeclineNotFailure(t *testing.T) {
	confirm := UpgradeConfirm(ConfirmOptions{
		In:          strings.NewReader("n\n"),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedUpgradeResults("ovdb"))
	if proceed || err != nil {
		t.Errorf("UpgradeConfirm with 'n' = (%v, %v), want (false, nil)", proceed, err)
	}
}

func TestUpgradeConfirm_NonInteractiveRefusal(t *testing.T) {
	confirm := UpgradeConfirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return false },
	})
	proceed, err := confirm(plannedUpgradeResults("ovdb"))
	if proceed {
		t.Error("UpgradeConfirm proceeded without an interactive terminal")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
	if !strings.Contains(err.Error(), "ovdb") {
		t.Errorf("error %q does not name the pending target", err.Error())
	}
}

func TestUpgradeConfirm_EmptyStdinRefusesInsteadOfDeclining(t *testing.T) {
	confirm := UpgradeConfirm(ConfirmOptions{
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: func() bool { return true },
	})
	proceed, err := confirm(plannedUpgradeResults("ovdb"))
	if proceed {
		t.Error("UpgradeConfirm proceeded on empty stdin")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
}

func TestUpgradeConfirm_NilInteractiveDefaultsToSelfupdateIsTerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	os.Stdin = devNull

	confirm := UpgradeConfirm(ConfirmOptions{In: strings.NewReader("y\n"), Out: &bytes.Buffer{}})
	proceed, cErr := confirm(plannedUpgradeResults("ovdb"))
	if proceed {
		t.Error("UpgradeConfirm proceeded with a nil Interactive and /dev/null stdin")
	}
	if selfupdate.KindOf(cErr) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(cErr))
	}
}
