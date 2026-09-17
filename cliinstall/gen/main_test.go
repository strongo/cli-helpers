package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// chdir changes the working directory to dir and restores the original on
// test cleanup.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	})
}

// stubRunGH installs fn as runGH for the duration of the test.
func stubRunGH(t *testing.T, fn func(args ...string) ([]byte, error)) {
	t.Helper()
	orig := runGH
	runGH = fn
	t.Cleanup(func() { runGH = orig })
}

// mkSnapshotsDir creates <dir>/testdata/snapshots/{releases,casks} and
// returns the snapshots directory path.
func mkSnapshotsDir(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, "testdata", "snapshots")
	for _, sub := range []string{"releases", "casks"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fakeReleaseView is a canned `gh release view ... --json tagName,assets`
// response used by every success-path test below.
const fakeReleaseView = `{"tagName":"v1.0.0","assets":[{"name":"foo_1.0.0_linux_amd64.tar.gz"},{"name":"foo_1.0.0_checksums.txt"}]}`

// fakeReleaseList is a canned `gh release list ... --json tagName` response
// containing both a "cli-" and an unrelated "servers-" tag, matching every
// tag-prefixed target this package actually declares.
const fakeReleaseList = `[{"tagName":"servers-v9.0.0"},{"tagName":"cli-v1.0.0"}]`

// succeedGH answers every recognized gh invocation shape with canned data;
// it is what main()'s success-path tests wire up so the real, unmodified
// `targets` list (every catalog CLI, cask and non-cask, prefixed and not)
// is exercised end to end without any network access.
func succeedGH(args ...string) ([]byte, error) {
	switch {
	case len(args) >= 2 && args[0] == "release" && args[1] == "list":
		return []byte(fakeReleaseList), nil
	case len(args) >= 2 && args[0] == "release" && args[1] == "view":
		return []byte(fakeReleaseView), nil
	case len(args) >= 1 && args[0] == "api":
		return []byte("aGVsbG8="), nil // base64 "hello"
	}
	return nil, fmt.Errorf("succeedGH: unexpected args %v", args)
}

// ---------- run ----------

func TestRun_MissingSnapshotsDir(t *testing.T) {
	if err := run(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("run() with a missing snapshots dir = nil error, want one")
	}
}

func TestRun_Success(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, succeedGH)

	if err := run(root); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	for _, want := range []string{"wb", "cover100", "synchestra"} {
		if _, err := os.Stat(filepath.Join(root, "releases", want+".json")); err != nil {
			t.Errorf("release snapshot for %s not written: %v", want, err)
		}
	}
	for _, want := range []string{"wb", "specscore"} {
		if _, err := os.Stat(filepath.Join(root, "casks", want+".rb")); err != nil {
			t.Errorf("cask snapshot for %s not written: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "casks", "cover100.rb")); err == nil {
		t.Error("cover100 (no cask) unexpectedly got a cask snapshot")
	}
}

func TestRun_RecordReleaseErrorPropagates(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) {
		return nil, errors.New("boom")
	})
	err := run(root)
	if err == nil || !strings.Contains(err.Error(), "release snapshot") {
		t.Fatalf("run() = %v, want an error mentioning \"release snapshot\"", err)
	}
}

func TestRun_RecordCaskErrorPropagates(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "api" {
			return nil, errors.New("boom")
		}
		return succeedGH(args...)
	})
	err := run(root)
	if err == nil || !strings.Contains(err.Error(), "cask snapshot") {
		t.Fatalf("run() = %v, want an error mentioning \"cask snapshot\"", err)
	}
}

// ---------- recordRelease ----------

func TestRecordRelease_LatestTagError(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) { return nil, errors.New("boom") })
	err := recordRelease(root, target{id: "x", repo: "o/r", tagPrefix: "cli-"})
	if err == nil {
		t.Fatal("recordRelease() = nil error, want the propagated latestTag error")
	}
}

func TestRecordRelease_RunGHError(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) { return nil, errors.New("boom") })
	if err := recordRelease(root, target{id: "x", repo: "o/r"}); err == nil {
		t.Fatal("recordRelease() = nil error, want one")
	}
}

func TestRecordRelease_MalformedJSON(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) { return []byte("not json"), nil })
	err := recordRelease(root, target{id: "x", repo: "o/r"})
	if err == nil || !strings.Contains(err.Error(), "decode gh output") {
		t.Fatalf("recordRelease() = %v, want a decode error", err)
	}
}

