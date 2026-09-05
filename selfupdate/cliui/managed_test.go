package cliui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
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
	err := VerifyManagedBinary(context.Background(), os.Args[0], []string{
		"-test.run=^TestManagedCommandHelperProcess$",
		"--", "--managed-command-helper", "ok", "version 1.2.3",
	}, "1.2.3")
	if err != nil {
		t.Fatalf("unexpected probe error: %v", err)
	}
}

func TestVerifyManagedBinaryFailures(t *testing.T) {
	t.Run("binary missing", func(t *testing.T) {
		err := VerifyManagedBinary(context.Background(), "selfupdate-binary-that-does-not-exist", []string{"--version"}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "locate updated") {
			t.Errorf("error = %v, want locate failure", err)
		}
	})

	t.Run("probe exits unsuccessfully", func(t *testing.T) {
		err := VerifyManagedBinary(context.Background(), os.Args[0], []string{
			"-test.run=^TestManagedCommandHelperProcess$",
			"--", "--managed-command-helper", "fail", "bad version",
		}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "probe updated") {
			t.Errorf("error = %v, want probe failure", err)
		}
	})

	t.Run("probe emits no version", func(t *testing.T) {
		err := VerifyManagedBinary(context.Background(), os.Args[0], []string{
			"-test.run=^TestManagedCommandHelperProcess$",
			"--", "--managed-command-helper", "empty", "ignored",
		}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), "no version output") {
			t.Errorf("error = %v, want empty-output failure", err)
		}
	})

	t.Run("probe reports stale version", func(t *testing.T) {
		err := VerifyManagedBinary(context.Background(), os.Args[0], []string{
			"-test.run=^TestManagedCommandHelperProcess$",
			"--", "--managed-command-helper", "ok", "version 1.2.2",
		}, "1.2.3")
		if err == nil || !strings.Contains(err.Error(), `expected version "1.2.3"`) {
			t.Errorf("error = %v, want exact-version mismatch", err)
		}
	})
}
