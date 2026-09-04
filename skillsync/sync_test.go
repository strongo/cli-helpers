package skillsync

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	localDigest, err := Digest(local)
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
	embeddedDigest, err := DigestWithExecutables(embedded, []string{"executable-script"})
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
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v, %v", info.Mode(), err)
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

func withTransactionOperations(t *testing.T, mutate func(*struct {
	rename    func(*os.Root, string, string) error
	removeAll func(*os.Root, string) error
})) {
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
	withTransactionOperations(t, func(ops *struct {
		rename    func(*os.Root, string, string) error
		removeAll func(*os.Root, string) error
	}) {
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
	withTransactionOperations(t, func(ops *struct {
		rename    func(*os.Root, string, string) error
		removeAll func(*os.Root, string) error
	}) {
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
	withTransactionOperations(t, func(ops *struct {
		rename    func(*os.Root, string, string) error
		removeAll func(*os.Root, string) error
	}) {
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
	withTransactionOperations(t, func(ops *struct {
		rename    func(*os.Root, string, string) error
		removeAll func(*os.Root, string) error
	}) {
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
