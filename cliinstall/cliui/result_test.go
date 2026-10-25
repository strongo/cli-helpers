package cliui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestWriteResult_DetailsBeforeInstall_Golden(t *testing.T) {
	ovdb, ok := cliinstall.ByID("ovdb")
	if !ok {
		t.Fatal("catalog missing ovdb")
	}
	relevance := ""
	for _, r := range cliinstall.Relevant("datatug") {
		if r.Target == "ovdb" {
			relevance = r.Text
		}
	}

	row := Row{
		Entry:     ovdb,
		Relevant:  true,
		Relevance: relevance,
		Status:    cliinstall.Status{ID: "ovdb", State: cliinstall.NotInstalled},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeDryRun, Method: cliinstall.MethodDirect,
			Destination: "/home/alex/.local/bin/ovdb", Version: "0.5.0", Tag: "v0.5.0",
			AssetURL: "https://github.com/openvaultdb/ovdb/releases/download/v0.5.0/ovdb_0.5.0_linux_amd64.tar.gz",
		},
	}

	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)

	want := fmt.Sprintf(
		"== ovdb ==\n%s\n\n%s\nHomepage: %s\nWhy relevant to datatug: %s\nStatus: not installed\nPlan: direct install, release v0.5.0 (tag v0.5.0), asset https://github.com/openvaultdb/ovdb/releases/download/v0.5.0/ovdb_0.5.0_linux_amd64.tar.gz, destination /home/alex/.local/bin/ovdb\n",
		ovdb.Description, ovdb.Details, ovdb.Homepage, relevance,
	)
	if out.String() != want {
		t.Errorf("WriteResult (details preview) mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
}

func TestWriteResult_HomebrewDryRun(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "wb"},
		Plan: &cliinstall.Result{
			Target: "wb", Outcome: cliinstall.OutcomeDryRun, Method: cliinstall.MethodHomebrew,
			CaskArgv: []string{"brew", "install", "--cask", "sneat-dev/tap/wb"},
		},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "specscore", []Row{row}, nil)
	want := "== wb ==\nNot listed as relevant to specscore.\nStatus: not installed\nPlan: brew install --cask sneat-dev/tap/wb\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_AlreadyInstalled(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "ovdb"},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeAlreadyInstalled, Version: "1.2.3", UpdateHint: "ovdb self-update",
		},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== ovdb ==\nNot listed as relevant to datatug.\nStatus: not installed\nResult: already installed (v1.2.3); update with `ovdb self-update`\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_Redirected(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "wb"},
		Plan:  &cliinstall.Result{Target: "wb", Outcome: cliinstall.OutcomeRedirected, CaskArgv: []string{"brew", "install", "--cask", "sneat-dev/tap/wb"}},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "specscore", []Row{row}, nil)
	if got := out.String(); got != "== wb ==\nNot listed as relevant to specscore.\nStatus: not installed\nResult: redirected; run: brew install --cask sneat-dev/tap/wb\n" {
		t.Errorf("got %q", got)
	}
}

func TestWriteResult_Declined(t *testing.T) {
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}, Plan: &cliinstall.Result{Target: "ovdb", Outcome: cliinstall.OutcomeDeclined}}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	if got := out.String(); got != "== ovdb ==\nNot listed as relevant to datatug.\nStatus: not installed\nResult: declined; nothing installed\n" {
		t.Errorf("got %q", got)
	}
}

func TestWriteResult_InstalledDirect(t *testing.T) {
	row := Row{
		Entry:  cliinstall.Entry{ID: "ovdb"},
		Status: cliinstall.Status{State: cliinstall.Installed, Version: "0.5.0", Path: "/home/alex/.local/bin/ovdb", Method: selfupdate.Manual},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeInstalled, Method: cliinstall.MethodDirect,
			Destination: "/home/alex/.local/bin/ovdb", Version: "0.5.0",
		},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== ovdb ==\nNot listed as relevant to datatug.\n" +
		"Status: installed v0.5.0 at /home/alex/.local/bin/ovdb (manual)\n" +
		"Result: installed v0.5.0 at /home/alex/.local/bin/ovdb\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_InstalledHomebrew(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "wb"},
		Plan: &cliinstall.Result{
			Target: "wb", Outcome: cliinstall.OutcomeInstalled, Method: cliinstall.MethodHomebrew, Version: "1.0.0",
		},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "specscore", []Row{row}, nil)
	want := "== wb ==\nNot listed as relevant to specscore.\nStatus: not installed\nResult: installed via Homebrew (v1.0.0)\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_FailedWithFailure(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "ovdb"},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeFailed,
			Failure: &selfupdate.Failure{Kind: selfupdate.KindDestinationExists, Err: fmt.Errorf("already there")},
		},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== ovdb ==\nNot listed as relevant to datatug.\nStatus: not installed\nResult: failed (destination_exists): already there\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// A KindUnknownTarget failure has no catalog Entry at all — REQ:
