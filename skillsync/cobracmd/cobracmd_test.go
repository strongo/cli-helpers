package cobracmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/strongo/cli-helpers/skillsync"
)

func commandConfig(t *testing.T) skillsync.Config {
	t.Helper()
	content := fstest.MapFS{"tool-install/SKILL.md": &fstest.MapFile{Data: []byte("skill")}}
	digest, err := skillsync.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	b, err := skillsync.EmbeddedBundle(skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "strongo", Name: "tool-plugin"}, Source: skillsync.Source{Repository: "github.com/strongo/tool-plugin", Path: "skills", Revision: "0123456789012345678901234567890123456789", Version: "1.0.0", Digest: digest}}, content)
	if err != nil {
		t.Fatal(err)
	}
	return skillsync.Config{CLI: skillsync.Identity{Publisher: "strongo", Name: "tool"}, CurrentVersion: "1.0.0", Bundles: []skillsync.Bundle{b}}
}
func execute(t *testing.T, cmdArgs ...string) (string, string, error) {
	t.Helper()
	cmd := New(commandConfig(t), CommandOptions{})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(cmdArgs)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func TestNewJSONUsesOnlyJSONOnStdout(t *testing.T) {
	dir := t.TempDir()
	out, errOut, err := execute(t, "sync", "--dir", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q", errOut)
	}
	var report skillsync.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not JSON: %q: %v", out, err)
	}
	if report.Dir != dir {
		t.Fatalf("dir = %q", report.Dir)
	}
}
func TestNewRejectsDirWithHarnessBeforeInstalling(t *testing.T) {
	dir := t.TempDir()
	_, _, err := execute(t, "sync", "--dir", dir, "--harness", "codex")
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, statErr := filepath.Glob(filepath.Join(dir, "*")); statErr != nil {
		t.Fatal(statErr)
	}
}
func TestNewDryRunDoesNotCreateTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	out, _, err := execute(t, "sync", "--dir", dir, "--dry-run", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report skillsync.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) == 0 {
		t.Fatal("no planned changes")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run target stat = %v", err)
	}
}

func TestNewRejectsUnknownFormat(t *testing.T) {
	_, _, err := execute(t, "sync", "--dir", t.TempDir(), "--format", "yaml")
	if err == nil {
		t.Fatal("expected invalid format error")
	}
}
