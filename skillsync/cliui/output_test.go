package cliui

import (
	"bytes"
	"encoding/json"
	"github.com/strongo/cli-helpers/skillsync"
	"testing"
)

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
	if err := WriteText(&out, skillsync.Report{Dir: "/skills", Changes: []skillsync.Change{{Name: "alpha", Action: skillsync.Added}}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "skills synced /skills\n  added: alpha\n" {
		t.Fatalf("got %q", got)
	}
}
