package cliui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// datatugFixtureRows builds a realistic listing fixture from the real
// compiled-in catalog: datatug's own two relevant targets, ingitdb
// installed with a full version/date/commit identity and ovdb not
// installed — exactly the scenario the task brief asks a golden-style test
// to cover.
func datatugFixtureRows(t *testing.T) []Row {
	t.Helper()
	ingitdb, ok := cliinstall.ByID("ingitdb")
	if !ok {
		t.Fatal("catalog missing ingitdb")
	}
	ovdb, ok := cliinstall.ByID("ovdb")
	if !ok {
		t.Fatal("catalog missing ovdb")
	}
	relevance := make(map[string]string)
	for _, r := range cliinstall.Relevant("datatug") {
		relevance[r.Target] = r.Text
	}

	return []Row{
		{
			Entry:     ingitdb,
			Relevant:  true,
			Relevance: relevance["ingitdb"],
			Status: cliinstall.Status{
				ID:         "ingitdb",
				State:      cliinstall.Installed,
				Path:       "/home/alex/.local/bin/ingitdb",
				OnPath:     true,
				Version:    "0.65.16",
				Commit:     "ab12cd34567",
				Date:       "2026-08-01T10:00:00Z",
				DateSource: "build",
				Method:     selfupdate.Manual,
			},
		},
		{
			Entry:     ovdb,
			Relevant:  true,
			Relevance: relevance["ovdb"],
			Status:    cliinstall.Status{ID: "ovdb", State: cliinstall.NotInstalled},
		},
	}
}

func TestWriteList_Golden(t *testing.T) {
	rows := datatugFixtureRows(t)
	relevance := make(map[string]string)
	for _, r := range cliinstall.Relevant("datatug") {
		relevance[r.Target] = r.Text
	}
	ingitdb, _ := cliinstall.ByID("ingitdb")
	ovdb, _ := cliinstall.ByID("ovdb")

	var out, errOut bytes.Buffer
	WriteList(&out, &errOut, "datatug", rows)

	want := fmt.Sprintf(
		"ingitdb: installed v0.65.16, built 2026-08-01, ab12cd3 at /home/alex/.local/bin/ingitdb (direct)\n"+
			"  %s\n"+
			"  Why: %s\n"+
			"\n"+
			"ovdb: not installed\n"+
			"  %s\n"+
			"  Why: %s\n"+
			"\n"+
			"Run 'datatug install <name>' to see details and install.\n",
		ingitdb.Description, relevance["ingitdb"], ovdb.Description, relevance["ovdb"],
	)
	if out.String() != want {
		t.Errorf("WriteList output mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
	if errOut.String() != "" {
		t.Errorf("errOut = %q, want empty (fixture rows carry no warnings)", errOut.String())
	}
}

func TestWriteList_NotRelevantNote(t *testing.T) {
	e, _ := cliinstall.ByID("wb")
	rows := []Row{{Entry: e, Relevant: false, Status: cliinstall.Status{State: cliinstall.NotInstalled}}}
	var out, errOut bytes.Buffer
	WriteList(&out, &errOut, "ovdb", rows)
	want := "wb: not installed\n  " + e.Description + "\n  Not listed as relevant to ovdb.\n\nRun 'ovdb install <name>' to see details and install.\n"
	if out.String() != want {
		t.Errorf("WriteList (not relevant) = %q, want %q", out.String(), want)
	}
}

func TestWriteList_Empty(t *testing.T) {
	var out, errOut bytes.Buffer
	WriteList(&out, &errOut, "wb", nil)
	want := "Run 'wb install <name>' to see details and install.\n"
	if out.String() != want {
		t.Errorf("WriteList (empty) = %q, want %q", out.String(), want)
	}
}

func TestWriteList_WarningsToStderr(t *testing.T) {
	rows := []Row{{
		Entry:  cliinstall.Entry{ID: "ovdb", Description: "d"},
		Status: cliinstall.Status{State: cliinstall.NotInstalled, Warnings: []string{"timed out"}},
	}}
	var out, errOut bytes.Buffer
	WriteList(&out, &errOut, "datatug", rows)
	if errOut.String() != "install: warning: ovdb: timed out\n" {
		t.Errorf("errOut = %q", errOut.String())
	}
	if bytes.Contains(out.Bytes(), []byte("timed out")) {
		t.Errorf("warning leaked into stdout: %q", out.String())
	}
}

func TestWriteListJSON(t *testing.T) {
	rows := datatugFixtureRows(t)
	var out, errOut bytes.Buffer
	if err := WriteListJSON(&out, &errOut, "datatug", rows); err != nil {
		t.Fatalf("WriteListJSON error = %v", err)
	}

	var doc struct {
		Host    string `json:"host"`
		Targets []struct {
			Name          string `json:"name"`
			Relevant      bool   `json:"relevant"`
			Description   string `json:"description"`
			Details       string `json:"details"`
			Status        string `json:"status"`
			Version       string `json:"version"`
			Commit        string `json:"commit"`
			DateSource    string `json:"date_source"`
			InstallMethod string `json:"install_method"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v (raw: %s)", err, out.String())
	}
	if doc.Host != "datatug" {
		t.Errorf("host = %q", doc.Host)
	}
	if len(doc.Targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(doc.Targets))
	}
	if doc.Targets[0].Name != "ingitdb" || doc.Targets[0].Status != "installed" || doc.Targets[0].Version != "0.65.16" ||
		doc.Targets[0].Commit != "ab12cd34567" || doc.Targets[0].DateSource != "build" || doc.Targets[0].InstallMethod != "manual" {
		t.Errorf("targets[0] = %+v", doc.Targets[0])
	}
	if doc.Targets[0].Details != "" {
		t.Errorf("listing JSON must omit details, got %q", doc.Targets[0].Details)
	}
	if doc.Targets[1].Name != "ovdb" || doc.Targets[1].Status != "not_installed" || doc.Targets[1].InstallMethod != "" {
		t.Errorf("targets[1] = %+v", doc.Targets[1])
	}

	// One JSON document, exactly, on a single line (REQ: machine-readable-output).
	if n := bytes.Count(out.Bytes(), []byte("\n")); n != 1 {
		t.Errorf("stdout has %d newlines, want exactly 1 (one JSON document)", n)
	}
}

func TestWriteListJSON_ManagerField(t *testing.T) {
	row := Row{
		Entry: cliinstall.Entry{ID: "wb"},
		Status: cliinstall.Status{
			State: cliinstall.Installed, Method: selfupdate.Managed,
			Manager: &selfupdate.Manager{Name: "Homebrew"},
		},
	}
	var out, errOut bytes.Buffer
	if err := WriteListJSON(&out, &errOut, "specscore", []Row{row}); err != nil {
		t.Fatalf("WriteListJSON error = %v", err)
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

func TestWriteListJSON_EncodeError(t *testing.T) {
	err := WriteListJSON(failingWriter{}, &bytes.Buffer{}, "datatug", nil)
	if err == nil {
		t.Fatal("expected an error from a failing writer")
	}
}

// failingWriter always fails, exercising WriteListJSON/WriteResultJSON's own
// error-propagation branch.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("boom") }
