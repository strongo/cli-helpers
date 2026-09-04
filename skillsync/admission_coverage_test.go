package skillsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type resolverFunc func(context.Context, Bundle) (Bundle, error)

func (f resolverFunc) Resolve(ctx context.Context, bundle Bundle) (Bundle, error) {
	return f(ctx, bundle)
}

// cancellationAfterChecks cancels at a deterministic syncLocked context check.
type cancellationAfterChecks struct {
	calls, cancelOn int
}

func (c *cancellationAfterChecks) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancellationAfterChecks) Done() <-chan struct{}       { return nil }
func (c *cancellationAfterChecks) Err() error {
	c.calls++
	if c.calls >= c.cancelOn {
		return context.Canceled
	}
	return nil
}
func (c *cancellationAfterChecks) Value(any) any { return nil }

func reportForPrepared(p Prepared, dir string) Report {
	report := Report{Dir: dir, CLI: p.cfg.CLI, CLIVersion: p.cfg.CurrentVersion}
	for _, bundle := range p.bundles {
		report.Bundles = append(report.Bundles, ResolvedBundle{Plugin: bundle.Bundle.Plugin, Source: bundle.Bundle.Source})
	}
	return report
}

func TestAdmissionPreparedSyncRejectsInvalidTargetsAndStalePreparation(t *testing.T) {
	b := bundle(t, "plugin", "prepared", "body")
	p, err := Prepare(context.Background(), config(t, b), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Sync(context.Background(), Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty target err=%v", err)
	}

	stale := p
	stale.bundles = append([]resolvedBundle(nil), p.bundles...)
	stale.bundles[0].Bundle.FS = fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("changed")}}
	if _, err := stale.Sync(context.Background(), Options{Dir: t.TempDir()}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("stale prepared bundle err=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Sync(canceled, Options{Dir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepared sync err=%v", err)
	}

	t.Run("dry run rejects unsafe ancestry and pending recovery", func(t *testing.T) {
		outside := t.TempDir()
		link := filepath.Join(t.TempDir(), "target")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Sync(context.Background(), Options{Dir: link, DryRun: true}); err == nil {
			t.Fatal("dry run accepted symlinked target")
		}

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, recoveryFileName), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Sync(context.Background(), Options{Dir: dir, DryRun: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("dry run recovery err=%v", err)
		}
	})

	t.Run("apply rejects unsafe ancestry before lock and symlinked lock", func(t *testing.T) {
		outside := t.TempDir()
		link := filepath.Join(t.TempDir(), "target")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Sync(context.Background(), Options{Dir: link}); err == nil {
			t.Fatal("apply accepted symlinked target")
		}

		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Symlink(outside, filepath.Join(parent, ".cli-helpers-skills-lock")); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Sync(context.Background(), Options{Dir: target}); err == nil {
			t.Fatal("apply accepted symlinked lock")
		}
	})
}

func TestAdmissionResolutionRevalidationAndReportHelpers(t *testing.T) {
	b := bundle(t, "plugin", "resolution", "body")
	cfg := config(t, b)
	resolverErr := errors.New("resolver unavailable")
	if _, err := resolvedBundles(context.Background(), cfg, Options{PreferNewerCompatible: true, Resolver: resolverFunc(func(context.Context, Bundle) (Bundle, error) {
		return Bundle{}, resolverErr
	})}); !errors.Is(err, resolverErr) {
		t.Fatalf("resolver failure err=%v", err)
	}
	if _, err := resolvedBundles(context.Background(), Config{CLI: cfg.CLI, CurrentVersion: cfg.CurrentVersion, Bundles: []Bundle{b, b}}, Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("duplicate plugin err=%v", err)
	}
	if _, err := revalidatePrepared(cfg, []resolvedBundle{{Bundle: Bundle{Plugin: b.Plugin, Source: b.Source, FS: fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("changed")}}}}}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("revalidation err=%v", err)
	}

	changes := []Change{{Plugin: b.Plugin, Name: "alpha", Action: Added}, {Plugin: b.Plugin, Name: "alpha", Action: Updated}}
	if got := actionFor(changes, b.Plugin.String(), "alpha"); got != Updated {
		t.Fatalf("latest action=%q", got)
	}
	if got := actionFor(changes, b.Plugin.String(), "missing"); got != Conflict {
		t.Fatalf("missing action=%q", got)
	}

	entry := contractPluginState(t)
	base := state{Plugins: map[string]pluginState{"strongo/plugin": entry}}
	if !statesEqual(base, cloneState(base)) {
		t.Fatal("identical state differs")
	}
	for _, mutate := range []func(*state){
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.Skills["alpha"] = strings.Repeat("b", 64)
			s.Plugins["strongo/plugin"] = p
		},
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.Suppliers["strongo/tool"] = revisionForTest("other")
			s.Plugins["strongo/plugin"] = p
		},
		func(s *state) {
			p := s.Plugins["strongo/plugin"]
			p.SupplierCLIVersions["strongo/tool"] = "2.0.0"
			s.Plugins["strongo/plugin"] = p
		},
	} {
		changed := cloneState(base)
		mutate(&changed)
		if statesEqual(base, changed) {
			t.Fatal("changed ownership state compared equal")
		}
	}
}

