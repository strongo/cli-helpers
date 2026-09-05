package cliui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

func TestManagedCommandHelperProcess(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "--managed-command-helper" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	mode := os.Args[marker+1]
	argument := os.Args[marker+2]
	if mode == "path-version" {
		if strings.Contains(strings.ToLower(os.Args[0]), "caskroom") {
			_, _ = fmt.Fprintln(os.Stdout, argument)
		} else {
			_, _ = fmt.Fprintln(os.Stdout, "version 0.0.0")
		}
		return
	}
	if mode == "empty" {
		os.Exit(0)
	}
	_, _ = fmt.Fprintln(os.Stdout, "stdout:"+argument)
	_, _ = fmt.Fprintln(os.Stderr, "stderr:"+argument)
	if mode == "fail" {
		os.Exit(23)
	}
}

func TestManagedCommandRunnerStreamsAndPreservesArgv(t *testing.T) {
	var out, errOut bytes.Buffer
	runner := ManagedCommandRunner(strings.NewReader(""), &out, &errOut)
	err := runner(context.Background(), os.Args[0], []string{
		"-test.run=^TestManagedCommandHelperProcess$",
		"--", "--managed-command-helper", "ok", "argument with spaces; and punctuation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for stream, got := range map[string]string{"stdout": out.String(), "stderr": errOut.String()} {
		if !strings.Contains(got, stream+":argument with spaces; and punctuation") {
			t.Errorf("%s = %q, want the exact single argv value", stream, got)
		}
	}
}

func TestManagedCommandRunnerPreservesProcessExitCode(t *testing.T) {
	runner := ManagedCommandRunner(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	err := runner(context.Background(), os.Args[0], []string{
		"-test.run=^TestManagedCommandHelperProcess$",
		"--", "--managed-command-helper", "fail", "value",
	})
	if err == nil {
		t.Fatal("expected child-process failure")
	}
	var exitCoder interface{ ExitCode() int }
	if !errors.As(err, &exitCoder) {
		t.Fatalf("error %T does not preserve the process ExitCode method: %v", err, err)
	}
	if got := exitCoder.ExitCode(); got != 23 {
		t.Errorf("ExitCode() = %d, want 23", got)
	}
}

func TestVerifyManagedBinaryRunsConfiguredProbe(t *testing.T) {
	identity, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, os.Args[0], []string{
		"-test.run=^TestManagedCommandHelperProcess$",
		"--", "--managed-command-helper", "ok", "version 1.2.3",
	}, "1.2.3")
	if err != nil {
		t.Fatalf("unexpected probe error: %v", err)
	}
	if identity.Path == "" || identity.ResolvedPath == "" {
		t.Fatalf("verified identity = %+v, want absolute invocation and resolved paths", identity)
	}
}

func TestVerifyManagedBinaryFailures(t *testing.T) {
	t.Run("binary missing", func(t *testing.T) {
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, "selfupdate-binary-that-does-not-exist", []string{"--version"}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "locate updated") {
			t.Errorf("error = %v, want locate failure", err)
		}
	})

	t.Run("absolute binary missing", func(t *testing.T) {
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, filepath.Join(t.TempDir(), "missing-wb"), []string{"--version"}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "executable not found") {
			t.Errorf("error = %v, want absolute locate failure", err)
		}
	})

	t.Run("probe exits unsuccessfully", func(t *testing.T) {
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, os.Args[0], []string{
			"-test.run=^TestManagedCommandHelperProcess$",
			"--", "--managed-command-helper", "fail", "bad version",
		}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "probe updated") {
			t.Errorf("error = %v, want probe failure", err)
		}
	})

	t.Run("probe emits no version", func(t *testing.T) {
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, os.Args[0], []string{
			"-test.run=^TestManagedCommandHelperProcess$",
			"--", "--managed-command-helper", "empty", "ignored",
		}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "no version output") {
			t.Errorf("error = %v, want empty-output failure", err)
		}
	})

	t.Run("probe reports stale version", func(t *testing.T) {
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, os.Args[0], []string{
			"-test.run=^TestManagedCommandHelperProcess$",
			"--", "--managed-command-helper", "ok", "version 1.2.2",
		}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), `expected version "1.2.3"`) {
			t.Errorf("error = %v, want exact-version mismatch", err)
		}
	})
}

