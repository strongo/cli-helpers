package cliinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

func execName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func TestUninstallOutcome_String(t *testing.T) {
	cases := []struct {
		outcome  UninstallOutcome
		expected string
	}{
		{UninstallOutcomeUninstalled, "uninstalled"},
		{UninstallOutcomeNotInstalled, "not_installed"},
		{UninstallOutcomeDryRun, "dry_run"},
		{UninstallOutcomeDeclined, "declined"},
		{UninstallOutcomeRedirected, "redirected"},
		{UninstallOutcomeFailed, "failed"},
		{UninstallOutcome(999), "unknown"},
	}

	for _, c := range cases {
		if s := c.outcome.String(); s != c.expected {
			t.Errorf("outcome.String() = %q, want %q", s, c.expected)
		}
	}
}

func TestUninstallMethod_String(t *testing.T) {
	cases := []struct {
		method   UninstallMethod
		expected string
	}{
		{UninstallMethodDirect, "direct"},
		{UninstallMethodHomebrew, "homebrew"},
		{UninstallMethodManaged, "managed"},
		{UninstallMethod(999), "unknown"},
	}

	for _, c := range cases {
		if s := c.method.String(); s != c.expected {
			t.Errorf("method.String() = %q, want %q", s, c.expected)
		}
	}
}

func TestUninstallBatchResult_Methods(t *testing.T) {
	// Empty batch
	emptyBatch := UninstallBatchResult{Host: "wb"}
	if emptyBatch.Failed() {
		t.Error("emptyBatch.Failed() = true, want false")
	}
	if emptyBatch.Failure() != nil {
		t.Errorf("emptyBatch.Failure() = %v, want nil", emptyBatch.Failure())
	}
	if count := emptyBatch.UninstalledCount(); count != 0 {
		t.Errorf("emptyBatch.UninstalledCount() = %d, want 0", count)
	}

	// Mixed batch
	batch := UninstallBatchResult{
		Host: "wb",
		Results: []UninstallResult{
			{
				Target:  "specscore",
				Outcome: UninstallOutcomeUninstalled,
			},
			{
				Target:  "cover100",
				Outcome: UninstallOutcomeFailed,
				Failure: &selfupdate.Failure{Kind: selfupdate.KindPermission, Err: errors.New("cannot delete")},
			},
			{
				Target:  "ingitdb",
				Outcome: UninstallOutcomeNotInstalled,
			},
		},
	}

	if !batch.Failed() {
		t.Error("batch.Failed() = false, want true")
	}
	if err := batch.Failure(); err == nil {
		t.Error("batch.Failure() = nil, want error")
	}
	if count := batch.UninstalledCount(); count != 1 {
		t.Errorf("batch.UninstalledCount() = %d, want 1", count)
	}
}

func TestPlanUninstall_InvalidHostPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unknown host id")
		}
	}()

	_, _ = PlanUninstall(context.Background(), []string{"specscore"}, UninstallOptions{
		HostID: "invalid-host-id",
	})
}

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

func TestPlanUninstall_All(t *testing.T) {
	tempDir := t.TempDir()
	specscorePath := filepath.Join(tempDir, execName("specscore"))
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
				return []byte(`{"name":"specscore","version":"1.0.0","commit":"abcdef"}`), nil
			},
		},
		UserHomeDir: func() (string, error) { return tempDir, nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}

	opts := UninstallOptions{
		HostID: "wb",
		All:    true,
		Dir:    tempDir,
		Env:    fakeEnv,
	}

	plan, err := PlanUninstall(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("PlanUninstall --all failed: %v", err)
	}

	// Should only contain specscore since it is the only one installed in tempDir
	if len(plan.Results) != 1 {
		t.Fatalf("len(plan.Results) = %d, want 1", len(plan.Results))
	}
	if plan.Results[0].Target != "specscore" {
		t.Errorf("plan.Results[0].Target = %q, want specscore", plan.Results[0].Target)
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
	specscorePath := filepath.Join(tempDir, execName("specscore"))
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

	// Dry-run execution does not modify anything
	opts.DryRun = true
	dryExecResult, err := ExecuteUninstall(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUninstall (dry-run) failed: %v", err)
	}
	if dryExecResult.Results[0].Outcome != UninstallOutcomeDryRun {
		t.Errorf("dryExecResult outcome = %v, want UninstallOutcomeDryRun", dryExecResult.Results[0].Outcome)
	}
	opts.DryRun = false

	// Now real execute
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

	// Re-executing deletion on already-removed file is also success
	execResult2, _ := ExecuteUninstall(context.Background(), plan, opts)
	if execResult2.Results[0].Outcome != UninstallOutcomeUninstalled {
		t.Errorf("re-executing remove = %v, want UninstallOutcomeUninstalled", execResult2.Results[0].Outcome)
	}
}