func TestRecordRelease_Success(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, succeedGH)
	if err := recordRelease(root, target{id: "x", repo: "o/r", tagPrefix: "cli-"}); err != nil {
		t.Fatalf("recordRelease() = %v, want nil", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "releases", "x.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": "1.0.0"`) {
		t.Errorf("recorded snapshot = %s, want version 1.0.0", data)
	}
}

// ---------- latestTag ----------

func TestLatestTag_NoPrefix(t *testing.T) {
	tag, err := latestTag(target{id: "x", repo: "o/r"})
	if err != nil || tag != "" {
		t.Fatalf("latestTag() = (%q, %v), want (\"\", nil)", tag, err)
	}
}

func TestLatestTag_RunGHError(t *testing.T) {
	stubRunGH(t, func(args ...string) ([]byte, error) { return nil, errors.New("boom") })
	if _, err := latestTag(target{id: "x", repo: "o/r", tagPrefix: "cli-"}); err == nil {
		t.Fatal("latestTag() = nil error, want one")
	}
}

func TestLatestTag_MalformedJSON(t *testing.T) {
	stubRunGH(t, func(args ...string) ([]byte, error) { return []byte("not json"), nil })
	_, err := latestTag(target{id: "x", repo: "o/r", tagPrefix: "cli-"})
	if err == nil || !strings.Contains(err.Error(), "decode gh release list") {
		t.Fatalf("latestTag() = %v, want a decode error", err)
	}
}

func TestLatestTag_NoMatch(t *testing.T) {
	stubRunGH(t, func(args ...string) ([]byte, error) { return []byte(fakeReleaseList), nil })
	_, err := latestTag(target{id: "x", repo: "o/r", tagPrefix: "nomatch-"})
	if err == nil || !strings.Contains(err.Error(), "no release tag with prefix") {
		t.Fatalf("latestTag() = %v, want a not-found error", err)
	}
}

func TestLatestTag_Match(t *testing.T) {
	stubRunGH(t, func(args ...string) ([]byte, error) { return []byte(fakeReleaseList), nil })
	tag, err := latestTag(target{id: "x", repo: "o/r", tagPrefix: "cli-"})
	if err != nil || tag != "cli-v1.0.0" {
		t.Fatalf("latestTag() = (%q, %v), want (\"cli-v1.0.0\", nil)", tag, err)
	}
}

// ---------- recordCask ----------

func TestRecordCask_RunGHError(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) { return nil, errors.New("boom") })
	if err := recordCask(root, target{id: "x", caskRepo: "o/r", caskPath: "Casks/x.rb"}); err == nil {
		t.Fatal("recordCask() = nil error, want one")
	}
}

func TestRecordCask_MalformedBase64(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, func(args ...string) ([]byte, error) { return []byte("not-base64!!"), nil })
	err := recordCask(root, target{id: "x", caskRepo: "o/r", caskPath: "Casks/x.rb"})
	if err == nil || !strings.Contains(err.Error(), "decode cask content") {
		t.Fatalf("recordCask() = %v, want a decode error", err)
	}
}

func TestRecordCask_Success(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	stubRunGH(t, succeedGH)
	if err := recordCask(root, target{id: "x", caskRepo: "o/r", caskPath: "Casks/x.rb"}); err != nil {
		t.Fatalf("recordCask() = %v, want nil", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "casks", "x.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("recorded cask = %q, want %q", data, "hello")
	}
}

// ---------- execGH (the real subprocess wiring, not runGH's stub) ----------

// withFakeGH prepends a directory holding a fake "gh" executable to PATH
// for the duration of the test, so execGH's real exec.Command wiring can
// be exercised against deterministic local output instead of the network.
func withFakeGH(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh script is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })
}

func TestExecGH_Success(t *testing.T) {
	withFakeGH(t, `echo -n "hello world"`)
	out, err := execGH("release", "view")
	if err != nil {
		t.Fatalf("execGH() = %v, want nil", err)
	}
	if string(out) != "hello world" {
		t.Errorf("execGH() = %q, want %q", out, "hello world")
	}
}

func TestExecGH_Failure(t *testing.T) {
	withFakeGH(t, `echo "boom" >&2; exit 1`)
	_, err := execGH("release", "view")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("execGH() = %v, want an error mentioning \"boom\"", err)
	}
}

// ---------- main ----------

func TestMain_Success(t *testing.T) {
	root := mkSnapshotsDir(t, t.TempDir())
	chdir(t, filepath.Dir(filepath.Dir(root))) // cwd = the temp dir that owns testdata/snapshots
	stubRunGH(t, succeedGH)

	exited := false
	origExit := exitFunc
	exitFunc = func(int) { exited = true }
	t.Cleanup(func() { exitFunc = origExit })

	main()

	if exited {
		t.Error("main() called exitFunc on a fully successful run")
	}
	if _, err := os.Stat(filepath.Join(root, "releases", "wb.json")); err != nil {
		t.Errorf("main() did not record wb's release snapshot: %v", err)
	}
}

func TestMain_Failure(t *testing.T) {
	chdir(t, t.TempDir()) // no testdata/snapshots here: run() fails immediately

	var gotCode int
	origExit := exitFunc
	exitFunc = func(code int) { gotCode = code }
	t.Cleanup(func() { exitFunc = origExit })

	main()

	if gotCode != 1 {
		t.Errorf("main() called exitFunc(%d), want exitFunc(1)", gotCode)
	}
}