func TestVerifyManagedBinarySkipsEarlierUnmanagedPATHCandidate(t *testing.T) {
	unmanagedDir := filepath.Join(t.TempDir(), "development", "bin")
	managedDir := filepath.Join(t.TempDir(), "Caskroom", "wb", "1.2.3", "bin")
	binary := "wb-probe"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	for _, directory := range []string{unmanagedDir, managedDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, binary), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", strings.Join([]string{unmanagedDir, managedDir}, string(os.PathListSeparator)))
	manager := selfupdate.Homebrew("brew upgrade --cask wb")
	identity, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{
		Method: selfupdate.Managed, Manager: &manager, Path: filepath.Join(managedDir, binary),
	}, binary, []string{
		"-test.run=^TestManagedCommandHelperProcess$",
		"--", "--managed-command-helper", "path-version", "version 1.2.3",
	}, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(managedDir, binary)
	wantResolved, err := filepath.EvalSymlinks(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Path != wantPath || identity.ResolvedPath != wantResolved {
		t.Fatalf("verified identity = %+v, want manager-owned candidate %s", identity, filepath.Join(managedDir, binary))
	}
}

func TestVerifyManagedBinaryCandidateResolutionFailures(t *testing.T) {
	originalLookPath, originalEval, originalAbs, originalGetenv := managedLookPath, managedEvalSymlinks, managedAbsPath, managedGetenv
	t.Cleanup(func() {
		managedLookPath, managedEvalSymlinks, managedAbsPath, managedGetenv = originalLookPath, originalEval, originalAbs, originalGetenv
	})
	probeArgs := []string{
		"-test.run=^TestManagedCommandHelperProcess$",
		"--", "--managed-command-helper", "ok", "version 1.2.3",
	}

	t.Run("symlink resolution", func(t *testing.T) {
		managedEvalSymlinks = func(string) (string, error) { return "", errors.New("broken launcher") }
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, os.Args[0], probeArgs, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "broken launcher") {
			t.Fatalf("error = %v, want symlink resolution failure", err)
		}
		managedEvalSymlinks = originalEval
	})

	t.Run("absolute path", func(t *testing.T) {
		managedAbsPath = func(string) (string, error) { return "", errors.New("cannot make absolute") }
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, os.Args[0], probeArgs, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "cannot make absolute") {
			t.Fatalf("error = %v, want absolute path failure", err)
		}
		managedAbsPath = originalAbs
	})

	t.Run("no candidate belongs to manager", func(t *testing.T) {
		manager := selfupdate.Homebrew("brew upgrade --cask wb")
		_, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{Method: selfupdate.Managed, Manager: &manager}, os.Args[0], probeArgs, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "owned by Homebrew") || !strings.Contains(err.Error(), "no matching executable") {
			t.Fatalf("error = %v, want manager-owned candidate failure", err)
		}
	})

	t.Run("empty PATH entries search current directory once", func(t *testing.T) {
		binary := "current-wb"
		managedGetenv = func(string) string { return string(os.PathListSeparator) }
		managedLookPath = func(path string) (string, error) {
			if path == filepath.Join(".", binary) {
				return os.Args[0], nil
			}
			return "", errors.New("missing")
		}
		identity, err := VerifyManagedBinary(context.Background(), selfupdate.Detection{}, binary, probeArgs, "1.2.3")
		if err != nil || identity.Path == "" {
			t.Fatalf("identity/error = %+v/%v, want current-directory candidate", identity, err)
		}
	})
}
