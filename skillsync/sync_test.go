package skillsync

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

//go:embed testdata/executable-script
var executableFixture embed.FS

func revisionForTest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return fmt.Sprintf("%x", sum[:])[:40]
}

func bundle(t *testing.T, plugin, revision, body string) Bundle {
	t.Helper()
	root := fstest.MapFS{
		"alpha/SKILL.md":     &fstest.MapFile{Data: []byte(body)},
		"alpha/reference.md": &fstest.MapFile{Data: []byte("reference")},
	}
	digest, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	return Bundle{Plugin: PluginIdentity{Publisher: "strongo", Name: plugin}, Source: Source{Repository: "github.com/strongo/" + plugin + "-plugin", Path: "skills", Revision: revisionForTest(revision), Version: "1.0.0", Digest: digest}, FS: root}
}

func bundleWith(t *testing.T, plugin, revision string, skills map[string]string) Bundle {
	t.Helper()
	root := fstest.MapFS{}
	for name, body := range skills {
		root[name+"/SKILL.md"] = &fstest.MapFile{Data: []byte(body)}
	}
	digest, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	return Bundle{Plugin: PluginIdentity{Publisher: "strongo", Name: plugin}, Source: Source{Repository: "github.com/strongo/" + plugin + "-plugin", Path: "skills", Revision: revisionForTest(revision), Version: "1.0.0", Digest: digest}, FS: root}
}

func config(t *testing.T, b Bundle) Config {
	t.Helper()
	return Config{CLI: Identity{Publisher: "strongo", Name: "tool"}, CurrentVersion: "1.2.3", Bundles: []Bundle{b}}
}

func TestSyncInstallThenNoopLeavesFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "r1", "first")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Added); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("added = %v", got)
	}
	path := filepath.Join(dir, "alpha", "SKILL.md")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err = Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Unchanged); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("unchanged = %v", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("no-op changed skill timestamp")
	}
}

func TestSyncDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "r1", "first")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Added); len(got) != 1 {
		t.Fatalf("added = %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, StateFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state exists after dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("skill exists after dry-run: %v", err)
	}
}

func TestSyncDoesNotOverwriteUnmanagedOrModifiedSkill(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "r1", "first")
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 {
		t.Fatalf("conflict = %v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); string(data) != "mine" {
		t.Fatal("unmanaged content changed")
	}

	// First install a second skill, then tamper with it. A newer bundle must
	// report the drift instead of replacing the user's edit.
	b2 := bundleWith(t, "plugin", "r1", map[string]string{"beta": "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), config(t, b2), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta", "SKILL.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	b3 := bundleWith(t, "plugin", "r2", map[string]string{"beta": "new"})
	report, err = Sync(context.Background(), config(t, b3), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 || got[0] != "beta" {
		t.Fatalf("conflict = %v", got)
	}
}

func TestSyncRemovesRetiredSkillsAsOnePluginRevision(t *testing.T) {
	dir := t.TempDir()
	old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	newer := bundleWith(t, "plugin", "r2", map[string]string{"alpha": "updated"})
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed() || strings.Join(report.Names(Removed), ",") != "beta" {
		t.Fatalf("report=%#v", report)
	}
	for _, change := range report.Changes {
		if change.Name == "alpha" && (change.Action != Updated || change.Outcome != Applied) {
			t.Fatalf("alpha change=%#v", change)
		}
		if change.Name == "beta" && (change.Action != Removed || change.Outcome != Applied) {
			t.Fatalf("beta change=%#v", change)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "beta")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("retired skill remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(data) != "updated" {
		t.Fatalf("retained skill=%q err=%v", data, err)
	}
	installed, err := readState(dir)
	if err != nil || len(installed.Plugins[old.Plugin.String()].Skills) != 1 || installed.Plugins[old.Plugin.String()].Skills["alpha"] == "" {
		t.Fatalf("state=%#v err=%v", installed, err)
	}
	report, err = Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil || report.Changed() || strings.Join(report.Names(Unchanged), ",") != "alpha" {
		t.Fatalf("noop report=%#v err=%v", report, err)
	}
}

func TestSyncPlansRemovalWithoutDryRunMutation(t *testing.T) {
	dir := t.TempDir()
	old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Sync(context.Background(), config(t, bundleWith(t, "plugin", "r2", map[string]string{"alpha": "keep"})), Options{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed() || strings.Join(report.Names(Removed), ",") != "beta" {
		t.Fatalf("report=%#v", report)
	}
	for _, change := range report.Changes {
		if change.Name == "beta" && change.Outcome != Planned {
			t.Fatalf("removal=%#v", change)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "beta")); err != nil {
		t.Fatalf("dry-run removed beta: %v", err)
	}
	stateAfter, err := os.ReadFile(statePath(dir))
	if err != nil || string(stateAfter) != string(stateBefore) {
		t.Fatalf("dry-run state changed err=%v", err)
	}
}

func TestSyncDoesNotPartiallyRemoveWhenAPluginSkillConflicts(t *testing.T) {
	dir := t.TempDir()
	old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta", "SKILL.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Sync(context.Background(), config(t, bundleWith(t, "plugin", "r2", map[string]string{"alpha": "updated"})), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("conflicts=%v report=%#v", got, report)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(data) != "keep" {
		t.Fatalf("alpha changed=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "beta", "SKILL.md")); err != nil || string(data) != "edited" {
		t.Fatalf("beta changed=%q err=%v", data, err)
	}
	installed, err := readState(dir)
	if err != nil || installed.Plugins[old.Plugin.String()].Revision != old.Source.Revision || len(installed.Plugins[old.Plugin.String()].Skills) != 2 {
		t.Fatalf("state=%#v err=%v", installed, err)
	}
}

func TestSyncRemovalFailureRestoresOriginal(t *testing.T) {
	dir := t.TempDir()
	old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		original := ops.rename
		ops.rename = func(root *os.Root, from, to string) error {
			if filepath.Base(from) == "beta" && strings.Contains(filepath.ToSlash(to), "/backup/") {
				return errors.New("removal backup failed")
			}
			return original(root, from, to)
		}
	})
	report, err := Sync(context.Background(), config(t, bundleWith(t, "plugin", "r2", map[string]string{"alpha": "keep"})), Options{Dir: dir})
	if err == nil {
		t.Fatal("expected removal failure")
	}
	for _, change := range report.Changes {
		if change.Name == "beta" && change.Outcome != Restored {
			t.Fatalf("removal outcome=%#v", change)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "beta", "SKILL.md")); err != nil || string(data) != "retire" {
		t.Fatalf("original beta=%q err=%v", data, err)
	}
}

func TestSyncRetiresAbsentSkillAndRefusesSymlinkedRemoval(t *testing.T) {
	newer := bundleWith(t, "plugin", "r2", map[string]string{"alpha": "keep"})
	t.Run("already absent", func(t *testing.T) {
		dir := t.TempDir()
		old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
		if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "beta")); err != nil {
			t.Fatal(err)
		}
		report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Removed), ",") != "beta" {
			t.Fatalf("report=%#v err=%v", report, err)
		}
		for _, change := range report.Changes {
			if change.Name == "beta" && change.Outcome != Applied {
				t.Fatalf("absent removal=%#v", change)
			}
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
		if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "beta")
		if err := os.Rename(filepath.Join(dir, "beta"), outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "beta")); err != nil {
			t.Fatal(err)
		}
		report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Conflict), ",") != "alpha,beta" {
			t.Fatalf("report=%#v err=%v", report, err)
		}
		info, err := os.Lstat(filepath.Join(dir, "beta"))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink changed info=%v err=%v", info, err)
		}
	})
}

