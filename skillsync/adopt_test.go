package skillsync

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// frontmatterSKILL builds a minimal, real SKILL.md: a leading YAML
// frontmatter block declaring "name", followed by body text distinct enough
// that a test can tell which copy (pre-existing vs. bundled) produced it.
func frontmatterSKILL(name, body string) string {
	return "---\nname: " + name + "\ndescription: test skill\n---\n" + body + "\n"
}

// adoptionBundle ships one skill per name, each with real frontmatter whose
// declared name matches its own bundle directory name, so classifyAdoption's
// name check can pass against it.
func adoptionBundle(t *testing.T, plugin, revision string, names ...string) Bundle {
	t.Helper()
	skills := map[string]string{}
	for _, name := range names {
		skills[name] = frontmatterSKILL(name, "bundled body for "+name)
	}
	return bundleWith(t, plugin, revision, skills)
}

func writeUnmanagedSkill(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, data := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func listAdoptionBackups(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, adoptedBackupDirName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSyncAdoptsUnmanagedFolderWithDurableBackup(t *testing.T) {
	dir := t.TempDir()
	original := frontmatterSKILL("alpha", "my own pre-existing copy")
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": original})

	b := adoptionBundle(t, "plugin", "r1", "alpha")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if got := report.Names(Adopted); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("adopted = %v report=%#v", got, report)
	}
	var change Change
	found := false
	for _, c := range report.Changes {
		if c.Name == "alpha" {
			change, found = c, true
		}
	}
	if !found || change.Outcome != Applied || change.BackupPath == "" {
		t.Fatalf("change=%#v", change)
	}

	// The target now holds exactly the bundled content.
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != frontmatterSKILL("alpha", "bundled body for alpha") {
		t.Fatalf("target content = %q", data)
	}

	// The durable backup preserves the exact original bytes, and survives
	// the transaction's own commit-time cleanup.
	backup, err := os.ReadFile(filepath.Join(change.BackupPath, "SKILL.md"))
	if err != nil {
		t.Fatalf("backup unreadable: %v", err)
	}
	if string(backup) != original {
		t.Fatalf("backup content = %q, want %q", backup, original)
	}
	if got := filepath.Dir(filepath.Dir(change.BackupPath)); got != filepath.Join(dir, adoptedBackupDirName) {
		t.Fatalf("backup path = %q not under %s", change.BackupPath, adoptedBackupDirName)
	}

	installed, err := readState(dir)
	if err != nil || installed.Plugins[b.Plugin.String()].Skills["alpha"] == "" {
		t.Fatalf("state=%#v err=%v", installed, err)
	}
}

func TestSyncKeepsForeignFileConflictByteIdentical(t *testing.T) {
	dir := t.TempDir()
	// The bundle ships only "alpha/SKILL.md". A pre-existing "extra.txt" is
	// not part of that bundle, so overwriting the folder could silently
	// discard it; adoption must refuse instead.
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":  frontmatterSKILL("alpha", "mine"),
		"extra.txt": "do not touch me",
	})

	b := adoptionBundle(t, "plugin", "r1", "alpha")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("conflict = %v report=%#v", got, report)
	}
	for _, c := range report.Changes {
		if c.Name == "alpha" && c.Reason == "" {
			t.Fatalf("conflict has no remedy reason: %#v", c)
		}
	}

	skill, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(skill) != frontmatterSKILL("alpha", "mine") {
		t.Fatalf("SKILL.md changed: %q err=%v", skill, err)
	}
	extra, err := os.ReadFile(filepath.Join(dir, "alpha", "extra.txt"))
	if err != nil || string(extra) != "do not touch me" {
		t.Fatalf("extra.txt changed: %q err=%v", extra, err)
	}
	if names := listAdoptionBackups(t, dir); len(names) != 0 {
		t.Fatalf("refused adoption still backed up: %v", names)
	}
}

func TestSyncAddsMissingSiblingDespiteUnrelatedConflict(t *testing.T) {
	dir := t.TempDir()
	// alpha is occupied by a folder that cannot be adopted (a foreign file);
	// beta is simply missing. This is the reported regression: one plugin
	// skill's conflict must not refuse an unrelated missing sibling.
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":  frontmatterSKILL("alpha", "mine"),
		"extra.txt": "foreign",
	})

	b := adoptionBundle(t, "plugin", "r1", "alpha", "beta")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if got := report.Names(Conflict); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("conflicts = %v report=%#v", got, report)
	}
	if got := report.NamesFor(Added, Applied); len(got) != 1 || got[0] != "beta" {
		t.Fatalf("added = %v report=%#v", got, report)
	}
	if _, err := os.Stat(filepath.Join(dir, "beta", "SKILL.md")); err != nil {
		t.Fatalf("beta not installed: %v", err)
	}

	installed, err := readState(dir)
	if err != nil {
		t.Fatal(err)
	}
	skills := installed.Plugins[b.Plugin.String()].Skills
	if _, ownsAlpha := skills["alpha"]; ownsAlpha {
		t.Fatalf("conflicted alpha recorded as owned: %#v", skills)
	}
	if _, ownsBeta := skills["beta"]; !ownsBeta {
		t.Fatalf("added beta not recorded as owned: %#v", skills)
	}
}

