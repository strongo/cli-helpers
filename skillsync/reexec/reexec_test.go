package reexec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunnerUsesStructuredArgsAndKeepsStdoutClean(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	path := filepath.Join(t.TempDir(), "new-tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err := Runner{Args: []string{"skills", "sync", "--format", "json"}, Stderr: &stderr, Timeout: time.Second}.Run(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stderr.String()); got != "skills sync --format json" {
		t.Fatalf("args = %q", got)
	}
}

func TestRunnerRejectsRelativeExecutable(t *testing.T) {
	err := Runner{}.Run(context.Background(), "tool")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err = %v", err)
	}
}