func TestSyncRejectsUnsafeBundleAndStateBeforeTargetMutation(t *testing.T) {
	t.Run("bundle", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		bad := bundleWith(t, "plugin", "r1", map[string]string{transactionPrefix + "reserved": "bad"})
		_, err := Sync(context.Background(), config(t, bad), Options{Dir: target})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("invalid bundle created target: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(parent, ".cli-helpers-skills-lock")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("invalid bundle created lock: %v", err)
		}
	})
	t.Run("state", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		b := bundle(t, "plugin", "r1", "body")
		entry := pluginState{Revision: b.Source.Revision, Digest: b.Source.Digest, CLI: "strongo/tool", Suppliers: map[string]string{"strongo/tool": b.Source.Revision}, Source: b.Source, Skills: map[string]string{"alpha": b.Source.Digest}}
		for _, mutate := range []func(*state){
			func(s *state) {
				s.Plugins["strongo/"] = s.Plugins["strongo/plugin"]
				delete(s.Plugins, "strongo/plugin")
			},
			func(s *state) {
				s.Plugins["strongo/plugin"] = pluginState{Revision: entry.Revision, Digest: entry.Digest, CLI: entry.CLI, Suppliers: entry.Suppliers, Source: entry.Source, Skills: map[string]string{transactionPrefix + "reserved": entry.Digest}}
			},
			func(s *state) {
				s.Plugins["strongo/plugin"] = pluginState{Revision: entry.Revision, Digest: entry.Digest, CLI: entry.CLI, Suppliers: entry.Suppliers, Source: entry.Source, Skills: map[string]string{"alpha": strings.Repeat("z", 64)}}
			},
			func(s *state) {
				s.Plugins["strongo/plugin"] = pluginState{Revision: entry.Revision, Digest: entry.Digest, CLI: entry.CLI, Suppliers: map[string]string{"strongo/tool": "short"}, Source: entry.Source, Skills: entry.Skills}
			},
		} {
			t.Run("corrupt", func(t *testing.T) {
				s := state{Schema: stateSchema, Plugins: map[string]pluginState{"strongo/plugin": entry}}
				mutate(&s)
				if err := writeState(target, s); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(statePath(target))
				if err != nil {
					t.Fatal(err)
				}
				_, err = Sync(context.Background(), config(t, b), Options{Dir: target})
				if !errors.Is(err, ErrStateCorrupt) {
					t.Fatalf("err=%v", err)
				}
				after, err := os.ReadFile(statePath(target))
				if err != nil || string(after) != string(before) {
					t.Fatalf("state mutated err=%v", err)
				}
				if _, err := os.Lstat(filepath.Join(parent, ".cli-helpers-skills-lock")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("corrupt state created lock: %v", err)
				}
			})
		}
	})
}

func TestEmbeddedBundleRequiresStableSourceAndVersion(t *testing.T) {
	content := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("x")}}
	digest, err := Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EmbeddedBundle(BundleDescriptor{Plugin: PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: revisionForTest("embed"), Version: "1.0.0", Digest: digest}}, content)
	if err != nil {
		t.Fatal(err)
	}
	if b.Source.Digest != digest || b.Source.Repository == "" {
		t.Fatalf("bundle = %#v", b)
	}
	_, err = EmbeddedBundle(BundleDescriptor{Plugin: b.Plugin, Source: Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: revisionForTest("embed"), Version: "1.0.0", Digest: strings.Repeat("f", 64)}}, content)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestDigestRetainsLeadingDotRootFilesAndExecutableModes(t *testing.T) {
	hidden, err := Digest(fstest.MapFS{".hidden": &fstest.MapFile{Data: []byte("same")}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Digest(fstest.MapFS{"hidden": &fstest.MapFile{Data: []byte("same")}})
	if err != nil {
		t.Fatal(err)
	}
	if hidden == plain {
		t.Fatal("leading-dot path did not affect digest")
	}
	executable, err := Digest(fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}, "alpha/run": &fstest.MapFile{Data: []byte("run"), Mode: 0o755}})
	if err != nil {
		t.Fatal(err)
	}
	nonExecutable, err := Digest(fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}, "alpha/run": &fstest.MapFile{Data: []byte("run"), Mode: 0o644}})
	if err != nil {
		t.Fatal(err)
	}
	if executable == nonExecutable {
		t.Fatal("mode-only change did not affect digest")
	}
}

func TestEmbeddedRealExecutableFixtureUsesDescriptorModeMetadata(t *testing.T) {
	local := os.DirFS("testdata")
	declaredExecutables := []string{"executable-script"}
	localDigest, err := DigestWithExecutables(local, declaredExecutables)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := fs.Sub(executableFixture, "testdata")
	if err != nil {
		t.Fatal(err)
	}
	info, err := fs.Stat(embedded, "executable-script")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("embed unexpectedly retained source execute bit: %v", info.Mode())
	}
	embeddedDigest, err := DigestWithExecutables(embedded, declaredExecutables)
	if err != nil {
		t.Fatal(err)
	}
	if localDigest != embeddedDigest {
		t.Fatalf("local %s != embedded %s", localDigest, embeddedDigest)
	}
}

func TestEmbeddedBundleAuthenticatesDeclaredExecutablePaths(t *testing.T) {
	content := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")},
		"alpha/run":      &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
	}
	digest, err := DigestWithExecutables(content, []string{"alpha/run"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := BundleDescriptor{Plugin: PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: revisionForTest("exec"), Version: "1.0.0", Digest: digest}, ExecutablePaths: []string{"alpha/run"}}
	b, err := EmbeddedBundle(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.ExecutablePaths) != 1 || b.ExecutablePaths[0] != "alpha/run" {
		t.Fatalf("bundle = %#v", b)
	}
	_, err = EmbeddedBundle(BundleDescriptor{Plugin: descriptor.Plugin, Source: descriptor.Source}, content)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("missing executable metadata err = %v", err)
	}
	_, err = EmbeddedBundle(BundleDescriptor{Plugin: descriptor.Plugin, Source: descriptor.Source, ExecutablePaths: []string{"alpha/run", "alpha/run"}}, content)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("duplicate executable metadata err = %v", err)
	}
	_, err = EmbeddedBundle(BundleDescriptor{Plugin: descriptor.Plugin, Source: descriptor.Source, ExecutablePaths: []string{"../escape"}}, content)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("escaped executable metadata err = %v", err)
	}
}