func TestSyncAdoptionIsIdempotentWithNoSecondBackup(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})

	b := adoptionBundle(t, "plugin", "r1", "alpha")
	if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	backupsAfterAdoption := listAdoptionBackups(t, dir)
	if len(backupsAfterAdoption) != 1 {
		t.Fatalf("backups after adoption = %v", backupsAfterAdoption)
	}

	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Unchanged); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("second sync = %v report=%#v", got, report)
	}
	if got := listAdoptionBackups(t, dir); len(got) != 1 || got[0] != backupsAfterAdoption[0] {
		t.Fatalf("second sync backups = %v, want unchanged %v", got, backupsAfterAdoption)
	}
}

func TestClassifyAdoptionRejectsEveryUnsafeOrUnprovenShape(t *testing.T) {
	item := skill{Name: "alpha", Digest: strings.Repeat("a", 64)}
	source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte(frontmatterSKILL("alpha", "bundled"))}}

	t.Run("no SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		action, reason := classifyAdoption(dir, item, source)
		if action != Conflict || !strings.Contains(reason, "no SKILL.md") {
			t.Fatalf("action=%q reason=%q", action, reason)
		}
	})

	t.Run("unreadable SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		// A SKILL.md that is itself a directory fails os.ReadFile with an
		// error other than fs.ErrNotExist.
		if err := os.MkdirAll(filepath.Join(dir, "alpha", "SKILL.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		action, reason := classifyAdoption(dir, item, source)
		if action != Conflict || reason != "unsafe target" {
			t.Fatalf("action=%q reason=%q", action, reason)
		}
	})

	t.Run("no frontmatter name", func(t *testing.T) {
		dir := t.TempDir()
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": "---\ndescription: only\n---\nbody\n"})
		action, reason := classifyAdoption(dir, item, source)
		if action != Conflict || !strings.Contains(reason, "no frontmatter name") {
			t.Fatalf("action=%q reason=%q", action, reason)
		}
	})

	t.Run("mismatched frontmatter name", func(t *testing.T) {
		dir := t.TempDir()
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("someone-else", "body")})
		action, reason := classifyAdoption(dir, item, source)
		if action != Conflict || !strings.Contains(reason, `declares name "someone-else"`) {
			t.Fatalf("action=%q reason=%q", action, reason)
		}
	})

	t.Run("bundle lookup failure surfaces as unsafe target", func(t *testing.T) {
		dir := t.TempDir()
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "body")})
		emptySource := fstest.MapFS{} // "alpha" is not present in this source at all.
		action, reason := classifyAdoption(dir, item, emptySource)
		if action != Conflict || reason != "unsafe target" {
			t.Fatalf("action=%q reason=%q", action, reason)
		}
	})

	t.Run("adoptable", func(t *testing.T) {
		dir := t.TempDir()
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})
		action, reason := classifyAdoption(dir, item, source)
		if action != Adopted || reason != "" {
			t.Fatalf("action=%q reason=%q", action, reason)
		}
	})
}

func TestFrontmatterFieldParsesOrRejectsEveryShape(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"no frontmatter at all", "plain body, no delimiter\n", ""},
		{"field present after a non-matching line", "---\ndescription: first\nname: alpha\n---\nbody\n", "alpha"},
		{"closing delimiter reached with no field", "---\ndescription: only\n---\nbody\n", ""},
		{"never closes", "---\ndescription: only\n", ""},
		{"quoted value", "---\nname: \"alpha\"\n---\n", "alpha"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := frontmatterField([]byte(c.data), "name"); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestForeignTargetFileReportsBundleLookupFailure(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "body")})
	if _, err := foreignTargetFile(dir, "alpha", fstest.MapFS{}); err == nil {
		t.Fatal("expected error for a source that does not ship alpha at all")
	}
}

func TestSyncAdoptionBackupFailureLeavesTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	original := frontmatterSKILL("alpha", "my own pre-existing copy")
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": original})

	withTransactionOperations(t, func(ops *transactionOperationSet) {
		realMkdirAll := ops.mkdirAll
		ops.mkdirAll = func(path string, mode fs.FileMode) error {
			if strings.Contains(filepath.ToSlash(path), adoptedBackupDirName) {
				return errors.New("backup mkdir failed")
			}
			return realMkdirAll(path, mode)
		}
	})

	b := adoptionBundle(t, "plugin", "r1", "alpha")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err == nil {
		t.Fatal("expected backup failure to surface as an error")
	}
	for _, c := range report.Changes {
		if c.Name == "alpha" && c.Outcome == Applied {
			t.Fatalf("adoption applied despite backup failure: %#v", c)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != original {
		t.Fatalf("target changed despite backup failure: %q err=%v", data, err)
	}
	if names := listAdoptionBackups(t, dir); len(names) != 0 {
		t.Fatalf("partial backup left behind: %v", names)
	}
}

func TestAdoptedChangeReportHasJSONShape(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})

	b := adoptionBundle(t, "plugin", "r1", "alpha")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Changes []struct {
			Name       string `json:"name"`
			Action     string `json:"action"`
			Outcome    string `json:"outcome"`
			BackupPath string `json:"backup_path"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range decoded.Changes {
		if c.Name != "alpha" {
			continue
		}
		found = true
		if c.Action != "adopted" || c.Outcome != "applied" || c.BackupPath == "" {
			t.Fatalf("decoded change = %#v", c)
		}
	}
	if !found {
		t.Fatalf("alpha change missing from JSON: %s", raw)
	}
}
