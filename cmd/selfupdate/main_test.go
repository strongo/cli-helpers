package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/strongo/selfupdate"
)

// These tests exercise this CLI's own wiring — config construction, command
// structure, and --explain-path — never the network and never a real
// installed binary (REQ: no-network-in-tests extends to this reference CLI
// too, per its own spec section). Anything that would need DetectSelf to
// resolve the real go-test binary's path, or a live GitHub endpoint, is out
// of scope here: that behavior is the root selfupdate package's
// responsibility and is covered by its own test suite.

func TestBuildConfig_SelfHostingIdentity(t *testing.T) {
	cfg := buildConfig()

	if cfg.BinaryName != "selfupdate" {
		t.Errorf("BinaryName = %q, want selfupdate", cfg.BinaryName)
	}
	if cfg.Repository != "strongo/selfupdate" {
		t.Errorf("Repository = %q, want strongo/selfupdate", cfg.Repository)
	}
	if cfg.CurrentVersion != version {
		t.Errorf("CurrentVersion = %q, want the package-level version var %q", cfg.CurrentVersion, version)
	}
	if len(cfg.UndeterminedVersions) != 1 || cfg.UndeterminedVersions[0] != undeterminedVersion {
		t.Errorf("UndeterminedVersions = %v, want [%q]", cfg.UndeterminedVersions, undeterminedVersion)
	}
	if len(cfg.VersionProbeArgs) != 1 || cfg.VersionProbeArgs[0] != "--version" {
		t.Errorf("VersionProbeArgs = %v, want [--version] (matches cobra's own --version flag)", cfg.VersionProbeArgs)
	}
	if cfg.HTTPClient == nil {
		t.Error("HTTPClient is nil")
	}
}

func TestBuildConfig_SupportedPlatformsMatchGoreleaser(t *testing.T) {
	cfg := buildConfig()
	want := map[[2]string]bool{
		{"darwin", "amd64"}: true,
		{"darwin", "arm64"}: true,
		{"linux", "amd64"}:  true,
		{"linux", "arm64"}:  true,
	}
	if len(cfg.SupportedPlatforms) != len(want) {
		t.Fatalf("SupportedPlatforms has %d entries, want %d", len(cfg.SupportedPlatforms), len(want))
	}
	for _, p := range cfg.SupportedPlatforms {
		if !want[[2]string{p.GOOS, p.GOARCH}] {
			t.Errorf("unexpected platform %+v", p)
		}
	}
}

// An unstamped local build (the default var value) honestly reports itself
// as undetermined, not as some fake release (REQ: reference-cli-self-hosting).
func TestVersion_DefaultsToUndeterminedPlaceholder(t *testing.T) {
	if version != undeterminedVersion {
		t.Errorf("version = %q, want the undetermined placeholder %q for an unstamped build", version, undeterminedVersion)
	}
}

func TestNewRootCmd_Structure(t *testing.T) {
	root := newRootCmd()
	if root.Use != binaryName {
		t.Errorf("root Use = %q, want %q", root.Use, binaryName)
	}
	if root.Version == "" {
		t.Error("root Version is empty; --version would have nothing to report")
	}

	sub, _, err := root.Find([]string{"self-update"})
	if err != nil || sub == nil {
		t.Fatalf("self-update subcommand not found: %v", err)
	}
	if !sub.HasAlias("update") {
		t.Error("self-update command is missing the 'update' alias")
	}
	if sub.Flags().Lookup("explain-path") == nil {
		t.Error("missing --explain-path flag")
	}
	if sub.Flags().Lookup("check") == nil {
		t.Error("missing --check flag (from cobracmd.New)")
	}
	if sub.Flags().Lookup("dry-run") == nil {
		t.Error("missing --dry-run flag (from cobracmd.New)")
	}
	if sub.Flags().Lookup("format") == nil {
		t.Error("missing --format flag (JSONFormat: true)")
	}
}

// --explain-path classifies without touching the network or the real
// install location, against every configured manager plus the Manual/
// Ambiguous fallbacks — demonstrating layouts this machine does not have.
func TestExplainPath_ClassifiesWithoutNetworkOrRealPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"homebrew", "/opt/homebrew/Cellar/selfupdate/1.0.0/bin/selfupdate", "managed by Homebrew"},
		{"scoop", `C:\Users\u\scoop\shims\selfupdate.exe`, "managed by Scoop"},
		{"winget", `C:\Users\u\AppData\Local\Microsoft\WinGet\Links\selfupdate.exe`, "managed by WinGet"},
		{"manual", "/usr/local/bin/selfupdate", "manual install"},
		{"ambiguous", "/tmp/wherever/selfupdate", "ambiguous"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd() // fresh command per subtest: flags carry state
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs([]string{"self-update", "--explain-path", tt.path})
			if err := root.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Errorf("output %q does not contain %q", out.String(), tt.want)
			}
		})
	}
}

// Without --explain-path, the subcommand falls through to the real
// cobracmd-built RunE — verified here only by confirming it does NOT take
// the explain-path branch (it would need network/DetectSelf beyond that,
// which is out of scope for this CLI's own tests).
func TestExplainPath_EmptyFlagFallsThroughToUpdate(t *testing.T) {
	root := newRootCmd()
	sub, _, err := root.Find([]string{"self-update"})
	if err != nil {
		t.Fatalf("self-update subcommand not found: %v", err)
	}
	if p, _ := sub.Flags().GetString("explain-path"); p != "" {
		t.Errorf("--explain-path default = %q, want empty", p)
	}
}

func TestPassthroughErrors(t *testing.T) {
	var m passthroughErrors
	original := &selfupdate.Failure{Kind: selfupdate.KindDownload}
	if got := m.Failure(original); got != error(original) {
		t.Errorf("Failure() = %v, want the error returned unchanged", got)
	}
	if got := m.UpdateAvailable(selfupdate.CheckResult{Verdict: selfupdate.UpdateAvailable}); got != nil {
		t.Errorf("UpdateAvailable() = %v, want nil (informational only)", got)
	}
}
