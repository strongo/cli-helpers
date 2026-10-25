package skillsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contractPluginState(t *testing.T) pluginState {
	t.Helper()
	b := bundle(t, "plugin", "contracts", "body")
	return pluginState{
		Revision:  b.Source.Revision,
		Digest:    b.Source.Digest,
		CLI:       "strongo/tool",
		Suppliers: map[string]string{"strongo/tool": b.Source.Revision},
		Source:    b.Source,
		Skills:    map[string]string{"alpha": strings.Repeat("a", 64)},
	}
}

func writeContractState(t *testing.T, dir string, s state) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(dir), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestContractsRejectUnreadableStateAndSupplierVersionCorruption(t *testing.T) {
	t.Run("state marker is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(statePath(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadStatus(dir); err == nil {
			t.Fatal("directory state marker was accepted")
		}
	})

	for _, mutate := range []func(*pluginState){
		func(entry *pluginState) {
			entry.SupplierCLIVersions = map[string]string{"invalid": "1.2.3"}
		},
		func(entry *pluginState) {
			entry.SupplierCLIVersions = map[string]string{"strongo/other": "1.2.3"}
		},
	} {
		dir := t.TempDir()
		entry := contractPluginState(t)
		mutate(&entry)
		writeContractState(t, dir, state{Schema: stateSchema, Plugins: map[string]pluginState{"strongo/plugin": entry}})
		if _, err := ReadStatus(dir); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("corrupt supplier version err=%v", err)
		}
	}
}

func TestContractsKeepLegacyValidationClosedAcrossDigestFaults(t *testing.T) {
	dir := t.TempDir()
	const skill = "alpha"
	const body = "legacy skill"
	if err := os.Mkdir(filepath.Join(dir, skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skill, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// WB's legacy marker hashes paths relative to each skill directory.
	legacyDigest := sha256.Sum256([]byte("SKILL.md\x00" + body + "\x00"))
	legacy := LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: PluginIdentity{Publisher: "strongo", Name: "plugin"}}
	writeMarker := func(t *testing.T, digest string, version string) {
		t.Helper()
		raw, err := json.Marshal(struct {
			SchemaVersion int               `json:"schema_version"`
			Skills        map[string]string `json:"skills"`
			WBVersion     string            `json:"wb_version"`
		}{SchemaVersion: 1, Skills: map[string]string{skill: digest}, WBVersion: version})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, legacy.MarkerFile), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("content differs from historic marker", func(t *testing.T) {
		writeMarker(t, strings.Repeat("0", 64), "")
		if _, err := importLegacy(dir, legacy); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("changed legacy content err=%v", err)
		}
	})
	t.Run("modern digest fault after historic digest matched", func(t *testing.T) {
		writeMarker(t, hex.EncodeToString(legacyDigest[:]), "")
		fault := errors.New("tree removed after legacy validation")
		original := installedSkillDigest
		installedSkillDigest = func(string, string) (string, error) { return "", fault }
		t.Cleanup(func() { installedSkillDigest = original })
		if _, err := importLegacy(dir, legacy); !errors.Is(err, ErrStateCorrupt) || !strings.Contains(err.Error(), fault.Error()) {
			t.Fatalf("modern digest fault err=%v", err)
		}
	})
	t.Run("invalid supplied CLI is rejected after valid marker", func(t *testing.T) {
		writeMarker(t, hex.EncodeToString(legacyDigest[:]), "1.2.3")
		if _, err := importLegacy(dir, legacy, Identity{Publisher: "bad/name", Name: "tool"}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("invalid legacy CLI err=%v", err)
		}
	})
}

func TestContractsReportFilteringAndStateSerializationFailure(t *testing.T) {
	report := Report{Changes: []Change{
		{Name: "beta", Action: Updated, Outcome: Applied},
		{Name: "alpha", Action: Updated, Outcome: Planned},
		{Name: "gamma", Action: Updated, Outcome: Restored},
	}}
	if got := report.NamesFor(Updated, Applied, Restored); strings.Join(got, ",") != "beta,gamma" {
		t.Fatalf("outcome-filtered names=%v", got)
	}

	dir := t.TempDir()
	err := writeState(dir, state{Plugins: map[string]pluginState{"strongo/plugin": {
		SyncedAt: time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}}})
	if err == nil {
		t.Fatal("unsupported time.Time year was serialized")
	}
	if _, err := os.Lstat(statePath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state marker appeared after serialization failure: %v", err)
	}
}