// unknown-target-refused. RowName falls back to Plan.Target and the
// description/details/homepage/relevance/status block is skipped entirely,
// since there is no catalog entry to describe.
func TestWriteResult_UnknownTargetHasNoEntryBlock(t *testing.T) {
	row := Row{
		Plan: &cliinstall.Result{
			Target: "nosuchcli", Outcome: cliinstall.OutcomeFailed,
			Failure: &selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: fmt.Errorf("not a known install target; valid ids: ovdb")},
		},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== nosuchcli ==\nResult: failed (unknown_target): not a known install target; valid ids: ovdb\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_FailedWithNilFailure(t *testing.T) {
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}, Plan: &cliinstall.Result{Target: "ovdb", Outcome: cliinstall.OutcomeFailed}}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== ovdb ==\nNot listed as relevant to datatug.\nStatus: not installed\nResult: failed (unexpected): \n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_UnknownOutcome(t *testing.T) {
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}, Plan: &cliinstall.Result{Target: "ovdb", Outcome: cliinstall.Outcome(99)}}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== ovdb ==\nNot listed as relevant to datatug.\nStatus: not installed\nResult: unknown\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_NoPlan(t *testing.T) {
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}, Relevant: true, Relevance: "why"}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "== ovdb ==\nWhy relevant to datatug: why\nStatus: not installed\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_MultipleRowsAreBlankLineSeparated(t *testing.T) {
	rows := []Row{
		{Entry: cliinstall.Entry{ID: "a"}},
		{Entry: cliinstall.Entry{ID: "b"}},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "host", rows, nil)
	want := "== a ==\nNot listed as relevant to host.\nStatus: not installed\n\n== b ==\nNot listed as relevant to host.\nStatus: not installed\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_WarningsMergedAndOnStderr(t *testing.T) {
	row := Row{
		Entry:  cliinstall.Entry{ID: "ovdb"},
		Status: cliinstall.Status{Warnings: []string{"not on PATH"}},
		Plan:   &cliinstall.Result{Target: "ovdb", Outcome: cliinstall.OutcomeDryRun, Warnings: []string{"shadowed"}},
	}
	var out, errOut bytes.Buffer
	WriteResult(&out, &errOut, "datatug", []Row{row}, nil)
	want := "install: warning: ovdb: not on PATH\ninstall: warning: ovdb: shadowed\n"
	if errOut.String() != want {
		t.Errorf("errOut = %q, want %q", errOut.String(), want)
	}
}

func TestWriteResult_BatchErrorLine(t *testing.T) {
	var out, errOut bytes.Buffer
	batchErr := errors.New("unknown targets: nosuchcli; valid ids: ovdb")
	WriteResult(&out, &errOut, "datatug", nil, batchErr)
	want := "Refused: unknown targets: nosuchcli; valid ids: ovdb\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestWriteResult_BatchErrorLineWithRows(t *testing.T) {
	var out, errOut bytes.Buffer
	batchErr := errors.New("refused")
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}}
	WriteResult(&out, &errOut, "datatug", []Row{row}, batchErr)
	want := "Refused: refused\n\n== ovdb ==\nNot listed as relevant to datatug.\nStatus: not installed\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// --- WriteOutcome ---------------------------------------------------------

func TestWriteOutcome_TerseNoDescriptionRepeat(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "ovdb", Description: "long description", Details: "long details"},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeInstalled, Method: cliinstall.MethodDirect,
			Destination: "/home/alex/.local/bin/ovdb", Version: "0.5.0",
		},
	}
	var out, errOut bytes.Buffer
	WriteOutcome(&out, &errOut, []Row{row}, nil)
	want := "ovdb: installed v0.5.0 at /home/alex/.local/bin/ovdb\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
	if bytes.Contains(out.Bytes(), []byte("long description")) || bytes.Contains(out.Bytes(), []byte("long details")) {
		t.Errorf("WriteOutcome must not repeat description/details: %q", out.String())
	}
}

func TestWriteOutcome_NoPlanUsesStatusSummary(t *testing.T) {
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}, Status: cliinstall.Status{State: cliinstall.NotInstalled}}
	var out, errOut bytes.Buffer
	WriteOutcome(&out, &errOut, []Row{row}, nil)
	if out.String() != "ovdb: not installed\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestWriteOutcome_BatchError(t *testing.T) {
	var out, errOut bytes.Buffer
	WriteOutcome(&out, &errOut, nil, errors.New("no tty"))
	if out.String() != "Refused: no tty\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestWriteOutcome_WarningsToStderr(t *testing.T) {
	row := Row{
		Entry:  cliinstall.Entry{ID: "ovdb"},
		Status: cliinstall.Status{Warnings: []string{"not on PATH"}},
	}
	var out, errOut bytes.Buffer
	WriteOutcome(&out, &errOut, []Row{row}, nil)
	if errOut.String() != "install: warning: ovdb: not on PATH\n" {
		t.Errorf("errOut = %q", errOut.String())
	}
}

