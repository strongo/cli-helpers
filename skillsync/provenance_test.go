package skillsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncAndStatusRecordSupplierCLIVersionWithoutChangingSource(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "provenance", "body")
	cfg := config(t, b)
	report, err := Sync(context.Background(), cfg, Options{Dir: dir})
	if err != nil || report.CLIVersion != "1.2.3" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	status, err := ReadStatus(dir)
	if err != nil || status.SupplierCLIVersions[b.Plugin.String()][cfg.CLI.String()] != "1.2.3" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	if status.Plugins[b.Plugin.String()] != b.Source {
		t.Fatalf("source changed: %#v", status.Plugins)
	}
	cfg.CurrentVersion = "1.2.4"
	report, err = Sync(context.Background(), cfg, Options{Dir: dir})
	if err != nil || report.Bundles[0].PriorCLIVersion != "1.2.3" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	status, err = ReadStatus(dir)
	if err != nil || status.SupplierCLIVersions[b.Plugin.String()][cfg.CLI.String()] != "1.2.4" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestRepeatedRegisteredSupplierDoesNotRewriteUnchangedOwnership(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "supplier-idempotence", "body")
	first := config(t, b)
	second := config(t, b)
	second.CLI = Identity{Publisher: "strongo", Name: "other-tool"}
	second.CurrentVersion = "1.2.4"
	if _, err := Sync(context.Background(), first, Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), second, Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	report, err := Sync(context.Background(), first, Options{Dir: dir})
	if err != nil || len(report.Names(Unchanged)) != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	after, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("unchanged marker rewritten: before=%q after=%q beforeTime=%v afterTime=%v", before, after, beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

func TestLegacyWBVersionSurvivesMigrationAsReportPriorVersion(t *testing.T) {
	for _, version := range []string{
		"0.92.0",
		"(devel)",
		"unknown",
		"dev",
		"v0.0.0-20260904214609-2fa8866c771f",
		"v0.95.1-0.20260904214609-2fa8866c771f",
		"v1.2.3-rc.1.0.20260904214609-2fa8866c771f",
	} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			b := bundle(t, "plugin", "legacy-version-"+version, "body")
			if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("body"), 0o644); err != nil {
				t.Fatal(err)
			}
			digest, err := legacyWBDigest(os.DirFS(dir), "alpha")
			if err != nil {
				t.Fatal(err)
			}
			marker, err := json.Marshal(map[string]any{"schema_version": 1, "wb_version": version, "skills": map[string]string{"alpha": digest}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".wb-skills-sync.json"), marker, 0o644); err != nil {
				t.Fatal(err)
			}
			report, err := Sync(context.Background(), config(t, b), Options{Dir: dir, Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: b.Plugin}})
			if err != nil {
				t.Fatal(err)
			}
			if got := report.Bundles[0].PriorCLIVersion; got != version {
				t.Fatalf("prior CLI version = %q", got)
			}
		})
	}
}

func TestLegacyWBVersionRejectsUnrecognizedBuildLabel(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "legacy-invalid-version", "body")
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := legacyWBDigest(os.DirFS(dir), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(map[string]any{"schema_version": 1, "wb_version": "nightly-42", "skills": map[string]string{"alpha": digest}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wb-skills-sync.json"), marker, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Sync(context.Background(), config(t, b), Options{Dir: dir, Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: b.Plugin}})
	if !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err=%v", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(data) != "body" {
		t.Fatalf("legacy target=%q err=%v", data, readErr)
	}
}

func TestValidCurrentCLIVersionAcceptsOnlyWellFormedGoPseudoVersions(t *testing.T) {
	for _, version := range []string{
		"v0.0.0-20260904214609-2fa8866c771f",
		"v0.95.1-0.20260904214609-2fa8866c771f",
		"v1.2.3-rc.1.0.20260904214609-2fa8866c771f",
		"v2.0.0-0.20260904214609-2fa8866c771f+incompatible",
	} {
		if !validCurrentCLIVersion(version) {
			t.Errorf("valid Go pseudo-version rejected: %q", version)
		}
	}
	for _, version := range []string{
		"nightly-42",
		"v0.95.1-0.2026090421460-2fa8866c771f",
		"v0.95.1-0.20260904214609-2fa8866c771",
		"v0.95.1-0.20260904214609-2FA8866C771F",
		"v0.95.1-1.20260904214609-2fa8866c771f",
	} {
		if validCurrentCLIVersion(version) {
			t.Errorf("invalid CLI version accepted: %q", version)
		}
	}
}

func TestPublicDescriptorJSONAndInvalidCompatibilityBounds(t *testing.T) {
	d := BundleDescriptor{Plugin: PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: revisionForTest("json"), Version: "1.2.3", Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Compatibility: Compatibility{MinCLI: "2.0.0", MaxCLI: "1.0.0"}}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !bytes.Contains(raw, []byte(`"min_cli"`)) {
		t.Fatalf("descriptor JSON = %s", raw)
	}
	var decoded BundleDescriptor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Source.Compatibility != d.Source.Compatibility {
		t.Fatalf("roundtrip = %#v", decoded.Source.Compatibility)
	}
	if Compatible("1.2.3", d.Source.Compatibility) {
		t.Fatal("inverted bounds reported compatible")
	}
	if err := ValidateDescriptor(d); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestDevelopmentCLIVersionStateRemainsReadable(t *testing.T) {
	for _, version := range []string{"(devel)", "unknown", "dev"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			b := bundle(t, "plugin", "development-"+version, "body")
			cfg := config(t, b)
			cfg.CurrentVersion = version
			if _, err := Sync(context.Background(), cfg, Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadStatus(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := Sync(context.Background(), cfg, Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateTargetRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTarget(filepath.Join(alias, "skills")); err == nil {
		t.Fatal("symlinked ancestor accepted")
	}
	if _, err := ValidateTarget(filepath.Join(real, "skills")); err != nil {
		t.Fatal(err)
	}
}