func TestSyncRestoresEmbeddedExecutableMode(t *testing.T) {
	content := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")},
		"alpha/run":      &fstest.MapFile{Data: []byte("#!/bin/sh\nexit 0\n")},
	}
	digest, err := DigestWithExecutables(content, []string{"alpha/run"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := EmbeddedBundle(BundleDescriptor{Plugin: PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: revisionForTest("embedded-script"), Version: "1.0.0", Digest: digest}, ExecutablePaths: []string{"alpha/run"}}, content)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "alpha", "run"))
	if err != nil {
		t.Fatalf("installed mode = %v, %v", info.Mode(), err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v", info.Mode())
	}
}

func TestSourceRejectsInvalidRevisionAndVersion(t *testing.T) {
	b := bundle(t, "plugin", "r1", "body")
	for _, mutate := range []func(*Source){
		func(s *Source) { s.Revision = "short" },
		func(s *Source) { s.Revision = strings.Repeat("z", 40) },
		func(s *Source) { s.Version = "1.999999999999999999999999999999999999999999999999" },
		func(s *Source) { s.Version = "1.-1.0" },
	} {
		bad := b
		mutate(&bad.Source)
		if _, err := Sync(context.Background(), config(t, bad), Options{Dir: t.TempDir()}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestSourceAPIsValidateAndDescribeBundles(t *testing.T) {
	content := fstest.MapFS{
		"zeta/SKILL.md":  &fstest.MapFile{Data: []byte("z")},
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("a")},
		"missing/README": &fstest.MapFile{Data: []byte("missing skill")},
		"ignored.txt":    &fstest.MapFile{Data: []byte("ignored")},
	}
	discovered, err := Discover(content)
	if err != nil || len(discovered) != 2 || discovered[0].Name != "alpha" || discovered[1].Name != "zeta" || discovered[0].Digest == "" {
		t.Fatalf("discover=%#v err=%v", discovered, err)
	}
	if _, err := Discover(failingFS{err: errors.New("read root failed")}); err == nil {
		t.Fatal("expected read directory failure")
	}
	if _, err := Discover(fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Mode: fs.ModeDir}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("non-regular descriptor err=%v", err)
	}
	if _, err := Discover(fstest.MapFS{transactionPrefix + "reserved/SKILL.md": &fstest.MapFile{Data: []byte("bad")}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("reserved skill err=%v", err)
	}
	if _, err := Discover(fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("ok")}, "alpha/link": &fstest.MapFile{Mode: fs.ModeSymlink}}); err == nil {
		t.Fatal("expected unsafe subtree failure")
	}

	executable := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")},
		"alpha/run":      &fstest.MapFile{Data: []byte("run"), Mode: 0o755},
		"alpha/helper":   &fstest.MapFile{Data: []byte("helper")},
	}
	paths, err := NormalizeExecutablePaths(executable, []string{"alpha/helper"})
	if err != nil || strings.Join(paths, ",") != "alpha/helper,alpha/run" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	for _, declared := range [][]string{{"alpha/missing"}, {"alpha/helper", "alpha/helper"}, {"."}} {
		if _, err := NormalizeExecutablePaths(executable, declared); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("declared=%v err=%v", declared, err)
		}
	}

	b := bundle(t, "plugin", "r1", "body")
	descriptor := BundleDescriptor{Plugin: b.Plugin, Source: b.Source}
	if err := ValidateDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BundleDescriptor){
		func(d *BundleDescriptor) { d.Plugin.Name = "" },
		func(d *BundleDescriptor) { d.Source.Digest = strings.Repeat("z", 64) },
		func(d *BundleDescriptor) { d.Source.Compatibility.MinCLI = "bad" },
		func(d *BundleDescriptor) { d.ExecutablePaths = []string{".", "alpha/run"} },
		func(d *BundleDescriptor) { d.ExecutablePaths = []string{"alpha/run", "alpha/run"} },
	} {
		bad := descriptor
		mutate(&bad)
		if err := ValidateDescriptor(bad); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("descriptor=%#v err=%v", bad, err)
		}
	}
	if !Compatible("1.2.3", Compatibility{MinCLI: "1.0.0", MaxCLI: "2.0.0"}) || Compatible("unknown", Compatibility{MinCLI: "1.0.0"}) || Compatible("0.9.0", Compatibility{MinCLI: "1.0.0"}) || Compatible("2.1.0", Compatibility{MaxCLI: "2.0.0"}) {
		t.Fatal("unexpected compatibility result")
	}
	if cmp, err := CompareVersions("v1.2.3", "1.2.2"); err != nil || cmp != 1 {
		t.Fatalf("comparison=%d err=%v", cmp, err)
	}
	if _, err := CompareVersions("bad", "1.2.3"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid comparison err=%v", err)
	}
}

func TestSyncRejectsMalformedProvenanceBeforeWriting(t *testing.T) {
	for _, mutate := range []func(*Bundle){
		func(b *Bundle) { b.Plugin.Publisher = "bad/name" },
		func(b *Bundle) { b.Source.Repository = "" },
		func(b *Bundle) { b.Source.Path = "../escape" },
		func(b *Bundle) { b.Source.Revision = strings.Repeat("A", 40) },
		func(b *Bundle) { b.Source.Version = "1.02.3" },
		func(b *Bundle) { b.Source.Digest = strings.Repeat("z", 64) },
		func(b *Bundle) { b.Source.Compatibility.MaxCLI = "bad" },
	} {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		bad := bundle(t, "plugin", "r1", "body")
		mutate(&bad)
		_, err := Sync(context.Background(), config(t, bad), Options{Dir: target})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("bundle=%#v err=%v", bad, err)
		}
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("malformed provenance created target: %v", err)
		}
	}
}

func TestReleaseResolverRejectsMissingInputsAndPropagatesSourceFailure(t *testing.T) {
	matched := bundle(t, "plugin", "r1", "matched")
	if _, err := (ReleaseResolver{}).Resolve(context.Background(), matched); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing source err=%v", err)
	}
	if _, err := (ReleaseResolver{Source: releaseSourceFunc(func(context.Context, Source, string) (BundleDescriptor, fs.FS, error) {
		return BundleDescriptor{}, nil, nil
	})}).Resolve(context.Background(), matched); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing version err=%v", err)
	}
	sourceFailure := errors.New("source unavailable")
	resolver := ReleaseResolver{CurrentVersion: "1.2.3", Source: releaseSourceFunc(func(context.Context, Source, string) (BundleDescriptor, fs.FS, error) {
		return BundleDescriptor{}, nil, sourceFailure
	})}
	if _, err := resolver.Resolve(context.Background(), matched); !errors.Is(err, sourceFailure) {
		t.Fatalf("source failure err=%v", err)
	}
}

