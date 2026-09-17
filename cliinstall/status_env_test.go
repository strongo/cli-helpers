package cliinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultEnv_AllFieldsSet(t *testing.T) {
	env := DefaultEnv()
	if env.PathDirs == nil || env.HostDir == nil || env.IsExecutable == nil || env.EvalSymlinks == nil || env.Run == nil {
		t.Fatalf("DefaultEnv() left a field nil: %+v", env)
	}
	// Sanity: none of these should error or panic against the real host.
	_ = env.PathDirs()
	if _, err := env.HostDir(); err != nil {
		t.Errorf("HostDir() error = %v", err)
	}
}

func TestDefaultPathDirs_FiltersRelativeAndEmpty(t *testing.T) {
	orig, had := os.LookupEnv("PATH")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("PATH", orig)
		} else {
			_ = os.Unsetenv("PATH")
		}
	})

	// defaultPathDirs filters on the REAL path/filepath.IsAbs, which follows
	// the build platform, not an injected goos (unlike this package's own
	// goos-parameterized isAbsPath in destination.go) — so this fixture
	// must supply absolute paths shaped for whatever platform actually
	// runs the test, or the POSIX-only fixture this test used to hardcode
	// filters out every entry and fails on a real Windows CI job (task-5
	// review M12).
	absA, absB := "/usr/bin", "/opt/bin"
	if runtime.GOOS == "windows" {
		absA, absB = `C:\usr\bin`, `C:\opt\bin`
	}

	sep := string(os.PathListSeparator)
	_ = os.Setenv("PATH", strings.Join([]string{absA, "relative/dir", "", absB}, sep))

	got := defaultPathDirs()
	want := []string{absA, absB}
	if len(got) != len(want) {
		t.Fatalf("defaultPathDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("defaultPathDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultHostDir_Success(t *testing.T) {
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	osExecutable = func() (string, error) { return "/opt/app/bin/mycli", nil }
	evalSymlinksFunc = func(p string) (string, error) { return p, nil }

	dir, err := defaultHostDir()
	if err != nil {
		t.Fatalf("defaultHostDir() error = %v", err)
	}
	if dir != "/opt/app/bin" {
		t.Errorf("defaultHostDir() = %q, want /opt/app/bin", dir)
	}
}

func TestDefaultHostDir_ExecutableError(t *testing.T) {
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	osExecutable = func() (string, error) { return "", errors.New("boom") }

	_, err := defaultHostDir()
	if err == nil {
		t.Fatal("defaultHostDir() error = nil, want an error")
	}
}

func TestDefaultHostDir_SymlinkResolutionFailsFallsBackToUnresolved(t *testing.T) {
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	osExecutable = func() (string, error) { return "/opt/app/bin/mycli", nil }
	evalSymlinksFunc = func(string) (string, error) { return "", errors.New("cannot resolve") }

	dir, err := defaultHostDir()
	if err != nil {
		t.Fatalf("defaultHostDir() error = %v, want nil (falls back to unresolved)", err)
	}
	if dir != "/opt/app/bin" {
		t.Errorf("defaultHostDir() = %q, want /opt/app/bin", dir)
	}
}

func TestDefaultIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits only")
	}
	dir := t.TempDir()

	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	missing := filepath.Join(dir, "missing")
	if defaultIsExecutable(missing) {
		t.Error("missing file: want false")
	}

	subdir := filepath.Join(dir, "adir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if defaultIsExecutable(subdir) {
		t.Error("directory: want false")
	}

	nonExec := filepath.Join(dir, "nonexec")
	if err := os.WriteFile(nonExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if defaultIsExecutable(nonExec) {
		t.Error("non-executable regular file: want false")
	}

	exec := filepath.Join(dir, "exec")
	if err := os.WriteFile(exec, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !defaultIsExecutable(exec) {
		t.Error("executable regular file: want true")
	}
}

func TestDefaultIsExecutable_Windows(t *testing.T) {
	dir := t.TempDir()
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "windows"

	if defaultIsExecutable(filepath.Join(dir, "missing.exe")) {
		t.Error("missing file on windows: want false")
	}

	subdir := filepath.Join(dir, "adir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if defaultIsExecutable(subdir) {
		t.Error("directory on windows: want false")
	}

	f := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !defaultIsExecutable(f) {
		t.Error("regular file on windows: want true (no exec bit concept)")
	}
}

func TestDefaultRun_CombinesOutputAndSetsNoColor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are POSIX-only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "probe")
	body := "#!/bin/sh\necho \"out:$NO_COLOR\"\necho err-line 1>&2\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := defaultRun(context.Background(), script, nil)
	if err != nil {
		t.Fatalf("defaultRun() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "out:1") {
		t.Errorf("output = %q, want NO_COLOR=1 visible to the child", text)
	}
	if !strings.Contains(text, "err-line") {
		t.Errorf("output = %q, want stderr combined into the result", text)
	}
}

// M13: probing a target executes any binary named like a catalog id, so a
// caller's own GH_TOKEN/GITHUB_TOKEN must never reach it.
func TestProbeEnv_StripsTokenVars(t *testing.T) {
	in := []string{"PATH=/bin", "GH_TOKEN=secret1", "GITHUB_TOKEN=secret2", "HOME=/home/alex", "GH_TOKEN_NOT_QUITE=keepme"}
	got := probeEnv(in)
	want := []string{"PATH=/bin", "HOME=/home/alex", "GH_TOKEN_NOT_QUITE=keepme"}
	if len(got) != len(want) {
		t.Fatalf("probeEnv() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("probeEnv()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultRun_StripsTokenVarsFromChildEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are POSIX-only")
	}
	origGH, hadGH := os.LookupEnv("GH_TOKEN")
	origGithub, hadGithub := os.LookupEnv("GITHUB_TOKEN")
	t.Cleanup(func() {
		if hadGH {
			_ = os.Setenv("GH_TOKEN", origGH)
		} else {
			_ = os.Unsetenv("GH_TOKEN")
		}
		if hadGithub {
			_ = os.Setenv("GITHUB_TOKEN", origGithub)
		} else {
			_ = os.Unsetenv("GITHUB_TOKEN")
		}
	})
	_ = os.Setenv("GH_TOKEN", "leaked-gh-token")
	_ = os.Setenv("GITHUB_TOKEN", "leaked-github-token")

	dir := t.TempDir()
	script := filepath.Join(dir, "probe")
	body := "#!/bin/sh\necho \"gh:[$GH_TOKEN] github:[$GITHUB_TOKEN]\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := defaultRun(context.Background(), script, nil)
	if err != nil {
		t.Fatalf("defaultRun() error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, "leaked") {
		t.Errorf("child process saw a token var: %q", text)
	}
	if !strings.Contains(text, "gh:[] github:[]") {
		t.Errorf("output = %q, want both token vars empty in the child", text)
	}
}

func TestDefaultRun_KillsOnContextDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are POSIX-only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hang")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _ = defaultRun(ctx, script, nil)
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("defaultRun took %s, want the process killed well under 3s", elapsed)
	}
}
