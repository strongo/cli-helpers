package cobracmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

type testUninstallErrorMapper struct {
	lastErr error
}

func (m *testUninstallErrorMapper) Failure(err error) error {
	m.lastErr = err
	return err
}

func TestNewUninstall_FlagsAndHelp(t *testing.T) {
	cmd := NewUninstall(UninstallCommandOptions{HostID: "wb"})

	if cmd.Use != "uninstall [name...]" {
		t.Errorf("cmd.Use = %q, want 'uninstall [name...]'", cmd.Use)
	}

	for _, flag := range []string{"all", "dry-run", "yes", "purge", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("expected flag --%s to be defined", flag)
		}
	}
}

func TestNewUninstall_UsageErrors(t *testing.T) {
	mapper := &testUninstallErrorMapper{}
	newCmd := func() *UninstallCommandOptions {
		return &UninstallCommandOptions{
			HostID: "wb",
			Errors: mapper,
		}
	}

	// Test 1: No args and no --all
	cmd := NewUninstall(*newCmd())
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error with no arguments and no --all")
	}
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected UsageError, got: %T", err)
	}

	// Test 2: --all combined with target args
	cmd = NewUninstall(*newCmd())
	cmd.SetArgs([]string{"--all", "specscore"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error with --all and positional args")
	}
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected UsageError, got: %T", err)
	}

	// Test 3: Invalid --format
	cmd = NewUninstall(*newCmd())
	cmd.SetArgs([]string{"--all", "--format", "invalid"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error with invalid --format")
	}
	if !errors.As(err, &usageErr) {
		t.Fatalf("expected UsageError, got: %T", err)
	}
}

func TestNewUninstall_DryRunAndExecution(t *testing.T) {
	tempDir := t.TempDir()
	specscorePath := filepath.Join(tempDir, "specscore")
	if err := os.WriteFile(specscorePath, []byte("#!/bin/sh\necho 'specscore 1.0.0'"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeEnv := cliinstall.InstallEnv{
		Env: cliinstall.Env{
			PathDirs:     func() []string { return []string{tempDir} },
			HostDir:      func() (string, error) { return tempDir, nil },
			IsExecutable: func(p string) bool { return p == specscorePath },
			EvalSymlinks: filepath.EvalSymlinks,
			Run: func(ctx context.Context, p string, args []string) ([]byte, error) {
				return []byte(`{"name":"specscore","version":"1.0.0","commit":"abcdef"}`), nil
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	mapper := &testUninstallErrorMapper{}
	newCmd := func() *UninstallCommandOptions {
		return &UninstallCommandOptions{
			HostID: "wb",
			Env:    fakeEnv,
			Errors: mapper,
		}
	}

	var out bytes.Buffer

	// 1. Dry Run
	cmd := NewUninstall(*newCmd())
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"specscore", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	if !strings.Contains(out.String(), "Would remove specscore") {
		t.Errorf("output = %q, want to contain 'Would remove specscore'", out.String())
	}
	if _, err := os.Stat(specscorePath); os.IsNotExist(err) {
		t.Fatal("file was deleted during --dry-run")
	}

	// 2. Real execution with -y
	out.Reset()
	cmd = NewUninstall(*newCmd())
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"specscore", "-y"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("real execution with -y failed: %v", err)
	}
	if !strings.Contains(out.String(), "Uninstalled specscore") {
		t.Errorf("output = %q, want to contain 'Uninstalled specscore'", out.String())
	}
	if _, err := os.Stat(specscorePath); !os.IsNotExist(err) {
		t.Errorf("file still exists after uninstall: %s", specscorePath)
	}
}

func TestNewUninstall_JSONOutput(t *testing.T) {
	tempDir := t.TempDir()
	specscorePath := filepath.Join(tempDir, "specscore")
	if err := os.WriteFile(specscorePath, []byte("#!/bin/sh\necho 'specscore 1.0.0'"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeEnv := cliinstall.InstallEnv{
		Env: cliinstall.Env{
			PathDirs:     func() []string { return []string{tempDir} },
			HostDir:      func() (string, error) { return tempDir, nil },
			IsExecutable: func(p string) bool { return p == specscorePath },
			EvalSymlinks: filepath.EvalSymlinks,
			Run: func(ctx context.Context, p string, args []string) ([]byte, error) {
				return []byte(`{"name":"specscore","version":"1.0.0","commit":"abcdef"}`), nil
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	cmd := NewUninstall(UninstallCommandOptions{
		HostID: "wb",
		Env:    fakeEnv,
	})

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"specscore", "--dry-run", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("json dry-run failed: %v", err)
	}

	jsonStr := out.String()
	if !strings.Contains(jsonStr, `"host": "wb"`) || !strings.Contains(jsonStr, `"name": "specscore"`) {
		t.Errorf("unexpected json output: %s", jsonStr)
	}
}

func TestNewUninstall_NonInteractiveRefusalWithoutYes(t *testing.T) {
	tempDir := t.TempDir()
	specscorePath := filepath.Join(tempDir, "specscore")
	if err := os.WriteFile(specscorePath, []byte("#!/bin/sh\necho 'specscore 1.0.0'"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeEnv := cliinstall.InstallEnv{
		Env: cliinstall.Env{
			PathDirs:     func() []string { return []string{tempDir} },
			HostDir:      func() (string, error) { return tempDir, nil },
			IsExecutable: func(p string) bool { return p == specscorePath },
			EvalSymlinks: filepath.EvalSymlinks,
			Run: func(ctx context.Context, p string, args []string) ([]byte, error) {
				return []byte(`{"name":"specscore","version":"1.0.0","commit":"abcdef"}`), nil
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	mapper := &testUninstallErrorMapper{}
	cmd := NewUninstall(UninstallCommandOptions{
		HostID:      "wb",
		Env:         fakeEnv,
		Interactive: func() bool { return false },
		Errors:      mapper,
	})

	cmd.SetArgs([]string{"specscore"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected non-interactive refusal error, got nil")
	}

	var f *selfupdate.Failure
	if !errors.As(err, &f) {
		t.Fatalf("expected *selfupdate.Failure, got: %T (%v)", err, err)
	}
	if f.Kind != selfupdate.KindNonInteractive {
		t.Fatalf("f.Kind = %v, want KindNonInteractive", f.Kind)
	}
}