func TestPlanAndExecuteUninstall_Homebrew(t *testing.T) {
	tempDir := t.TempDir()
	brewPath := filepath.Join(tempDir, execName("specscore"))

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

	// 1. Without RunManaged (Redirected)
	opts.Env.RunManaged = nil
	redirectedExec, err := ExecuteUninstall(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUninstall failed: %v", err)
	}
	if redirectedExec.Results[0].Outcome != UninstallOutcomeRedirected {
		t.Errorf("Outcome = %v, want UninstallOutcomeRedirected", redirectedExec.Results[0].Outcome)
	}

	// 2. With RunManaged returning error
	opts.Env.RunManaged = func(ctx context.Context, manager string, argv []string) error {
		return errors.New("brew error")
	}
	failedExec, err := ExecuteUninstall(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUninstall failed: %v", err)
	}
	if failedExec.Results[0].Outcome != UninstallOutcomeFailed {
		t.Errorf("Outcome = %v, want UninstallOutcomeFailed", failedExec.Results[0].Outcome)
	}

	// 3. With RunManaged succeeding
	var ranManager string
	var ranArgv []string
	opts.Env.RunManaged = func(ctx context.Context, manager string, argv []string) error {
		ranManager = manager
		ranArgv = argv
		return nil
	}

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

func TestExecuteUninstall_DirectFailures(t *testing.T) {
	opts := UninstallOptions{
		HostID: "wb",
		Env:    DefaultInstallEnv(),
	}

	// 1. Path is empty
	batch := UninstallBatchResult{
		Host: "wb",
		Results: []UninstallResult{
			{
				Target:  "tool",
				Outcome: UninstallOutcomeDryRun,
				Method:  UninstallMethodDirect,
				Path:    "",
			},
		},
	}
	res, _ := ExecuteUninstall(context.Background(), batch, opts)
	if res.Results[0].Outcome != UninstallOutcomeFailed {
		t.Errorf("empty path outcome = %v, want UninstallOutcomeFailed", res.Results[0].Outcome)
	}

	// 2. Path cannot be removed (directory not file or permissions)
	tempDir := t.TempDir()
	nestedDir := filepath.Join(tempDir, "cannot-delete")
	_ = os.MkdirAll(filepath.Join(nestedDir, "child"), 0o755)

	batch2 := UninstallBatchResult{
		Host: "wb",
		Results: []UninstallResult{
			{
				Target:  "tool",
				Outcome: UninstallOutcomeDryRun,
				Method:  UninstallMethodDirect,
				Path:    nestedDir,
			},
		},
	}
	res2, _ := ExecuteUninstall(context.Background(), batch2, opts)
	if res2.Results[0].Outcome != UninstallOutcomeFailed {
		t.Errorf("dir remove outcome = %v, want UninstallOutcomeFailed", res2.Results[0].Outcome)
	}

	// 3. Unsupported method
	batch3 := UninstallBatchResult{
		Host: "wb",
		Results: []UninstallResult{
			{
				Target:  "tool",
				Outcome: UninstallOutcomeDryRun,
				Method:  UninstallMethod(999),
			},
		},
	}
	res3, _ := ExecuteUninstall(context.Background(), batch3, opts)
	if res3.Results[0].Outcome != UninstallOutcomeFailed {
		t.Errorf("unknown method outcome = %v, want UninstallOutcomeFailed", res3.Results[0].Outcome)
	}
}

func TestPlanUninstall_HomebrewDetection(t *testing.T) {
	tempDir := t.TempDir()
	brewDir := filepath.Join(tempDir, "homebrew", "bin")
	if err := os.MkdirAll(brewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	specscorePath := filepath.Join(brewDir, execName("specscore"))
	if err := os.WriteFile(specscorePath, []byte("#!/bin/sh\necho 'specscore 1.0.0'"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeEnv := InstallEnv{
		Env: Env{
			PathDirs:     func() []string { return []string{brewDir} },
			HostDir:      func() (string, error) { return brewDir, nil },
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
	if plan.Results[0].Method != UninstallMethodHomebrew {
		t.Errorf("Method = %v, want UninstallMethodHomebrew", plan.Results[0].Method)
	}
	if strings.Join(plan.Results[0].CaskArgv, " ") != "brew uninstall --cask specscore/tap/specscore" {
		t.Errorf("CaskArgv = %v, want brew uninstall --cask specscore/tap/specscore", plan.Results[0].CaskArgv)
	}
}

func TestExecuteUninstall_SkipsNonDryRun(t *testing.T) {
	opts := UninstallOptions{
		HostID: "wb",
		Env:    DefaultInstallEnv(),
	}

	batch := UninstallBatchResult{
		Host: "wb",
		Results: []UninstallResult{
			{
				Target:  "specscore",
				Outcome: UninstallOutcomeNotInstalled,
			},
			{
				Target:  "cover100",
				Outcome: UninstallOutcomeUninstalled,
			},
		},
	}

	res, err := ExecuteUninstall(context.Background(), batch, opts)
	if err != nil {
		t.Fatalf("ExecuteUninstall failed: %v", err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("len(res.Results) = %d, want 2", len(res.Results))
	}
	if res.Results[0].Outcome != UninstallOutcomeNotInstalled || res.Results[1].Outcome != UninstallOutcomeUninstalled {
		t.Errorf("unexpected outcomes: %+v", res.Results)
	}
}
