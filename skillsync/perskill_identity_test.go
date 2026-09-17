package skillsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// These tests pin down PluginIdentity's role as Sync's install/removal
// boundary (see the PluginIdentity and ownership-and-coexistence doc
// comments), using a scenario modeled on an OVDB-style consumer: two
// independently installable skills (storage + todo-demo) plus an unrelated
// "specscore" skill never registered with skillsync at all.
//
// TestSpikeS5_SharedPluginIdentity_RemovesSiblingSkill documents the failure
// mode a consumer must avoid: syncing only the todo bundle under a
// PluginIdentity shared with the storage skill removes the storage skill,
// because it is no longer desired under that shared key.
//
// TestSpikeS5_PerSkillPluginIdentity_LeavesSiblingsUntouched documents the
// configuration that satisfies "install one skill only": giving each skill
// its own PluginIdentity lets Sync install/remove them independently, leaves
// unrelated plugins and unmanaged directories untouched, and reports
// `unchanged` on a repeat run with no bundle changes.

func ovdbBundle(t *testing.T, pluginName, skillDir, revision string) Bundle {
	t.Helper()
	root := fstest.MapFS{
		skillDir + "/SKILL.md": &fstest.MapFile{Data: []byte("content for " + skillDir)},
	}
	digest, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	return Bundle{
		Plugin: PluginIdentity{Publisher: "openvaultdb", Name: pluginName},
		Source: Source{
			Repository: "github.com/openvaultdb/openvaultdb",
			Path:       "skills/" + skillDir,
			Revision:   revisionForTest(revision),
			Version:    "1.0.0",
			Digest:     digest,
		},
		FS: root,
	}
}

func TestSpikeS5_SharedPluginIdentity_RemovesSiblingSkill(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Simulate a specscore skill written by a completely different tool,
	// never registered with skillsync at all.
	mustMkSkill(t, dir, "specscore")

	// Both OVDB bundles share ONE PluginIdentity ("openvaultdb"/"ovdb"),
	// installed in separate Sync calls the way `ovdb skills install
	// <name>` would (one bundle per invocation).
	storage := ovdbBundle(t, "ovdb", "openvaultdb", "storage-r1")
	todo := ovdbBundle(t, "ovdb", "openvaultdb-todo-demo", "todo-r1")

	cfgStorage := Config{CLI: Identity{Publisher: "openvaultdb", Name: "ovdb"}, CurrentVersion: "1.0.0", Bundles: []Bundle{storage}}
	if _, err := Sync(ctx, cfgStorage, Options{Dir: dir}); err != nil {
		t.Fatalf("install storage skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openvaultdb", "SKILL.md")); err != nil {
		t.Fatalf("storage skill missing after install: %v", err)
	}

	// `ovdb skills install todo-demo` -> Sync with ONLY the todo bundle in
	// cfg.Bundles, same shared plugin identity.
	cfgTodoOnly := Config{CLI: Identity{Publisher: "openvaultdb", Name: "ovdb"}, CurrentVersion: "1.0.0", Bundles: []Bundle{todo}}
	report, err := Sync(ctx, cfgTodoOnly, Options{Dir: dir})
	if err != nil {
		t.Fatalf("install todo skill: %v", err)
	}

	t.Logf("shared-identity report: added=%v removed=%v unchanged=%v", report.Names(Added), report.Names(Removed), report.Names(Unchanged))

	// FINDING: with a shared PluginIdentity, installing only the todo-demo
	// bundle treats the openvaultdb skill (previously owned by the same
	// plugin key, now absent from cfg.Bundles) as no longer desired, and
	// Sync REMOVES it. This configuration fails the S5 pass criteria.
	if _, err := os.Stat(filepath.Join(dir, "openvaultdb")); err == nil {
		t.Fatal("expected shared-identity Sync to remove the sibling openvaultdb skill, but it is still present")
	}
	if removed := report.Names(Removed); len(removed) != 1 || removed[0] != "openvaultdb" {
		t.Fatalf("expected openvaultdb to be reported removed, got %v", removed)
	}
	assertSkillUntouched(t, dir, "specscore")
}

func TestSpikeS5_PerSkillPluginIdentity_LeavesSiblingsUntouched(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	mustMkSkill(t, dir, "specscore")

	// One PluginIdentity per skill.
	storage := ovdbBundle(t, "openvaultdb", "openvaultdb", "storage-r1")
	todo := ovdbBundle(t, "todo-demo", "openvaultdb-todo-demo", "todo-r1")

	cli := Identity{Publisher: "openvaultdb", Name: "ovdb"}

	cfgStorage := Config{CLI: cli, CurrentVersion: "1.0.0", Bundles: []Bundle{storage}}
	if _, err := Sync(ctx, cfgStorage, Options{Dir: dir}); err != nil {
		t.Fatalf("install storage skill: %v", err)
	}

	cfgTodoOnly := Config{CLI: cli, CurrentVersion: "1.0.0", Bundles: []Bundle{todo}}
	report, err := Sync(ctx, cfgTodoOnly, Options{Dir: dir})
	if err != nil {
		t.Fatalf("install todo skill: %v", err)
	}
	t.Logf("per-skill-identity first todo sync: added=%v removed=%v unchanged=%v", report.Names(Added), report.Names(Removed), report.Names(Unchanged))

	if got := report.Names(Added); len(got) != 1 || got[0] != "openvaultdb-todo-demo" {
		t.Fatalf("expected only the todo skill added, got %v", got)
	}
	if len(report.Names(Removed)) != 0 {
		t.Fatalf("expected no removals, got %v", report.Names(Removed))
	}
	assertSkillUntouched(t, dir, "openvaultdb")
	assertSkillUntouched(t, dir, "specscore")
	if _, err := os.Stat(filepath.Join(dir, "openvaultdb-todo-demo", "SKILL.md")); err != nil {
		t.Fatalf("todo skill missing after install: %v", err)
	}

	// Idempotency: same call again must report unchanged, no removed/added.
	report2, err := Sync(ctx, cfgTodoOnly, Options{Dir: dir})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	t.Logf("per-skill-identity second todo sync: added=%v removed=%v unchanged=%v", report2.Names(Added), report2.Names(Removed), report2.Names(Unchanged))
	if got := report2.Names(Unchanged); len(got) != 1 || got[0] != "openvaultdb-todo-demo" {
		t.Fatalf("expected unchanged on second run, got unchanged=%v added=%v removed=%v", got, report2.Names(Added), report2.Names(Removed))
	}
	if len(report2.Names(Added)) != 0 || len(report2.Names(Removed)) != 0 {
		t.Fatal("second run must not add or remove anything")
	}
	assertSkillUntouched(t, dir, "openvaultdb")
	assertSkillUntouched(t, dir, "specscore")
}

func mustMkSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte("unrelated tool content"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSkillUntouched(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
		t.Fatalf("expected %s to remain untouched, stat error: %v", name, err)
	}
}
