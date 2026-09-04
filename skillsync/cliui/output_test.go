package cliui

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/strongo/cli-helpers/skillsync"
	"strings"
	"testing"
)

type failAtWriter struct{ writes, fail int }

func (w *failAtWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.fail {
		return 0, errors.New("writer failed")
	}
	return len(p), nil
}

func TestWriteJSONOneReport(t *testing.T) {
	var out bytes.Buffer
	if err := WriteJSON(&out, []skillsync.Report{{Dir: "/skills"}}); err != nil {
		t.Fatal(err)
	}
	var report skillsync.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Dir != "/skills" {
		t.Fatalf("report = %#v", report)
	}
}
func TestWriteText(t *testing.T) {
	var out bytes.Buffer
	if err := WriteText(&out, skillsync.Report{Dir: "/skills", Changes: []skillsync.Change{{Name: "alpha", Action: skillsync.Added, Outcome: skillsync.Applied}}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "directory synced: /skills\n  added: alpha\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteTargetTextCallsDryRunPreviewAndDoesNotClaimRestoredSuccess(t *testing.T) {
	var out bytes.Buffer
	report := skillsync.Report{Dir: "/skills", DryRun: true, Changes: []skillsync.Change{
		{Name: "add", Action: skillsync.Added, Outcome: skillsync.Planned},
		{Name: "old", Action: skillsync.Updated, Outcome: skillsync.Restored},
		{Name: "broken", Action: skillsync.Removed, Outcome: skillsync.Incomplete},
		{Name: "same-a", Action: skillsync.Unchanged}, {Name: "same-b", Action: skillsync.Unchanged},
		{Name: "mine", Action: skillsync.Conflict},
	}}
	if err := WriteTargetText(&out, []TargetReport{{Harness: "codex", Dir: "/skills", Report: report, Err: errors.New("disk full")}}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"codex failed", "planned: add", "restored: old", "incomplete removal: broken", "unchanged: 2", "conflicts: mine", "failed: disk full"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from %q", want, got)
		}
	}
	if strings.Contains(got, "updated: old") || strings.Contains(got, "removed: broken") {
		t.Fatalf("misleading success output: %q", got)
	}
}

func TestWriteTargetTextCallsDryRunWithoutFailurePreview(t *testing.T) {
	var out bytes.Buffer
	if err := WriteTargetText(&out, []TargetReport{{Harness: "claude", Dir: "/skills", Report: skillsync.Report{DryRun: true}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "claude preview") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestWriteTargetJSONCarriesHarnessAndRuntimeError(t *testing.T) {
	var out bytes.Buffer
	if err := WriteTargetJSON(&out, []TargetReport{{Harness: "wb", Dir: "/skills", Report: skillsync.Report{Dir: "/skills"}, Err: errors.New("denied")}}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["harness"] != "wb" || got["error"] != "denied" {
		t.Fatalf("json = %#v", got)
	}
}

func TestRenderersPropagateEveryWriterBoundary(t *testing.T) {
	report := skillsync.Report{Dir: "/skills", Changes: []skillsync.Change{
		{Name: "a", Action: skillsync.Added, Outcome: skillsync.Applied}, {Name: "u", Action: skillsync.Updated, Outcome: skillsync.Applied},
		{Name: "r", Action: skillsync.Removed, Outcome: skillsync.Applied}, {Name: "x", Action: skillsync.Conflict}, {Name: "same", Action: skillsync.Unchanged},
	}}
	for n := 1; n <= 12; n++ {
		w := &failAtWriter{fail: n}
		if err := WriteTargetText(w, []TargetReport{{Dir: "/skills", Report: report, Err: errors.New("target")}}); err == nil {
			continue
		}
	}
	if err := WriteTargetJSON(&failAtWriter{fail: 1}, []TargetReport{{Dir: "/skills", Report: report}, {Dir: "/other", Report: report}}); err == nil {
		t.Fatal("expected JSON writer error")
	}
	for n := 1; n <= 8; n++ {
		_ = WriteText(&failAtWriter{fail: n}, report)
	}
}
