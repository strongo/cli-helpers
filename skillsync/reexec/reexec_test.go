package reexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const childModeEnvironment = "STRONGO_SKILLSYNC_REEXEC_CHILD"

func TestMain(m *testing.M) {
	if mode := os.Getenv(childModeEnvironment); mode != "" {
		switch mode {
		case "arguments":
			_ = json.NewEncoder(os.Stdout).Encode(os.Args[1:])
		case "stderr":
			_, _ = os.Stderr.WriteString("child stderr\n")
		case "failure":
			os.Exit(23)
		case "wait":
			time.Sleep(10 * time.Second)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunnerUsesExactStructuredArgsAndKeepsStdoutClean(t *testing.T) {
	t.Setenv(childModeEnvironment, "arguments")
	var stderr bytes.Buffer
	args := []string{"skills", "sync", "--dir", "a path/$HOME;*", "", "--format=json"}
	err := Runner{Args: args, Stderr: &stderr, Timeout: 10 * time.Second}.Run(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(stderr.Bytes(), &got); err != nil {
		t.Fatalf("child output is not JSON: %v", err)
	}
	if len(got) != len(args) {
		t.Fatalf("args = %#v, want %#v", got, args)
	}
	for i := range args {
		if got[i] != args[i] {
			t.Fatalf("args = %#v, want %#v", got, args)
		}
	}
}

func TestRunnerUsesDefaultArgsAndRoutesChildStderr(t *testing.T) {
	t.Setenv(childModeEnvironment, "arguments")
	var stderr bytes.Buffer
	if err := (Runner{Stderr: &stderr}).Run(context.Background(), os.Args[0]); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(stderr.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := []string{"skills", "sync"}; !equalArgs(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}

	t.Setenv(childModeEnvironment, "stderr")
	stderr.Reset()
	if err := (Runner{Stderr: &stderr}).Run(context.Background(), os.Args[0]); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != "child stderr\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunnerValidationFailuresPreserveFilesystemCause(t *testing.T) {
	err := Runner{}.Run(context.Background(), "tool")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err = %v", err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	err = Runner{}.Run(context.Background(), missing)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing error = %v; want fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), `arguments ["skills" "sync"]`) {
		t.Fatalf("missing executable retry omits effective default arguments: %v", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("missing error = %T; want *fs.PathError", err)
	}

	err = Runner{}.Run(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}

	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Runner{}.Run(context.Background(), nonExecutable)
	if err == nil {
		t.Fatal("non-executable file ran successfully")
	}
	if !strings.Contains(err.Error(), "retry with direct execution") {
		t.Fatalf("non-executable error does not offer retry: %v", err)
	}
}

func TestRunnerPreservesExitAndContextFailures(t *testing.T) {
	t.Setenv(childModeEnvironment, "failure")
	err := Runner{}.Run(context.Background(), os.Args[0])
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("exit error = %v; want exit status 23", err)
	}
	if !strings.Contains(err.Error(), "arguments [\"skills\" \"sync\"]") {
		t.Fatalf("exit error does not name retry argv: %v", err)
	}

	t.Setenv(childModeEnvironment, "wait")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Runner{Timeout: time.Second}.Run(ctx, os.Args[0])
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v; want context canceled", err)
	}

	err = Runner{Timeout: 100 * time.Millisecond}.Run(context.Background(), os.Args[0])
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v; want deadline exceeded", err)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