func TestSourceValidationFilesystemFailureBoundaries(t *testing.T) {
	base := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}}
	bundleForFS := func(source fs.FS) Bundle {
		t.Helper()
		digest, err := Digest(source)
		if err != nil {
			t.Fatal(err)
		}
		b := bundle(t, "plugin", "r1", "body")
		b.FS, b.Source.Digest = source, digest
		return b
	}

	withRootFile := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}, "README.md": &fstest.MapFile{Data: []byte("note")}}
	if skills, err := validateBundle(bundleForFS(withRootFile), "1.2.3"); err != nil || len(skills) != 1 {
		t.Fatalf("root-file bundle skills=%#v err=%v", skills, err)
	}
	withoutSkill := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}, "empty/README.md": &fstest.MapFile{Data: []byte("note")}}
	if skills, err := validateBundle(bundleForFS(withoutSkill), "1.2.3"); err != nil || len(skills) != 1 || skills[0].Name != "alpha" {
		t.Fatalf("missing-skill bundle skills=%#v err=%v", skills, err)
	}
	nonRegular := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}, "beta/SKILL.md": &fstest.MapFile{Mode: fs.ModeDir}}
	if _, err := validateBundle(bundleForFS(nonRegular), "1.2.3"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("non-regular skill err=%v", err)
	}
	noSkills := fstest.MapFS{"README.md": &fstest.MapFile{Data: []byte("note")}}
	if _, err := validateBundle(bundleForFS(noSkills), "1.2.3"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("no-skills err=%v", err)
	}
	unsafe := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")}, "alpha/link": &fstest.MapFile{Mode: fs.ModeSymlink}}
	b := bundle(t, "plugin", "r1", "body")
	b.FS, b.Source.Digest = unsafe, strings.Repeat("0", 64)
	if _, err := validateBundle(b, "1.2.3"); err == nil {
		t.Fatal("expected unsafe digest failure")
	}

	for _, failAt := range []int{3, 4, 5, 6} {
		t.Run(fmt.Sprintf("read-dir-%d", failAt), func(t *testing.T) {
			flaky := &failAfterReadDirFS{FS: base, failAt: failAt, err: errors.New("read directory failed")}
			b := bundleForFS(base)
			b.FS = flaky
			if _, err := validateBundle(b, "1.2.3"); err == nil {
				t.Fatal("expected read directory failure")
			}
		})
	}
	if _, err := executableSet(failingFS{err: errors.New("walk failed")}, nil); err == nil {
		t.Fatal("expected executable walk failure")
	}
	if _, err := executableSet(infoFailureFS{err: errors.New("entry info failed")}, nil); err == nil {
		t.Fatal("expected executable info failure")
	}
	if _, err := DigestWithExecutables(base, []string{"alpha/missing"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("digest executable error=%v", err)
	}
	if _, err := Digest(failingFS{err: errors.New("walk failed")}); err == nil {
		t.Fatal("expected digest walk failure")
	}
	if _, err := Digest(openFailureFS{FS: base, path: "alpha/SKILL.md", err: errors.New("read failed")}); err == nil {
		t.Fatal("expected digest read failure")
	}
	if _, err := Digest(infoFailureFS{err: errors.New("entry info failed")}); err == nil {
		t.Fatal("expected digest info failure")
	}
	if _, err := legacyWBDigest(failingFS{err: errors.New("walk failed")}, "."); err == nil {
		t.Fatal("expected legacy walk failure")
	}
	if _, err := legacyWBDigest(openFailureFS{FS: base, path: "alpha/SKILL.md", err: errors.New("read failed")}, "."); err == nil {
		t.Fatal("expected legacy read failure")
	}
	if _, err := legacyWBDigest(unsafe, "."); err == nil {
		t.Fatal("expected legacy unsafe-entry failure")
	}

	for _, version := range []string{"", "unknown", "1.2", "1..2", "1.02.3", "1.x.3", "1.2.18446744073709551616"} {
		if validVersion(version) {
			t.Fatalf("invalid version accepted: %q", version)
		}
	}
	if versionCompare("1", "1.0") != 0 || versionCompare("1", "1.0.1") != -1 || versionCompare("1.1", "1") != 1 {
		t.Fatal("version comparison normalization failed")
	}
}

func TestExplicitNewerCompatibleResolverReplacesMatchedBundle(t *testing.T) {
	matched := bundle(t, "plugin", "r1", "matched")
	newer := bundle(t, "plugin", "r2", "newer")
	newer.Source.Version = "1.1.0"
	resolver := ReleaseResolver{Source: releaseSourceFunc(func(_ context.Context, _ Source, _ string) (BundleDescriptor, fs.FS, error) {
		return BundleDescriptor{Plugin: newer.Plugin, Source: newer.Source, ExecutablePaths: newer.ExecutablePaths}, newer.FS, nil
	}), CurrentVersion: "1.2.3"}
	dir := t.TempDir()
	report, err := Sync(context.Background(), config(t, matched), Options{Dir: dir, PreferNewerCompatible: true, Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Added); len(got) != 1 {
		t.Fatalf("added = %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "newer" {
		t.Fatalf("content = %q", data)
	}
}

func TestReleaseResolverRejectsNonNewerOrForeignSource(t *testing.T) {
	matched := bundle(t, "plugin", "r1", "matched")
	for _, tc := range []struct {
		name   string
		source Source
	}{{"same version", matched.Source}, {"foreign source", func() Source {
		s := matched.Source
		s.Version = "1.1.0"
		s.Repository = "github.com/other/plugin"
		return s
	}()}} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := ReleaseResolver{CurrentVersion: "1.2.3", Source: releaseSourceFunc(func(context.Context, Source, string) (BundleDescriptor, fs.FS, error) {
				return BundleDescriptor{Plugin: matched.Plugin, Source: tc.source}, matched.FS, nil
			})}
			_, err := resolver.Resolve(context.Background(), matched)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestReleaseResolverKeepsMatchedBundleWhenNoNewerCompatibleReleaseExists(t *testing.T) {
	matched := bundle(t, "plugin", "r1", "matched")
	resolver := ReleaseResolver{CurrentVersion: "1.2.3", Source: releaseSourceFunc(func(context.Context, Source, string) (BundleDescriptor, fs.FS, error) {
		return BundleDescriptor{}, nil, ErrNoNewerCompatible
	})}
	resolved, err := resolver.Resolve(context.Background(), matched)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != matched.Source {
		t.Fatalf("resolved = %#v, want matched bundle", resolved)
	}
	data, err := fs.ReadFile(resolved.FS, "alpha/SKILL.md")
	if err != nil || string(data) != "matched" {
		t.Fatalf("resolved content = %q, %v", data, err)
	}
}

func TestBoundedCompatibilityRefusesUnknownBuildVersion(t *testing.T) {
	b := bundle(t, "plugin", "r1", "body")
	b.Source.Compatibility = Compatibility{MinCLI: "1.0.0", MaxCLI: "2.0.0"}
	cfg := config(t, b)
	cfg.CurrentVersion = "(devel)"
	_, err := Sync(context.Background(), cfg, Options{Dir: t.TempDir()})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestConcurrentSyncSerializesOneTarget(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "r1", "body")
	cfg := config(t, b)
	start := make(chan struct{})
	reports := make(chan Report, 2)
	failures := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			report, err := Sync(context.Background(), cfg, Options{Dir: dir, LockTimeout: time.Second})
			if err != nil {
				failures <- err
				return
			}
			reports <- report
		}()
	}
	close(start)
	for range 2 {
		select {
		case err := <-failures:
			t.Fatal(err)
		case <-reports:
		}
	}
	state, err := readState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Plugins) != 1 {
		t.Fatalf("plugins = %#v", state.Plugins)
	}
}

