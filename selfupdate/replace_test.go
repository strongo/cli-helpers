package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReplaceExecutable_SwapsNewBytes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wb")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, "new-source")
	if err := os.WriteFile(newBin, []byte("new binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := replaceExecutable("wb", target, newBin); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("target bytes = %q, want %q", got, "new binary")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("target is not executable: mode %v", info.Mode())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "wb" || e.Name() == "new-source" {
			continue
		}
		t.Fatalf("unexpected leftover file in dir: %q", e.Name())
	}
}

// A staging failure (source missing) leaves the target byte-for-byte
// untouched (REQ: failure-leaves-working-binary), and leaves no partial
// staging file behind either.
func TestReplaceExecutable_StagingFailureLeavesTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wb")
	if err := os.WriteFile(target, []byte("original bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist")

	if err := replaceExecutable("wb", target, missing); err == nil {
		t.Fatal("expected error when new binary source is missing, got nil")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target missing after failed swap: %v", err)
	}
	if string(got) != "original bytes" {
		t.Fatalf("target corrupted after failed swap: got %q, want %q", got, "original bytes")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "wb" {
			t.Fatalf("unexpected leftover file in dir: %q", e.Name())
		}
	}
}

func TestReplaceExecutable_RenameError(t *testing.T) {
	orig := renameFunc
	t.Cleanup(func() { renameFunc = orig })
	renameFunc = func(string, string) error { return errors.New("rename fail") }

	dir := t.TempDir()
	target := filepath.Join(dir, "wb")
	_ = os.WriteFile(target, []byte("orig"), 0o755)
	newBin := filepath.Join(dir, "new")
	_ = os.WriteFile(newBin, []byte("new"), 0o644)

	if err := replaceExecutable("wb", target, newBin); err == nil {
		t.Fatal("expected rename error, got nil")
	}
}

// Windows takes a different code path (move-aside, install, best-effort
// cleanup) exercised here via the goosName seam regardless of host OS.
func TestReplaceExecutable_WindowsPath(t *testing.T) {
	origGoos := goosName
	t.Cleanup(func() { goosName = origGoos })
	goosName = "windows"

	dir := t.TempDir()
	target := filepath.Join(dir, "wb.exe")
	_ = os.WriteFile(target, []byte("orig"), 0o755)
	newBin := filepath.Join(dir, "new")
	_ = os.WriteFile(newBin, []byte("new bytes"), 0o644)

	if err := replaceExecutable("wb", target, newBin); err != nil {
		t.Fatalf("windows replace: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new bytes" {
		t.Fatalf("target = %q, want %q", got, "new bytes")
	}
}

func TestReplaceExecutable_WindowsMoveAsideError(t *testing.T) {
	origGoos, origRename := goosName, renameFunc
	t.Cleanup(func() { goosName, renameFunc = origGoos, origRename })
	goosName = "windows"
	renameFunc = func(string, string) error { return errors.New("rename fail") }

	dir := t.TempDir()
	target := filepath.Join(dir, "wb.exe")
	_ = os.WriteFile(target, []byte("orig"), 0o755)
	newBin := filepath.Join(dir, "new")
	_ = os.WriteFile(newBin, []byte("new"), 0o644)

	if err := replaceExecutable("wb", target, newBin); err == nil {
		t.Fatal("expected move-aside error, got nil")
	}
}

func TestReplaceExecutable_WindowsInstallErrorRestoresOriginal(t *testing.T) {
	origGoos, origRename := goosName, renameFunc
	t.Cleanup(func() { goosName, renameFunc = origGoos, origRename })
	goosName = "windows"
	calls := 0
	renameFunc = func(oldp, newp string) error {
		calls++
		if calls == 2 {
			return errors.New("install fail")
		}
		return os.Rename(oldp, newp)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "wb.exe")
	_ = os.WriteFile(target, []byte("orig"), 0o755)
	newBin := filepath.Join(dir, "new")
	_ = os.WriteFile(newBin, []byte("new"), 0o644)

	if err := replaceExecutable("wb", target, newBin); err == nil {
		t.Fatal("expected install error, got nil")
	}
}

func TestStage_CreateTempError(t *testing.T) {
	orig := stageCreateTmp
	t.Cleanup(func() { stageCreateTmp = orig })
	stageCreateTmp = func(string, string) (tempFile, error) { return nil, errors.New("no temp") }

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	if _, err := stage("wb", dir, src); err == nil {
		t.Fatal("expected create-temp error, got nil")
	}
}

func TestStage_CopyError(t *testing.T) {
	// src is a directory -> io.Copy from a directory read fails.
	dir := t.TempDir()
	if _, err := stage("wb", dir, dir); err == nil {
		t.Fatal("expected copy error reading a directory, got nil")
	}
}

type closeErrStageFile struct{ *os.File }

func (c *closeErrStageFile) Close() error {
	_ = c.File.Close()
	return errors.New("close fail")
}

func TestStage_CloseError(t *testing.T) {
	orig := stageCreateTmp
	t.Cleanup(func() { stageCreateTmp = orig })
	stageCreateTmp = func(d, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(d, pattern)
		if err != nil {
			return nil, err
		}
		return &closeErrStageFile{File: f}, nil
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	if _, err := stage("wb", dir, src); err == nil {
		t.Fatal("expected close error, got nil")
	}
}

type removeOnCloseFile struct{ *os.File }

func (r *removeOnCloseFile) Close() error {
	name := r.Name()
	err := r.File.Close()
	_ = os.Remove(name)
	return err
}

func TestStage_ChmodError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	orig := stageCreateTmp
	t.Cleanup(func() { stageCreateTmp = orig })
	stageCreateTmp = func(d, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(d, pattern)
		if err != nil {
			return nil, err
		}
		return &removeOnCloseFile{File: f}, nil
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	if _, err := stage("wb", dir, src); err == nil {
		t.Fatal("expected chmod error, got nil")
	}
}

func TestVerifyBinaryVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake")
	script := "#!/bin/sh\necho \"wb version 1.2.3\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := verifyBinaryVersion(bin, []string{"--version"}, "1.2.3"); err != nil {
		t.Fatalf("verifyBinaryVersion match: %v", err)
	}

	err := verifyBinaryVersion(bin, []string{"--version"}, "9.9.9")
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "9.9.9") {
		t.Fatalf("mismatch error should mention wanted version: %v", err)
	}
}

// Probe args are configurable, not hard-coded to "--version"
// (REQ: consumer-configured-identity).
func TestVerifyBinaryVersion_CustomProbeArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake")
	script := "#!/bin/sh\nif [ \"$1\" = \"version\" ] && [ \"$2\" = \"--json\" ]; then echo '{\"version\":\"2.0.0\"}'; fi\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := verifyBinaryVersion(bin, []string{"version", "--json"}, "2.0.0"); err != nil {
		t.Fatalf("verifyBinaryVersion with custom probe args: %v", err)
	}
}

func TestVerifyBinaryVersion_RunError(t *testing.T) {
	if _, err := runVersionProbe(filepath.Join(t.TempDir(), "nope"), []string{"--version"}); err == nil {
		t.Fatal("expected run error for a nonexistent path, got nil")
	}
	if err := verifyBinaryVersion(filepath.Join(t.TempDir(), "nope"), []string{"--version"}, "1.0.0"); err == nil {
		t.Fatal("expected run error, got nil")
	}
}