func TestWriteResultJSON(t *testing.T) {
	ovdb, _ := cliinstall.ByID("ovdb")
	row := Row{
		Entry:  ovdb,
		Status: cliinstall.Status{State: cliinstall.NotInstalled},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeDryRun, Method: cliinstall.MethodDirect,
			Destination: "/home/alex/.local/bin/ovdb", Version: "0.5.0", Tag: "v0.5.0",
		},
	}
	var out, errOut bytes.Buffer
	if err := WriteResultJSON(&out, &errOut, "datatug", []Row{row}, nil); err != nil {
		t.Fatalf("WriteResultJSON error = %v", err)
	}

	var doc struct {
		Host    string `json:"host"`
		Targets []struct {
			Name           string `json:"name"`
			Details        string `json:"details"`
			Outcome        string `json:"outcome"`
			Method         string `json:"method"`
			Destination    string `json:"destination"`
			PlannedVersion string `json:"planned_version"`
			Tag            string `json:"tag"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v (raw %s)", err, out.String())
	}
	if len(doc.Targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(doc.Targets))
	}
	got := doc.Targets[0]
	if got.Name != "ovdb" || got.Details != ovdb.Details || got.Outcome != "dry_run" || got.Method != "direct" ||
		got.Destination != "/home/alex/.local/bin/ovdb" || got.PlannedVersion != "0.5.0" || got.Tag != "v0.5.0" {
		t.Errorf("targets[0] = %+v", got)
	}
}

func TestWriteResultJSON_FailureFields(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "ovdb"},
		Plan: &cliinstall.Result{
			Target: "ovdb", Outcome: cliinstall.OutcomeFailed,
			Failure: &selfupdate.Failure{Kind: selfupdate.KindNoInstallDir, Err: fmt.Errorf("no dir")},
		},
	}
	var out, errOut bytes.Buffer
	if err := WriteResultJSON(&out, &errOut, "datatug", []Row{row}, nil); err != nil {
		t.Fatalf("error = %v", err)
	}
	var doc struct {
		Targets []struct {
			FailureKind string `json:"failure_kind"`
			Error       string `json:"error"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Targets[0].FailureKind != "no_install_dir" || doc.Targets[0].Error != "no dir" {
		t.Errorf("targets[0] = %+v", doc.Targets[0])
	}
}

func TestWriteResultJSON_ManagerField(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "wb"},
		Status: cliinstall.Status{
			State: cliinstall.Installed, Method: selfupdate.Managed,
			Manager: &selfupdate.Manager{Name: "Homebrew"},
		},
	}
	var out, errOut bytes.Buffer
	if err := WriteResultJSON(&out, &errOut, "specscore", []Row{row}, nil); err != nil {
		t.Fatalf("WriteResultJSON error = %v", err)
	}
	var doc struct {
		Targets []struct {
			Manager string `json:"manager"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Targets[0].Manager != "Homebrew" {
		t.Errorf("manager = %q, want Homebrew", doc.Targets[0].Manager)
	}
}

// M6: an unknown-target row must not carry phantom zero-value planning
// fields (method/destination/cask_argv) — it was never planned at all.
func TestWriteResultJSON_UnknownTargetOmitsPlanningFields(t *testing.T) {
	row := Row{
		Plan: &cliinstall.Result{
			Target: "nosuchcli", Outcome: cliinstall.OutcomeFailed,
			Failure: &selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: errors.New("not a known install target")},
		},
	}
	var out, errOut bytes.Buffer
	if err := WriteResultJSON(&out, &errOut, "datatug", []Row{row}, nil); err != nil {
		t.Fatalf("WriteResultJSON error = %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte(`"method"`)) {
		t.Errorf("output contains a phantom method field for an unknown target: %s", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte(`"destination"`)) {
		t.Errorf("output contains a phantom destination field for an unknown target: %s", out.String())
	}
	var doc struct {
		Targets []struct {
			Name        string `json:"name"`
			Outcome     string `json:"outcome"`
			FailureKind string `json:"failure_kind"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Targets[0].Name != "nosuchcli" || doc.Targets[0].Outcome != "failed" || doc.Targets[0].FailureKind != "unknown_target" {
		t.Errorf("targets[0] = %+v", doc.Targets[0])
	}
}

func TestWriteResultJSON_BatchError(t *testing.T) {
	batchErr := &selfupdate.Failure{Kind: selfupdate.KindUnknownTarget, Err: errors.New("nosuchcli: not a known install target")}
	var out, errOut bytes.Buffer
	if err := WriteResultJSON(&out, &errOut, "datatug", nil, batchErr); err != nil {
		t.Fatalf("WriteResultJSON error = %v", err)
	}
	var doc struct {
		Host        string `json:"host"`
		Error       string `json:"error"`
		FailureKind string `json:"failure_kind"`
		Targets     []any  `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error == "" || doc.FailureKind != "unknown_target" {
		t.Errorf("doc = %+v", doc)
	}
	if doc.Targets == nil {
		t.Error("Targets must be present (possibly empty), never absent")
	}
}

func TestWriteResultJSON_EncodeError(t *testing.T) {
	err := WriteResultJSON(failingWriter{}, &bytes.Buffer{}, "datatug", nil, nil)
	if err == nil {
		t.Fatal("expected an error from a failing writer")
	}
}