func TestLockSharesRelativeAndAbsoluteTargetIdentity(t *testing.T) {
	root, err := os.MkdirTemp(".", "skillsync-lock-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "skills")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lock(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := lock(ctx, rel, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("relative alias lock error = %v", err)
	}
}

func TestReadStatusDoesNotTraverseSkills(t *testing.T) {
	dir := t.TempDir()
	status, err := ReadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed {
		t.Fatal("missing marker reported installed")
	}
	b := bundle(t, "plugin", "r1", "body")
	if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	status, err = ReadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.Plugins[b.Plugin.String()].Digest != b.Source.Digest {
		t.Fatalf("status = %#v", status)
	}
}

func TestStateMarkerRejectsCorruptOwnershipAndPreservesStatusSafety(t *testing.T) {
	b := bundle(t, "plugin", "r1", "body")
	entry := pluginState{Revision: b.Source.Revision, Digest: b.Source.Digest, CLI: "strongo/tool", Suppliers: map[string]string{"strongo/tool": b.Source.Revision}, Source: b.Source, Skills: map[string]string{"alpha": strings.Repeat("a", 64)}}
	writeRawState := func(t *testing.T, dir string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath(dir), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("normalizes absent maps", func(t *testing.T) {
		dir := t.TempDir()
		writeRawState(t, dir, state{Schema: stateSchema})
		loaded, err := readState(dir)
		if err != nil || loaded.Plugins == nil {
			t.Fatalf("state=%#v err=%v", loaded, err)
		}
		writeRawState(t, dir, state{Schema: stateSchema, Plugins: map[string]pluginState{"strongo/plugin": {Revision: entry.Revision, Digest: entry.Digest, CLI: entry.CLI, Source: entry.Source, Skills: entry.Skills}}})
		loaded, err = readState(dir)
		if err != nil || loaded.Plugins["strongo/plugin"].Suppliers[entry.CLI] != entry.Revision {
			t.Fatalf("legacy supplier normalization=%#v err=%v", loaded, err)
		}
	})
	for _, mutate := range []func(*state){
		func(s *state) { s.Schema = 0 },
		func(s *state) { s.Schema = stateSchema + 1 },
		func(s *state) { s.Plugins["bad/"] = s.Plugins["strongo/plugin"]; delete(s.Plugins, "strongo/plugin") },
		func(s *state) {
			s.Plugins["strongo/plugin"] = pluginState{Legacy: true, Revision: "unexpected", Skills: entry.Skills}
		},
		func(s *state) { p := s.Plugins["strongo/plugin"]; p.CLI = "bad/"; s.Plugins["strongo/plugin"] = p },
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.Source.Revision = revisionForTest("different")
			s.Plugins["strongo/plugin"] = p
		},
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.Suppliers = map[string]string{"strongo/tool": "short"}
			s.Plugins["strongo/plugin"] = p
		},
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.Suppliers = map[string]string{"strongo/tool": revisionForTest("different supplier")}
			s.Plugins["strongo/plugin"] = p
		},
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.Skills = map[string]string{"alpha": strings.Repeat("z", 64)}
			s.Plugins["strongo/plugin"] = p
		},
		func(s *state) { s.Plugins["other/plugin"] = entry },
	} {
		t.Run("invalid", func(t *testing.T) {
			dir := t.TempDir()
			s := state{Schema: stateSchema, Plugins: map[string]pluginState{"strongo/plugin": entry}}
			mutate(&s)
			writeRawState(t, dir, s)
			if _, err := readState(dir); !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("state=%#v err=%v", s, err)
			}
			if _, err := ReadStatus(dir); !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("status err=%v", err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, statePath(dir)); err != nil {
			t.Fatal(err)
		}
		if _, err := readState(dir); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("symlink err=%v", err)
		}
	})
}

