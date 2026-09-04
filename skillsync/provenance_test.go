package skillsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
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

func TestLegacyWBVersionSurvivesMigrationAsReportPriorVersion(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "legacy-version", "body")
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
	marker, err := json.Marshal(map[string]any{"schema_version": 1, "wb_version": "0.92.0", "skills": map[string]string{"alpha": digest}})
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
	if got := report.Bundles[0].PriorCLIVersion; got != "0.92.0" {
		t.Fatalf("prior CLI version = %q", got)
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
