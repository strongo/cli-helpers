package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/producer"
)

func TestReadDescriptorAndUsageErrors(t *testing.T) {
	if _, err := readDescriptor(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing descriptor error")
	}
	for _, raw := range []string{"{", `{"unknown":true}`, `{} {}`} {
		path := filepath.Join(t.TempDir(), "bundle.json")
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readDescriptor(path); err == nil {
			t.Fatalf("expected descriptor rejection for %q", raw)
		}
	}
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage error")
	}
	if err := run([]string{"--unknown"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected flag error")
	}
}

func TestRunBuildsSnapshot(t *testing.T) {
	repo := t.TempDir()
	command(t, repo, "init")
	command(t, repo, "config", "user.email", "test@example.com")
	command(t, repo, "config", "user.name", "Test")
	command(t, repo, "remote", "add", "origin", "https://github.com/example/plugin.git")
	if err := os.MkdirAll(filepath.Join(repo, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skills", "demo", "SKILL.md"), []byte("demo"), 0o644); err != nil {
		t.Fatal(err)
	}
	command(t, repo, "add", ".")
	command(t, repo, "commit", "-m", "skills")
	revision := strings.TrimSpace(command(t, repo, "rev-parse", "HEAD"))
	descriptor := skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "example", Name: "plugin"}, Source: skillsync.Source{Repository: "github.com/example/plugin", Path: "skills", Revision: revision, Version: "1.0.0"}}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	descriptorPath := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(descriptorPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	var stdout bytes.Buffer
	if err := run([]string{"--descriptor", descriptorPath, "--repo", repo, "--out", out}, &stdout); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"skillsync-bundle.tar", "skillsync-bundle.json", "embed"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(strings.Split(strings.TrimSpace(stdout.String()), "\n")) != 3 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestMainReportsFailure(t *testing.T) {
	originalArgs, originalExit, originalStderr := os.Args, exit, stderr
	defer func() { os.Args, exit, stderr = originalArgs, originalExit, originalStderr }()
	os.Args = []string{"skillsbundle"}
	var output bytes.Buffer
	stderr = &output
	code := 0
	exit = func(value int) { code = value }
	main()
	if code != 1 || !strings.Contains(output.String(), "usage:") {
		t.Fatalf("code=%d stderr=%q", code, output.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestRunReportsOutputWriteFailure(t *testing.T) {
	original := produce
	defer func() { produce = original }()
	produce = func(producer.Config) (producer.Result, error) {
		return producer.Result{ArchivePath: "archive", DescriptorPath: "descriptor", EmbedDir: "embed"}, nil
	}
	path := filepath.Join(t.TempDir(), "descriptor.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--descriptor", path, "--repo", "repo", "--out", "out"}, failingWriter{}); err == nil {
		t.Fatal("expected output failure")
	}
}

func TestRunReturnsProducerFailure(t *testing.T) {
	original := produce
	defer func() { produce = original }()
	produce = func(producer.Config) (producer.Result, error) { return producer.Result{}, os.ErrPermission }
	path := filepath.Join(t.TempDir(), "descriptor.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--descriptor", path, "--repo", "repo", "--out", "out"}, &bytes.Buffer{}); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("error=%v", err)
	}
}

func TestRunReturnsDescriptorReadFailure(t *testing.T) {
	original := produce
	defer func() { produce = original }()
	if err := run([]string{"--descriptor", filepath.Join(t.TempDir(), "missing"), "--repo", "repo", "--out", "out"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected descriptor read failure")
	}
}

func command(t *testing.T, directory string, args ...string) string {
	t.Helper()
	raw, err := exec.Command("git", append([]string{"-C", directory}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, raw)
	}
	return string(raw)
}