func TestLegacyImportFailsClosedBeforePublishingOwnership(t *testing.T) {
	b := bundle(t, "plugin", "r1", "body")
	legacy := LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: b.Plugin}
	if _, err := importLegacy(t.TempDir(), LegacyImport{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid input err=%v", err)
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  error
	}{
		{name: "missing", setup: func(*testing.T, string) {}, want: fs.ErrNotExist},
		{name: "invalid-json", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, legacy.MarkerFile), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: ErrStateCorrupt},
		{name: "invalid-schema", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, legacy.MarkerFile), []byte(`{"schema_version":2,"skills":{"alpha":"x"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: ErrStateCorrupt},
		{name: "reserved-name", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, legacy.MarkerFile), []byte(`{"schema_version":1,"skills":{".cli-helpers-skills-recovery.json":"x"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: ErrStateCorrupt},
		{name: "missing-skill", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, legacy.MarkerFile), []byte(`{"schema_version":1,"skills":{"alpha":"x"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: ErrStateCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			if _, err := importLegacy(dir, legacy); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if _, err := os.Lstat(statePath(dir)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("legacy import wrote state: %v", err)
			}
		})
	}
	// A symlinked marker must never be followed, even when its content looks valid.
	dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(outside, []byte(`{"schema_version":1,"skills":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, legacy.MarkerFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := importLegacy(dir, legacy); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("symlink marker err=%v", err)
	}
}

func TestRecoveryRollsBackInterruptedReplacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "alpha")
	txDir := filepath.Join(dir, transactionPrefix+"interrupted")
	backup := filepath.Join(txDir, "backup", "alpha")
	for path, body := range map[string]string{filepath.Join(target, "SKILL.md"): "new", filepath.Join(backup, "SKILL.md"): "old"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, err := subtreeDigest(os.DirFS(filepath.Join(txDir, "backup")), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	new, err := installedDigest(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(recoveryJournal{Schema: 2, ID: "interrupted", Transaction: transactionPrefix + "interrupted", Changes: []recoveryChange{{Name: "alpha", Old: old, New: new, Existed: true, Phase: "published"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recoveryFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeState(dir, state{Plugins: map[string]pluginState{"strongo/plugin": {Legacy: true, Skills: map[string]string{"alpha": old}}}}); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("content = %q", data)
	}
}

func TestRecoveryRejectsEscapingJournalWithoutTouchingFiles(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("outside = %q", data)
	}
}

func TestRecoveryFailsClosedWhenStateIsCorrupt(t *testing.T) {
	dir := t.TempDir()
	txDir := filepath.Join(dir, transactionPrefix+"corrupt-state")
	for path, body := range map[string]string{
		filepath.Join(dir, "alpha", "SKILL.md"):             "new",
		filepath.Join(txDir, "backup", "alpha", "SKILL.md"): "old",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, err := subtreeDigest(os.DirFS(filepath.Join(txDir, "backup")), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	new, err := installedDigest(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(recoveryJournal{Schema: 2, ID: "corrupt-state", Transaction: transactionPrefix + "corrupt-state", Changes: []recoveryChange{{Name: "alpha", Old: old, New: new, Existed: true, Phase: "published"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recoveryFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "new" {
		t.Fatalf("target = %q, %v", data, err)
	}
}

func TestRecoveryRejectsSymlinkedTransactionDirectory(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := "outside"
	raw, err := json.Marshal(recoveryJournal{Schema: 2, ID: id, Transaction: transactionPrefix + id, Changes: []recoveryChange{{Name: "alpha", Old: strings.Repeat("a", 64), New: strings.Repeat("b", 64), Existed: true, Phase: "prepared"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recoveryFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, transactionPrefix+id)); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "keep"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("outside = %q, %v", data, err)
	}
}

func TestRecoveryWillNotDeleteAnUnmanagedMatchingAddedTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("unmanaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := installedDigest(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	id := "unmanaged-add"
	raw, err := json.Marshal(recoveryJournal{Schema: 2, ID: id, Transaction: transactionPrefix + id, Changes: []recoveryChange{{Name: "alpha", New: digest, Phase: "published"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recoveryFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "unmanaged" {
		t.Fatalf("unmanaged target = %q, %v", data, err)
	}
}

type releaseSourceFunc func(context.Context, Source, string) (BundleDescriptor, fs.FS, error)

func (f releaseSourceFunc) NewerCompatible(ctx context.Context, source Source, version string) (BundleDescriptor, fs.FS, error) {
	return f(ctx, source, version)
}

func TestSyncRejectsDigestMismatchAndSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "r1", "first")
	b.Source.Digest = strings.Repeat("f", 64)
	if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err = %v", err)
	}
	b = bundle(t, "plugin", "r1", "first")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "alpha")); err != nil {
		t.Fatal(err)
	}
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 {
		t.Fatalf("conflict = %v", got)
	}
}

func TestSyncTwoPluginsCannotClaimSameSkill(t *testing.T) {
	dir := t.TempDir()
	first := bundle(t, "first", "r1", "first")
	if _, err := Sync(context.Background(), config(t, first), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	second := bundle(t, "second", "r1", "second")
	report, err := Sync(context.Background(), config(t, second), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 {
		t.Fatalf("conflict = %v", got)
	}
}

func TestDuplicateDesiredNamePlansConflictBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	first := bundle(t, "first", "r1", "first")
	second := bundle(t, "second", "r1", "second")
	cfg := Config{CLI: Identity{Publisher: "strongo", Name: "tool"}, CurrentVersion: "1.2.3", Bundles: []Bundle{first, second}}
	dry, err := Sync(context.Background(), cfg, Options{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := Sync(context.Background(), cfg, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := dry.Names(Conflict); len(got) != 2 {
		t.Fatalf("dry conflicts = %v", got)
	}
	if got := actual.Names(Conflict); len(got) != 2 {
		t.Fatalf("actual conflicts = %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("duplicate plan wrote target: %v", err)
	}
}

func TestSyncSoleSupplierCanAdvancePluginRevision(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	newer := bundle(t, "plugin", "r2", "new")
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Updated); len(got) != 1 {
		t.Fatalf("updated = %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
}

func TestSyncRejectsDivergentRevisionFromAnotherSupplier(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	other := config(t, old)
	other.CLI = Identity{Publisher: "strongo", Name: "other-tool"}
	if _, err := Sync(context.Background(), other, Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	newer := bundle(t, "plugin", "r2", "new")
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 {
		t.Fatalf("conflicts = %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("content = %q", data)
	}
}

func TestSyncRejectsConflictingImmutableSourceFromAnotherSupplier(t *testing.T) {
	dir := t.TempDir()
	fromA := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "bytes from CLI A", "beta": "also from CLI A"})
	fromB := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "different bytes from CLI B", "beta": "also different from CLI B"})
	cliA := config(t, fromA)
	cliA.CLI = Identity{Publisher: "strongo", Name: "cli-a"}
	cliB := config(t, fromB)
	cliB.CLI = Identity{Publisher: "strongo", Name: "cli-b"}
	if _, err := Sync(context.Background(), cliA, Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	report, err := Sync(context.Background(), cliB, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(report.Names(Conflict), ","); got != "alpha,beta" {
		t.Fatalf("conflicting source report=%#v", report)
	}
	for _, change := range report.Changes {
		if change.Reason != "plugin immutable source already owned by another CLI" {
			t.Fatalf("source conflict reason=%#v", change)
		}
	}
	for name, want := range map[string]string{"alpha": "bytes from CLI A", "beta": "also from CLI A"} {
		data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
		if err != nil || string(data) != want {
			t.Fatalf("CLI B changed CLI A %s target: %q, %v", name, data, err)
		}
	}
	installed, err := readState(dir)
	if err != nil || installed.Plugins[fromA.Plugin.String()].Source != fromA.Source || len(installed.Plugins[fromA.Plugin.String()].Suppliers) != 1 {
		t.Fatalf("CLI B changed CLI A ownership: %#v, %v", installed, err)
	}
	if _, err := Sync(context.Background(), cliA, Options{Dir: dir}); err != nil {
		t.Fatalf("CLI A retry: %v", err)
	}
	for name, want := range map[string]string{"alpha": "bytes from CLI A", "beta": "also from CLI A"} {
		data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
		if err != nil || string(data) != want {
			t.Fatalf("CLI A retry changed %s target: %q, %v", name, data, err)
		}
	}
}

func TestSyncRejectsCompatibilityChangeFromAnotherSupplier(t *testing.T) {
	dir := t.TempDir()
	matched := bundle(t, "plugin", "r1", "same bytes")
	first := config(t, matched)
	first.CLI = Identity{Publisher: "strongo", Name: "cli-a"}
	if _, err := Sync(context.Background(), first, Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	other := config(t, matched)
	other.CLI = Identity{Publisher: "strongo", Name: "cli-b"}
	other.Bundles[0].Source.Compatibility = Compatibility{MinCLI: "1.0.0", MaxCLI: "2.0.0"}
	report, err := Sync(context.Background(), other, Options{Dir: dir})
	if err != nil || strings.Join(report.Names(Conflict), ",") != "alpha" {
		t.Fatalf("compatibility conflict report=%#v err=%v", report, err)
	}
}

func TestSyncDoesNotClaimOrPartiallyApplyAConflictedPluginRevision(t *testing.T) {
	dir := t.TempDir()
	old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "old alpha", "beta": "old beta"})
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("user edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	newer := bundleWith(t, "plugin", "r2", map[string]string{"alpha": "new alpha", "beta": "new beta"})
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 2 {
		t.Fatalf("conflicts = %v", got)
	}
	beta, err := os.ReadFile(filepath.Join(dir, "beta", "SKILL.md"))
	if err != nil || string(beta) != "old beta" {
		t.Fatalf("beta = %q, %v", beta, err)
	}
	status, err := ReadStatus(dir)
	if err != nil || status.Plugins[old.Plugin.String()].Revision != old.Source.Revision {
		t.Fatalf("status = %#v, %v", status, err)
	}
}

func TestSyncImportsOnlyValidatedLegacyOwnership(t *testing.T) {
	dir := t.TempDir()
	b := bundle(t, "plugin", "r1", "first")
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "reference.md"), []byte("reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyDigest, err := legacyWBDigest(b.FS, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(map[string]any{"schema_version": 1, "skills": map[string]string{"alpha": legacyDigest}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wb-skills-sync.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir, Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: b.Plugin}})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Unchanged); len(got) != 1 {
		t.Fatalf("unchanged = %v", got)
	}
}

func TestSyncWithMissingLegacyMarkerStartsFresh(t *testing.T) {
	b := bundle(t, "plugin", "r1", "body")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: t.TempDir(), Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: b.Plugin}})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Added); len(got) != 1 {
		t.Fatalf("added = %v", got)
	}
}

func withTransactionOperations(t *testing.T, mutate func(*transactionOperationSet)) {
	t.Helper()
	previous := transactionOperations
	t.Cleanup(func() { transactionOperations = previous })
	mutate(&transactionOperations)
}

func TestSyncRecordFailurePreservesOriginalAndReportsIncomplete(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		original := ops.rename
		ops.rename = func(root *os.Root, from, to string) error {
			if filepath.Base(to) == recoveryFileName {
				return errors.New("journal write failed")
			}
			return original(root, from, to)
		}
	})
	report, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), Options{Dir: dir})
	if err == nil || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "old" {
		t.Fatalf("original = %q, %v", data, err)
	}
}

func TestSyncPublishFailureRestoresOriginalAndReportsRestored(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		original := ops.rename
		ops.rename = func(root *os.Root, from, to string) error {
			if strings.Contains(filepath.ToSlash(from), "/stage/") {
				return errors.New("publish failed")
			}
			return original(root, from, to)
		}
	})
	report, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), Options{Dir: dir})
	if err == nil || report.Changes[0].Outcome != Restored {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "old" {
		t.Fatalf("original = %q, %v", data, err)
	}
}

func TestSyncRollbackFailurePreservesBackupAndReportsIncomplete(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		original := ops.rename
		ops.rename = func(root *os.Root, from, to string) error {
			if strings.Contains(filepath.ToSlash(from), "/backup/") {
				return errors.New("restore failed")
			}
			if strings.Contains(filepath.ToSlash(from), "/stage/") {
				return errors.New("publish failed")
			}
			return original(root, from, to)
		}
	})
	report, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), Options{Dir: dir})
	if err == nil || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	entries, readErr := filepath.Glob(filepath.Join(dir, transactionPrefix+"*", "backup", "alpha", "SKILL.md"))
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("backup paths = %v, %v", entries, readErr)
	}
	data, readErr := os.ReadFile(entries[0])
	if readErr != nil || string(data) != "old" {
		t.Fatalf("backup = %q, %v", data, readErr)
	}
}

func TestSyncStateFailureReportsRollbackFailureAndLeavesRecoveryEvidence(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	newer := bundle(t, "plugin", "r2", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	previous := transactionOperations
	transactionOperations.rename = func(root *os.Root, from, to string) error {
		if filepath.Base(to) == StateFileName {
			return errors.New("state write failed")
		}
		if strings.Contains(filepath.ToSlash(from), "/backup/") {
			return errors.New("restore failed")
		}
		return previous.rename(root, from, to)
	}
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	transactionOperations = previous
	if err == nil || !strings.Contains(err.Error(), "rollback") || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("missing recovery evidence: %v", err)
	}
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir}); err != nil {
		t.Fatalf("recovery through new public Sync: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "new" {
		t.Fatalf("final content = %q, %v", data, err)
	}
}

func TestSyncRetainsPublishedStateAndFilesWhenFinalDirectorySyncFails(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	newer := bundle(t, "plugin", "r2", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	previous := stateDirectorySync
	t.Cleanup(func() { stateDirectorySync = previous })
	stateDirectorySync = func(string) error { return errors.New("state directory sync failed") }
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err == nil || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if readErr != nil || string(data) != "new" {
		t.Fatalf("target=%q err=%v", data, readErr)
	}
	installed, readErr := readState(dir)
	if readErr != nil || installed.Plugins[newer.Plugin.String()].Revision != newer.Source.Revision || installed.RecoveryID == "" {
		t.Fatalf("state=%#v err=%v", installed, readErr)
	}
	if _, readErr := os.Lstat(filepath.Join(dir, recoveryFileName)); readErr != nil {
		t.Fatalf("missing recovery journal: %v", readErr)
	}
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir}); err == nil {
		t.Fatal("recovery accepted an undurable state marker")
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("recovery discarded evidence after persistence retry failure: %v", err)
	}
	stateDirectorySync = previous
	retry, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil || strings.Join(retry.Names(Unchanged), ",") != "alpha" {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("journal remains after public retry: %v", err)
	}
}

