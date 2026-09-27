package cliinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

func TestPlanUninstall_UnknownTarget(t *testing.T) {
	opts := UninstallOptions{
		HostID: "wb",
		Env:    DefaultInstallEnv(),
	}

	batch, err := PlanUninstall(context.Background(), []string{"not-a-real-tool"}, opts)
	if err == nil {
		t.Fatal("PlanUninstall with unknown target returned nil error, want error")
	}

	var f *selfupdate.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err is not *selfupdate.Failure: %v", err)
	}
	if f.Kind != selfupdate.KindUnknownTarget {
		t.Fatalf("f.Kind = %v, want KindUnknownTarget", f.Kind)
	}
	if batch.Host != "wb" {
		t.Fatalf("batch.Host = %q, want wb", batch.Host)
	}
}

func TestPlanUninstall_NotInstalled(t *testing.T) {
	tempDir := t.TempDir()
	fakeEnv := InstallEnv{
		Env: Env{
			PathDirs: func() []string { return []string{tempDir} },
			HostDir:  func() (string, error) { return tempDir, nil },
			IsExecutable: func(p string) bool { return false },
			EvalSymlinks: filepath.EvalSymlinks,
			Run: func(ctx context.Context, p string, args []string) ([]byte, error) {
				return nil, errors.New("not executable")
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	opts := UninstallOptions{
		HostID: "wb",
		Env:    fakeEnv,
	}

	batch, err := PlanUninstall(context.Background(), []string{"specscore"}, opts)
	if err != nil {
		t.Fatalf("PlanUninstall failed: %v", err)
	}

	if len(batch.Results) != 1 {
		t.Fatalf("len(batch.Results) = %d, want 1", len(batch.Results))
	}
	r := batch.Results[0]
	if r.Target != "specscore" {
		t.Errorf("r.Target = %q, want specscore", r.Target)
	}
	if r.Outcome != UninstallOutcomeNotInstalled {
		t.Errorf("r.Outcome = %v, want UninstallOutcomeNotInstalled", r.Outcome)
	}
}

func TestPlanAndExecuteUninstall_Direct(t *testing.T) {
	tempDir := t.TempDir()
	specscorePath := filepath.Join(tempDir, "specscore")
	if err := os.WriteFile(specscorePath, []byte("#!/bin/sh\necho 'specscore 1.0.0'"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeEnv := InstallEnv{
		Env: Env{
			PathDirs:     func() []string { return []string{tempDir} },
			HostDir:      func() (string, error) { return tempDir, nil },
			IsExecutable: func(p string) bool { return p == specscorePath },
			EvalSymlinks: filepath.EvalSymlinks,
			Run: func(ctx context.Context, p string, args []string) ([]byte, error) {
				if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
					return []byte(`{"name":"specscore","version":"1.0.0","commit":"abcdef"}`), nil
				}
				return []byte("specscore 1.0.0 (abcdef) 2026-01-01"), nil
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	opts := UninstallOptions{
		HostID: "wb",
		Env:    fakeEnv,
	}

	plan, err := PlanUninstall(context.Background(), []string{"specscore"}, opts)
	if err != nil {
		t.Fatalf("PlanUninstall failed: %v", err)
	}

	if len(plan.Results) != 1 {
		t.Fatalf("len(plan.Results) = %d, want 1", len(plan.Results))
	}
	if plan.Results[0].Outcome != UninstallOutcomeDryRun {
		t.Fatalf("plan.Results[0].Outcome = %v, want UninstallOutcomeDryRun", plan.Results[0].Outcome)
	}
	if plan.Results[0].Method != UninstallMethodDirect {
		t.Fatalf("plan.Results[0].Method = %v, want UninstallMethodDirect", plan.Results[0].Method)
	}
	if plan.Results[0].Path != specscorePath {
		t.Fatalf("plan.Results[0].Path = %q, want %q", plan.Results[0].Path, specscorePath)
	}

	// Now execute
	execResult, err := ExecuteUninstall(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUninstall failed: %v", err)
	}

	if execResult.Failed() {
		t.Fatalf("ExecuteUninstall reported failure: %v", execResult.Failure())
	}
	if execResult.Results[0].Outcome != UninstallOutcomeUninstalled {
		t.Fatalf("Outcome = %v, want UninstallOutcomeUninstalled", execResult.Results[0].Outcome)
	}

	// Verify file was deleted
	if _, err := os.Stat(specscorePath); !os.IsNotExist(err) {
		t.Errorf("file %s still exists after ExecuteUninstall", specscorePath)
	}
}

func TestPlanAndExecuteUninstall_Homebrew(t *testing.T) {
	tempDir := t.TempDir()
	brewPath := filepath.Join(tempDir, "specscore")

	fakeEnv := InstallEnv{
		Env: Env{
			PathDirs:     func() []string { return []string{tempDir} },
			HostDir:      func() (string, error) { return tempDir, nil },
			IsExecutable: func(p string) bool { return p == brewPath },
			EvalSymlinks: filepath.EvalSymlinks,
			Run: func(ctx context.Context, p string, args []string) ([]byte, error) {
				return []byte(`{"name":"specscore","version":"1.0.0","commit":"abcdef"}`), nil
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	opts := UninstallOptions{
		HostID: "wb",
		Env:    fakeEnv,
	}

	plan, err := PlanUninstall(context.Background(), []string{"specscore"}, opts)
	if err != nil {
		t.Fatalf("PlanUninstall failed: %v", err)
	}

	// Overwrite method to Homebrew to test the Homebrew execution branch
	plan.Results[0].Outcome = UninstallOutcomeDryRun
	plan.Results[0].Method = UninstallMethodHomebrew
	plan.Results[0].CaskArgv = []string{"brew", "uninstall", "--cask", "specscore"}

	var ranManager string
	var ranArgv []string
	fakeEnv.RunManaged = func(ctx context.Context, manager string, argv []string) error {
		ranManager = manager
		ranArgv = argv
		return nil
	}
	opts.Env = fakeEnv

	execResult, err := ExecuteUninstall(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUninstall failed: %v", err)
	}
	if execResult.Failed() {
		t.Fatalf("ExecuteUninstall reported failure: %v", execResult.Failure())
	}
	if execResult.Results[0].Outcome != UninstallOutcomeUninstalled {
		t.Fatalf("Outcome = %v, want UninstallOutcomeUninstalled", execResult.Results[0].Outcome)
	}
	if ranManager != "brew" || len(ranArgv) != 3 || ranArgv[2] != "specscore" {
		t.Errorf("ran = %s %v, want brew [uninstall --cask specscore]", ranManager, ranArgv)
	}
}