func TestAdmissionClassificationRejectsUnsafeFilesystemShapes(t *testing.T) {
	item := skill{Name: "alpha", Digest: strings.Repeat("a", 64)}
	prior := pluginState{Skills: map[string]string{"alpha": item.Digest}}
	owners := map[string]string{"alpha": "strongo/plugin"}

	t.Run("classify file and unsafe tree", func(t *testing.T) {
		fileParent := filepath.Join(t.TempDir(), "file-parent")
		if err := os.WriteFile(fileParent, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := classify(fileParent, item, prior, owners, "strongo/plugin"); err == nil {
			t.Fatal("classification accepted file parent")
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "alpha"), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if action, reason, err := classify(dir, item, prior, owners, "strongo/plugin"); err != nil || action != Conflict || reason != "non-directory target" {
			t.Fatalf("file classify action=%q reason=%q err=%v", action, reason, err)
		}
		if err := os.Remove(filepath.Join(dir, "alpha")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "alpha", "link")); err != nil {
			t.Fatal(err)
		}
		if action, reason, err := classify(dir, item, prior, owners, "strongo/plugin"); err != nil || action != Conflict || reason != "unsafe target" {
			t.Fatalf("unsafe classify action=%q reason=%q err=%v", action, reason, err)
		}
	})

	t.Run("removal protects ownership and malformed target", func(t *testing.T) {
		dir := t.TempDir()
		if action, reason, err := classifyRemoval(dir, "alpha", item.Digest, map[string]string{"alpha": "strongo/other"}, "strongo/plugin"); err != nil || action != Conflict || reason != "owned by another plugin" {
			t.Fatalf("ownership removal action=%q reason=%q err=%v", action, reason, err)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := classifyRemoval(file, "alpha", item.Digest, owners, "strongo/plugin"); err == nil {
			t.Fatal("removal accepted file parent")
		}
		if err := os.WriteFile(filepath.Join(dir, "alpha"), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if action, reason, err := classifyRemoval(dir, "alpha", item.Digest, owners, "strongo/plugin"); err != nil || action != Conflict || reason != "non-directory target" {
			t.Fatalf("file removal action=%q reason=%q err=%v", action, reason, err)
		}
		if err := os.Remove(filepath.Join(dir, "alpha")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "alpha", "link")); err != nil {
			t.Fatal(err)
		}
		if action, reason, err := classifyRemoval(dir, "alpha", item.Digest, owners, "strongo/plugin"); err != nil || action != Conflict || reason != "unsafe target" {
			t.Fatalf("unsafe removal action=%q reason=%q err=%v", action, reason, err)
		}
	})

	t.Run("target root rejects missing parent errors links and files", func(t *testing.T) {
		if err := rejectSymlink(filepath.Join(t.TempDir(), "missing")); err != nil {
			t.Fatalf("missing target err=%v", err)
		}
		if err := rejectSymlink(filepath.Join(t.TempDir(), "missing-parent", "target")); err != nil {
			t.Fatalf("nested missing target err=%v", err)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rejectSymlink(filepath.Join(file, "child")); err == nil {
			t.Fatal("file-parent target accepted")
		}
		if err := rejectSymlink(file); err == nil {
			t.Fatal("file target accepted")
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		if err := rejectSymlink(link); err == nil {
			t.Fatal("symlink target accepted")
		}
		if err := rejectSymlink(filepath.Join(link, "missing")); err == nil {
			t.Fatal("symlink ancestor target accepted")
		}
	})

	t.Run("missing ancestry lookup failures are preserved", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		if err := validTransactionDir(missing); err != nil {
			t.Fatalf("missing transaction dir=%v", err)
		}
		if err := checkTransactionSubdir(missing, "backup"); err != nil {
			t.Fatalf("missing transaction subdir=%v", err)
		}
		lookupErr := errors.New("ancestor lookup")
		previous := filesystemOperations
		t.Cleanup(func() { filesystemOperations = previous })
		filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return nil, lookupErr }
		if err := rejectSymlink(missing); !errors.Is(err, lookupErr) {
			t.Fatalf("target ancestry error=%v", err)
		}
		if _, _, _, err := readRecoveryJournal(missing); !errors.Is(err, ErrStateCorrupt) || !errors.Is(err, lookupErr) {
			t.Fatalf("journal ancestry error=%v", err)
		}
		if err := validTransactionDir(missing); !errors.Is(err, ErrStateCorrupt) || !errors.Is(err, lookupErr) {
			t.Fatalf("transaction ancestry error=%v", err)
		}
		if err := checkTransactionSubdir(missing, "backup"); !errors.Is(err, ErrStateCorrupt) || !errors.Is(err, lookupErr) {
			t.Fatalf("subdir ancestry error=%v", err)
		}
		fileParent := filepath.Join(t.TempDir(), "file-parent")
		if err := os.WriteFile(fileParent, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(fileParent)
		if err != nil {
			t.Fatal(err)
		}
		filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return info, nil }
		if err := rejectSymlink(missing); err == nil {
			t.Fatal("non-directory ancestor accepted")
		}
	})
}

func TestAdmissionSyncLockedPreservesTargetsAcrossFailures(t *testing.T) {
	t.Run("reinstalls a missing owned skill from the same immutable bundle", func(t *testing.T) {
		dir := t.TempDir()
		b := bundle(t, "plugin", "reinstall", "body")
		if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "alpha")); err != nil {
			t.Fatal(err)
		}
		report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
		if err != nil || report.Changes[0].Action != Added || report.Changes[0].Outcome != Applied {
			t.Fatalf("reinstall report=%#v err=%v", report, err)
		}
		if content, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(content) != "body" {
			t.Fatalf("reinstall content=%q err=%v", content, err)
		}
	})

	t.Run("reinstall finalization failure retains recovered target and journal", func(t *testing.T) {
		dir := t.TempDir()
		b := bundle(t, "plugin", "reinstall-finalize", "body")
		if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "alpha")); err != nil {
			t.Fatal(err)
		}
		commitErr := fmt.Errorf("commit cleanup: %w", ErrStateCorrupt)
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.removeAll = func(root *os.Root, path string) error {
			if strings.HasPrefix(filepath.Base(path), transactionPrefix) {
				return commitErr
			}
			return previous.removeAll(root, path)
		}
		report, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
		transactionOperations = previous
		if !errors.Is(err, ErrStateCorrupt) || report.Changes[0].Action != Added || report.Changes[0].Outcome != Incomplete {
			t.Fatalf("finalization report=%#v err=%v", report, err)
		}
		if content, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(content) != "body" {
			t.Fatalf("finalization failure lost recovered target=%q err=%v", content, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
			t.Fatalf("finalization failure discarded journal: %v", err)
		}
		retry, err := Sync(context.Background(), config(t, b), Options{Dir: dir})
		if err != nil || strings.Join(retry.NamesFor(Added, Applied), ",") != "alpha" {
			t.Fatalf("retry=%#v err=%v", retry, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("retry left recovery journal: %v", err)
		}
	})

	t.Run("unsafe target, unreadable marker, and legacy collision fail closed", func(t *testing.T) {
		b := bundle(t, "plugin", "locked", "body")
		p, err := Prepare(context.Background(), config(t, b), Options{})
		if err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "target")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		if _, err := syncLocked(context.Background(), p.cfg, p.bundles, Options{Dir: link}, reportForPrepared(p, link)); err == nil {
			t.Fatal("syncLocked accepted symlinked target")
		}
		dir := t.TempDir()
		if err := os.Mkdir(statePath(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := syncLocked(context.Background(), p.cfg, p.bundles, Options{Dir: dir}, reportForPrepared(p, dir)); err == nil {
			t.Fatal("syncLocked accepted unreadable state marker")
		}

		dir = t.TempDir()
		other := bundle(t, "other", "legacy-owner", "body")
		if _, err := Sync(context.Background(), config(t, other), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		legacyDigest, err := legacyWBDigest(other.FS, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"schema_version": 1, "skills": map[string]string{"alpha": legacyDigest}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".wb-skills-sync.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := syncLocked(context.Background(), p.cfg, p.bundles, Options{Dir: dir, Legacy: LegacyImport{MarkerFile: ".wb-skills-sync.json", Plugin: b.Plugin}}, reportForPrepared(p, dir)); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("legacy ownership collision err=%v", err)
		}
		if content, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(content) != "body" {
			t.Fatalf("legacy collision changed existing target=%q err=%v", content, err)
		}
	})

	t.Run("planner error and empty resolved skills preserve target", func(t *testing.T) {
		dir := t.TempDir()
		keep := filepath.Join(dir, "keep")
		if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		b := bundle(t, "plugin", "planner", "body")
		entry := contractPluginState(t)
		entry.Skills = map[string]string{strings.Repeat("a", 300): strings.Repeat("a", 64)}
		if err := writeState(dir, state{Plugins: map[string]pluginState{b.Plugin.String(): entry}}); err != nil {
			t.Fatal(err)
		}
		p, err := Prepare(context.Background(), config(t, b), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := syncLocked(context.Background(), p.cfg, p.bundles, Options{Dir: dir}, reportForPrepared(p, dir)); err == nil {
			t.Fatal("planner accepted unaddressable prior target")
		}
		if got, err := os.ReadFile(keep); err != nil || string(got) != "keep" {
			t.Fatalf("planner failure changed target=%q err=%v", got, err)
		}
		empty := resolvedBundle{Bundle: b}
		if _, err := syncLocked(context.Background(), p.cfg, []resolvedBundle{empty}, Options{Dir: t.TempDir(), DryRun: true}, Report{}); err != nil {
			t.Fatalf("empty resolved skills err=%v", err)
		}
	})

	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation restores changed target", true: "cancellation preserves recovery evidence"}[rollbackFails], func(t *testing.T) {
			dir := t.TempDir()
			old := bundleWith(t, "plugin", "old", map[string]string{"alpha": "old alpha", "beta": "old beta"})
			newer := bundleWith(t, "plugin", "new", map[string]string{"alpha": "new alpha", "beta": "new beta"})
			if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			p, err := Prepare(context.Background(), config(t, newer), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if rollbackFails {
				rollbackErr := errors.New("rollback remove failed")
				withTransactionOperations(t, func(ops *transactionOperationSet) {
					original := ops.removeAll
					ops.removeAll = func(root *os.Root, path string) error {
						if filepath.Base(path) == "alpha" {
							return rollbackErr
						}
						return original(root, path)
					}
				})
			}
			ctx := &cancellationAfterChecks{cancelOn: 3}
			report, err := syncLocked(ctx, p.cfg, p.bundles, Options{Dir: dir}, reportForPrepared(p, dir))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation err=%v", err)
			}
			alpha, alphaErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md"))
			beta, betaErr := os.ReadFile(filepath.Join(dir, "beta", "SKILL.md"))
			if betaErr != nil || string(beta) != "old beta" {
				t.Fatalf("cancellation changed untouched target=%q err=%v", beta, betaErr)
			}
			if rollbackFails {
				if alphaErr != nil || string(alpha) != "new alpha" || !strings.Contains(err.Error(), "rollback") || report.Changes[0].Outcome != Incomplete {
					t.Fatalf("rollback evidence report=%#v alpha=%q err=%v", report, alpha, err)
				}
				matches, globErr := filepath.Glob(filepath.Join(dir, transactionPrefix+"*", "backup", "alpha", "SKILL.md"))
				if globErr != nil || len(matches) != 1 {
					t.Fatalf("changed backup was not recoverable: %v %v", matches, globErr)
				}
			} else if alphaErr != nil || string(alpha) != "old alpha" || report.Changes[0].Outcome != Restored {
				t.Fatalf("cancellation report=%#v alpha=%q err=%v", report, alpha, err)
			}
		})
	}

	t.Run("state persistence failure restores original target", func(t *testing.T) {
		dir := t.TempDir()
		old := bundle(t, "plugin", "state-old", "old")
		newer := bundle(t, "plugin", "state-new", "new")
		if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		stateErr := errors.New("state rename failed")
		withTransactionOperations(t, func(ops *transactionOperationSet) {
			original := ops.rename
			ops.rename = func(root *os.Root, from, to string) error {
				if filepath.Base(to) == StateFileName {
					return stateErr
				}
				return original(root, from, to)
			}
		})
		report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
		if !errors.Is(err, stateErr) || report.Changes[0].Outcome != Restored {
			t.Fatalf("state failure report=%#v err=%v", report, err)
		}
		if content, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(content) != "old" {
			t.Fatalf("state failure changed target=%q err=%v", content, err)
		}
	})
}