func TestSyncDryRunRefusesPendingRecoveryWithoutMutating(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	newer := bundle(t, "plugin", "r2", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSyncCrashChild$", "--", "skillsync-crash", "publish", dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash child: %v\n%s", err, output)
	}
	before, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(before) != "new" {
		t.Fatalf("published target=%q err=%v", before, err)
	}
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir, DryRun: true}); !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("dry-run err=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(after) != "new" {
		t.Fatalf("dry-run mutated target=%q err=%v", after, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("dry-run mutated journal: %v", err)
	}
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir}); err != nil {
		t.Fatalf("normal Sync recovery: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("journal remains after recovery: %v", err)
	}
}

func TestSyncDryRunRejectsCorruptRecoveryJournal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, recoveryFileName), []byte("not a journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r1", "body")), Options{Dir: dir, DryRun: true})
	if !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("dry-run err=%v", err)
	}
}

func TestSyncCommitCleanupFailureReturnsErrorAndNewSyncFinishesCleanup(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	newer := bundle(t, "plugin", "r2", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	previous := transactionOperations
	transactionOperations.removeAll = func(root *os.Root, path string) error {
		if strings.HasPrefix(filepath.Base(path), transactionPrefix) {
			return errors.New("cleanup failed")
		}
		return previous.removeAll(root, path)
	}
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	transactionOperations = previous
	if err == nil || report.Changes[0].Outcome != Applied {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("missing committed recovery journal: %v", err)
	}
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir}); err != nil {
		t.Fatalf("cleanup retry through new public Sync: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("journal remains after cleanup retry: %v", err)
	}
}

func TestSyncCrashChild(t *testing.T) {
	args := os.Args
	marker := -1
	for i, arg := range args {
		if arg == "skillsync-crash" {
			marker = i
			break
		}
	}
	legacy := false
	if marker < 0 {
		for i, arg := range args {
			if arg == "skillsync-legacy-crash" {
				marker, legacy = i, true
				break
			}
		}
		if marker < 0 {
			return
		}
	}
	if marker+2 >= len(args) {
		t.Fatal("missing crash child arguments")
	}
	boundary, dir := args[marker+1], args[marker+2]
	transactionBoundary = func(actual string) {
		if actual == boundary {
			os.Exit(0)
		}
	}
	opts := Options{Dir: dir}
	if legacy {
		opts.Legacy = LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: PluginIdentity{Publisher: "strongo", Name: "plugin"}}
	}
	_, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), opts)
	t.Fatalf("child did not exit at %s: %v", boundary, err)
}

func TestSyncRemovalCrashChild(t *testing.T) {
	args := os.Args
	marker := -1
	for i, arg := range args {
		if arg == "skillsync-removal-crash" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	if marker+2 >= len(args) {
		t.Fatal("missing removal crash child arguments")
	}
	boundary, dir := args[marker+1], args[marker+2]
	transactionBoundary = func(actual string) {
		if actual == boundary {
			os.Exit(0)
		}
	}
	_, err := Sync(context.Background(), config(t, bundleWith(t, "plugin", "r2", map[string]string{"alpha": "keep"})), Options{Dir: dir})
	t.Fatalf("child did not exit at %s: %v", boundary, err)
}

func TestNewPublicSyncRecoversEveryAbruptRemovalBoundary(t *testing.T) {
	for _, boundary := range []string{"journal", "backup", "state"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			old := bundleWith(t, "plugin", "r1", map[string]string{"alpha": "keep", "beta": "retire"})
			if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSyncRemovalCrashChild$", "--", "skillsync-removal-crash", boundary, dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash child: %v\n%s", err, output)
			}
			report, err := Sync(context.Background(), config(t, bundleWith(t, "plugin", "r2", map[string]string{"alpha": "keep"})), Options{Dir: dir})
			if err != nil || (boundary != "state" && strings.Join(report.Names(Removed), ",") != "beta") {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "beta")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("retired skill remains after recovery: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("recovery journal remains: %v", err)
			}
		})
	}
}

func TestLegacyUpgradeCrashPersistsRecoverableOwnershipFirst(t *testing.T) {
	for _, boundary := range []string{"backup", "publish"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			old := bundle(t, "plugin", "r1", "old")
			for path, body := range map[string]string{"SKILL.md": "old", "reference.md": "reference"} {
				if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "alpha", path), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			legacyDigest, err := legacyWBDigest(old.FS, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			marker, err := json.Marshal(map[string]any{"schema_version": 1, "skills": map[string]string{"alpha": legacyDigest}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".wb-skills-sync.json"), marker, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSyncCrashChild$", "--", "skillsync-legacy-crash", boundary, dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash child: %v\n%s", err, output)
			}
			opts := Options{Dir: dir, Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: old.Plugin}}
			if _, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), opts); err != nil {
				t.Fatalf("new public Sync recovery: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
			if err != nil || string(data) != "new" {
				t.Fatalf("content = %q, %v", data, err)
			}
		})
	}
}

func TestLegacyMarkerPublicationFailureDoesNotMoveOriginal(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "r1", "old")
	if err := os.MkdirAll(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "reference.md"), []byte("reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyDigest, err := legacyWBDigest(old.FS, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(map[string]any{"schema_version": 1, "skills": map[string]string{"alpha": legacyDigest}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wb-skills-sync.json"), marker, 0o644); err != nil {
		t.Fatal(err)
	}
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		original := ops.rename
		ops.rename = func(root *os.Root, from, to string) error {
			if filepath.Base(to) == StateFileName {
				return errors.New("state marker failed")
			}
			return original(root, from, to)
		}
	})
	_, err = Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), Options{Dir: dir, Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: old.Plugin}})
	if err == nil {
		t.Fatal("expected legacy ownership publication failure")
	}
	data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "old" {
		t.Fatalf("original = %q, %v", data, err)
	}
}

func TestNewPublicSyncRecoversEveryAbruptTransactionBoundary(t *testing.T) {
	for _, boundary := range []string{"journal", "backup", "publish", "state"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			old := bundle(t, "plugin", "r1", "old")
			if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSyncCrashChild$", "--", "skillsync-crash", boundary, dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash child: %v\n%s", err, output)
			}
			report, err := Sync(context.Background(), config(t, bundle(t, "plugin", "r2", "new")), Options{Dir: dir})
			if err != nil {
				t.Fatalf("new public Sync: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
			if err != nil || string(data) != "new" {
				t.Fatalf("final content = %q, %v", data, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("recovery journal remains: %v; report=%#v", err, report)
			}
		})
	}
}

type failingFS struct{ err error }

func (f failingFS) Open(string) (fs.File, error) { return nil, f.err }

type failAfterReadDirFS struct {
	fs.FS
	failAt, calls int
	err           error
}

func (f *failAfterReadDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, f.err
	}
	return fs.ReadDir(f.FS, name)
}

type openFailureFS struct {
	fs.FS
	path string
	err  error
}

func (f openFailureFS) Open(name string) (fs.File, error) {
	if name == f.path {
		return nil, f.err
	}
	return f.FS.Open(name)
}

type infoFailureFS struct{ err error }

func (f infoFailureFS) Open(name string) (fs.File, error) {
	switch name {
	case ".":
		return infoFailureFile{}, nil
	case "broken":
		return &infoFailureContentFile{Reader: *strings.NewReader("content")}, nil
	default:
		return nil, fs.ErrNotExist
	}
}
func (f infoFailureFS) ReadDir(string) ([]fs.DirEntry, error) {
	return []fs.DirEntry{infoFailureEntry(f)}, nil
}

type infoFailureFile struct{}

func (infoFailureFile) Stat() (fs.FileInfo, error) { return infoFailureInfo{}, nil }
func (infoFailureFile) Read([]byte) (int, error)   { return 0, io.EOF }
func (infoFailureFile) Close() error               { return nil }

type infoFailureContentFile struct{ strings.Reader }

func (f *infoFailureContentFile) Stat() (fs.FileInfo, error) { return infoFailureRegularInfo{}, nil }
func (*infoFailureContentFile) Close() error                 { return nil }

type infoFailureInfo struct{}

func (infoFailureInfo) Name() string       { return "." }
func (infoFailureInfo) Size() int64        { return 0 }
func (infoFailureInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (infoFailureInfo) ModTime() time.Time { return time.Time{} }
func (infoFailureInfo) IsDir() bool        { return true }
func (infoFailureInfo) Sys() any           { return nil }

type infoFailureRegularInfo struct{}

func (infoFailureRegularInfo) Name() string       { return "broken" }
func (infoFailureRegularInfo) Size() int64        { return int64(len("content")) }
func (infoFailureRegularInfo) Mode() fs.FileMode  { return 0o644 }
func (infoFailureRegularInfo) ModTime() time.Time { return time.Time{} }
func (infoFailureRegularInfo) IsDir() bool        { return false }
func (infoFailureRegularInfo) Sys() any           { return nil }

type infoFailureEntry struct{ err error }

func (infoFailureEntry) Name() string                 { return "broken" }
func (infoFailureEntry) IsDir() bool                  { return false }
func (infoFailureEntry) Type() fs.FileMode            { return 0 }
func (e infoFailureEntry) Info() (fs.FileInfo, error) { return nil, e.err }
