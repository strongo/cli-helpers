package skillsync

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// findChange returns the change named name; it is safe from any goroutine.
func findChange(report Report, name string) (Change, bool) {
	for _, c := range report.Changes {
		if c.Name == name {
			return c, true
		}
	}
	return Change{}, false
}

// changeFor returns the one change named name, failing the test without it.
func changeFor(t *testing.T, report Report, name string) Change {
	t.Helper()
	c, ok := findChange(report, name)
	if !ok {
		t.Fatalf("no change for %q in %#v", name, report.Changes)
	}
	return c
}

func assertUntouchedAndUnbacked(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
	if err != nil || string(got) != want {
		t.Fatalf("%s/SKILL.md = %q, %v; want it byte-identical to %q", name, got, err, want)
	}
	if backups := listAdoptionBackups(t, dir); len(backups) != 0 {
		t.Fatalf("a refused adoption left backups: %v", backups)
	}
}

// With NoAdopt an adoptable folder is the pre-adoption result: Conflict with
// the old reason "unmanaged target", untouched, no backup, no ownership
// recorded, and the change says it would have been adoptable. A dry run
// reports the same.
func TestNoAdoptRefusesAnAdoptableFolderAsPlainConflict(t *testing.T) {
	for _, dry := range []bool{false, true} {
		dir := t.TempDir()
		original := frontmatterSKILL("alpha", "mine")
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": original})
		b := adoptionBundle(t, "plugin", "r1", "alpha")

		report, err := Sync(context.Background(), config(t, b), Options{Dir: dir, DryRun: dry, NoAdopt: true})
		if err != nil {
			t.Fatalf("dry=%v: %v", dry, err)
		}
		c := changeFor(t, report, "alpha")
		if c.Action != Conflict || c.Reason != "unmanaged target" || !c.Adoptable || c.Outcome != "" || c.BackupPath != "" {
			t.Fatalf("dry=%v change = %#v", dry, c)
		}
		if report.Changed() || len(report.Names(Adopted)) != 0 {
			t.Fatalf("dry=%v report claims a change: %#v", dry, report)
		}
		assertUntouchedAndUnbacked(t, dir, "alpha", original)
		state, err := readState(dir)
		if err == nil && state.Plugins[b.Plugin.String()].Skills["alpha"] != "" {
			t.Fatalf("dry=%v ownership recorded for a refused folder: %#v", dry, state)
		}
	}
}

// The default is unchanged: adoption on, and Adoptable stays false because
// nothing was refused.
func TestAdoptionStaysOnByDefault(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})
	report, err := Sync(context.Background(), config(t, adoptionBundle(t, "plugin", "r1", "alpha")), Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	c := changeFor(t, report, "alpha")
	if c.Action != Adopted || c.Outcome != Applied || c.Adoptable {
		t.Fatalf("change = %#v", c)
	}
	raw, _ := json.Marshal(c)
	if json.Valid(raw) && containsKey(t, raw, "adoptable") {
		t.Fatalf("an adopted change serializes adoptable: %s", raw)
	}
}

func containsKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	_, ok := m[key]
	return ok
}

// A folder adoption would also refuse keeps its specific reason and is not
// reported adoptable, with or without NoAdopt; skills that never needed
// adoption (missing, unchanged) are unaffected by it.
func TestNoAdoptLeavesEveryOtherOutcomeAlone(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":  frontmatterSKILL("alpha", "mine"),
		"extra.txt": "keep me",
	})
	b := adoptionBundle(t, "plugin", "r1", "alpha", "beta")
	report, err := Sync(context.Background(), config(t, b), Options{Dir: dir, NoAdopt: true})
	if err != nil {
		t.Fatal(err)
	}
	alpha := changeFor(t, report, "alpha")
	if alpha.Action != Conflict || alpha.Adoptable || alpha.Reason == "unmanaged target" || alpha.Reason == "" {
		t.Fatalf("non-adoptable folder change = %#v, want its specific reason and not adoptable", alpha)
	}
	if beta := changeFor(t, report, "beta"); beta.Action != Added || beta.Outcome != Applied || beta.Adoptable {
		t.Fatalf("missing sibling = %#v", beta)
	}
	report, err = Sync(context.Background(), config(t, b), Options{Dir: dir, NoAdopt: true})
	if err != nil {
		t.Fatal(err)
	}
	if beta := changeFor(t, report, "beta"); beta.Action != Unchanged {
		t.Fatalf("managed sibling on the second run = %#v", beta)
	}
}

// The take-over a consumer offers after the refusal: the same call without
// NoAdopt adopts, with the backup.
func TestRefusedFolderCanBeAdoptedByTheNextCallWithoutNoAdopt(t *testing.T) {
	dir := t.TempDir()
	original := frontmatterSKILL("alpha", "mine")
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": original})
	cfg := config(t, adoptionBundle(t, "plugin", "r1", "alpha"))
	if report, err := Sync(context.Background(), cfg, Options{Dir: dir, NoAdopt: true}); err != nil || !changeFor(t, report, "alpha").Adoptable {
		t.Fatalf("first call: %#v, %v", report, err)
	}
	report, err := Sync(context.Background(), cfg, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if c := changeFor(t, report, "alpha"); c.Action != Adopted || c.BackupPath == "" {
		t.Fatalf("second call = %#v", c)
	}
}

// Adoptable is part of the JSON report, and only when set.
func TestAdoptableChangeReportHasJSONShape(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})
	report, err := Sync(context.Background(), config(t, adoptionBundle(t, "plugin", "r1", "alpha")), Options{Dir: dir, NoAdopt: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(changeFor(t, report, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["adoptable"] != true || decoded["action"] != "conflict" || decoded["reason"] != "unmanaged target" {
		t.Fatalf("json = %s", raw)
	}
	if _, has := decoded["outcome"]; has {
		t.Fatalf("a refused folder has an outcome: %s", raw)
	}
}

// The window the option closes: a plan made while the folder was absent (or
// a caller's own earlier check) says nothing about the folder that exists when
// the write happens. The decision is made at the write, so a folder that
// became adoptable after a dry run is still not taken over.
func TestNoAdoptHoldsForAFolderThatAppearsAfterTheDryRun(t *testing.T) {
	dir := t.TempDir()
	cfg := config(t, adoptionBundle(t, "plugin", "r1", "alpha"))
	plan, err := Sync(context.Background(), cfg, Options{Dir: dir, DryRun: true, NoAdopt: true})
	if err != nil || changeFor(t, plan, "alpha").Action != Added {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	original := frontmatterSKILL("alpha", "appeared later")
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": original})
	report, err := Sync(context.Background(), cfg, Options{Dir: dir, NoAdopt: true})
	if err != nil {
		t.Fatal(err)
	}
	if c := changeFor(t, report, "alpha"); c.Action != Conflict || !c.Adoptable {
		t.Fatalf("change = %#v", c)
	}
	assertUntouchedAndUnbacked(t, dir, "alpha", original)
}

// leavePendingRecovery interrupts an update of another plugin's skill so the
// target holds a recovery journal (as a crashed or failed install does), then
// puts an adoptable unmanaged folder next to it.
func leavePendingRecovery(t *testing.T) (dir, original string) {
	t.Helper()
	dir = t.TempDir()
	old := bundle(t, "plugin", "retry-old", "old")
	newer := bundle(t, "plugin", "retry-new", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	stateSyncErr := errors.New("state directory sync")
	previous := stateDirectorySync
	stateDirectorySync = func(string) error { return stateSyncErr }
	_, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	stateDirectorySync = previous
	if !errors.Is(err, stateSyncErr) {
		t.Fatalf("setup sync err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("setup left no recovery journal: %v", err)
	}
	original = frontmatterSKILL("beta", "mine")
	writeUnmanagedSkill(t, dir, "beta", map[string]string{"SKILL.md": original})
	return dir, original
}

// The second hole of a caller-side pre-read: with a recovery journal pending,
// a dry run fails (so a caller sees nothing about the folder), and the real
// sync recovers and then used to adopt. With NoAdopt the real sync recovers
// and refuses; without it, adoption still happens as before.
func TestNoAdoptHoldsWhileARecoveryJournalIsPending(t *testing.T) {
	other := adoptionBundle(t, "other", "r1", "beta")
	cfg := config(t, other)

	t.Run("NoAdopt refuses after recovering", func(t *testing.T) {
		dir, original := leavePendingRecovery(t)
		if _, err := Sync(context.Background(), cfg, Options{Dir: dir, DryRun: true, NoAdopt: true}); !errors.Is(err, ErrRecoveryPending) {
			t.Fatalf("dry run err = %v, want ErrRecoveryPending as before", err)
		}
		report, err := Sync(context.Background(), cfg, Options{Dir: dir, NoAdopt: true})
		if err != nil {
			t.Fatal(err)
		}
		if c := changeFor(t, report, "beta"); c.Action != Conflict || c.Reason != "unmanaged target" || !c.Adoptable {
			t.Fatalf("change = %#v", c)
		}
		assertUntouchedAndUnbacked(t, dir, "beta", original)
		if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the pending journal was not recovered: %v", err)
		}
	})

	t.Run("default still adopts", func(t *testing.T) {
		dir, _ := leavePendingRecovery(t)
		report, err := Sync(context.Background(), cfg, Options{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if c := changeFor(t, report, "beta"); c.Action != Adopted {
			t.Fatalf("change = %#v", c)
		}
	})
}

// Concurrent callers on one target: the lock serializes them, and a NoAdopt
// caller never adopts, whatever the others do. Run with -race.
func TestNoAdoptNeverAdoptsUnderConcurrentSync(t *testing.T) {
	t.Run("only refusing callers", func(t *testing.T) {
		dir := t.TempDir()
		original := frontmatterSKILL("alpha", "mine")
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": original})
		cfg := config(t, adoptionBundle(t, "plugin", "r1", "alpha"))
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				report, err := Sync(context.Background(), cfg, Options{Dir: dir, NoAdopt: true})
				if err == nil {
					if c, _ := findChange(report, "alpha"); c.Action != Conflict || !c.Adoptable {
						errs <- errors.New("a NoAdopt call did not refuse: " + string(c.Action))
					}
				} else {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		assertUntouchedAndUnbacked(t, dir, "alpha", original)
	})

	t.Run("mixed with an adopting caller", func(t *testing.T) {
		dir := t.TempDir()
		writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})
		cfg := config(t, adoptionBundle(t, "plugin", "r1", "alpha"))
		type result struct {
			noAdopt bool
			action  Action
			err     error
		}
		results := make(chan result, 9)
		var wg sync.WaitGroup
		for i := 0; i < 9; i++ {
			wg.Add(1)
			go func(noAdopt bool) {
				defer wg.Done()
				report, err := Sync(context.Background(), cfg, Options{Dir: dir, NoAdopt: noAdopt})
				r := result{noAdopt: noAdopt, err: err}
				if err == nil {
					c, _ := findChange(report, "alpha")
					r.action = c.Action
				}
				results <- r
			}(i != 4)
		}
		wg.Wait()
		close(results)
		adopted := 0
		for r := range results {
			if r.err != nil {
				t.Errorf("sync error: %v", r.err)
				continue
			}
			if r.action == Adopted {
				adopted++
				if r.noAdopt {
					t.Error("a NoAdopt call reported Adopted")
				}
			}
			// A refusing caller sees the folder before adoption (Conflict) or
			// after it (Unchanged): never a take-over of its own.
			if r.noAdopt && r.action != Conflict && r.action != Unchanged {
				t.Errorf("NoAdopt call action = %s", r.action)
			}
		}
		if adopted != 1 {
			t.Errorf("adopted by %d calls, want exactly the one adopting caller", adopted)
		}
		if backups := listAdoptionBackups(t, dir); len(backups) != 1 {
			t.Errorf("backups = %v, want exactly one", backups)
		}
	})
}

// reportJSON is the report as a consumer sees it, for byte comparison.
func reportJSON(t *testing.T, report Report) string {
	t.Helper()
	report.Dir = "<dir>"
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// otherConflictSetup is the review's repro: plugin owns "old", the person
// hand-edited it (a modified target, an unresolved conflict of the plugin), an
// adoptable folder "beta" sits beside it, and a new revision ships both.
func otherConflictSetup(t *testing.T) (dir string, next Config) {
	t.Helper()
	dir = t.TempDir()
	first := bundleWith(t, "plugin", "r1", map[string]string{"old": frontmatterSKILL("old", "v1")})
	if _, err := Sync(context.Background(), config(t, first), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old", "SKILL.md"), []byte("hand edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUnmanagedSkill(t, dir, "beta", map[string]string{"SKILL.md": frontmatterSKILL("beta", "mine")})
	second := adoptionBundle(t, "plugin", "r2", "old", "beta")
	return dir, config(t, second)
}

// A folder the plugin's other conflict withdraws is not one this call would
// have adopted, so NoAdopt must not call it adoptable or change its reason:
// the report is byte-identical with the flag on and off.
func TestNoAdoptReportsAFolderWithdrawnByAnotherConflictAsWithoutTheFlag(t *testing.T) {
	for _, dry := range []bool{true, false} {
		dirOff, cfg := otherConflictSetup(t)
		off, err := Sync(context.Background(), cfg, Options{Dir: dirOff, DryRun: dry})
		if err != nil {
			t.Fatal(err)
		}
		dirOn, cfgOn := otherConflictSetup(t)
		on, err := Sync(context.Background(), cfgOn, Options{Dir: dirOn, DryRun: dry, NoAdopt: true})
		if err != nil {
			t.Fatal(err)
		}
		beta := changeFor(t, on, "beta")
		if beta.Action != Conflict || beta.Reason != "plugin has unresolved conflicts" || beta.Adoptable {
			t.Fatalf("dry=%v NoAdopt beta = %#v", dry, beta)
		}
		// The two runs used different temporary directories, so compare the
		// reports as consumers see them, apart from timestamps nobody reports.
		if a, b := reportJSON(t, off), reportJSON(t, on); a != b {
			t.Fatalf("dry=%v reports differ with the flag:\n off %s\n on  %s", dry, a, b)
		}
	}
}

// For every shape adoption does not take over, the report is byte-identical
// with NoAdopt on and off, in a dry run and a real sync; the only difference
// the flag makes is for a folder this call would have adopted.
func TestNoAdoptChangesNothingButAdoptionAcrossShapes(t *testing.T) {
	type shape struct {
		name  string
		setup func(t *testing.T, dir string)
	}
	shapes := []shape{
		{"missing", func(*testing.T, string) {}},
		{"foreign file", func(t *testing.T, dir string) {
			writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "x"), "extra.txt": "e"})
		}},
		{"no SKILL.md", func(t *testing.T, dir string) {
			writeUnmanagedSkill(t, dir, "alpha", map[string]string{"other.md": "x"})
		}},
		{"wrong name", func(t *testing.T, dir string) {
			writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("zzz", "x")})
		}},
		{"no frontmatter", func(t *testing.T, dir string) {
			writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": "plain"})
		}},
		{"non-directory", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "alpha"), []byte("file"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	cfg := config(t, adoptionBundle(t, "plugin", "r1", "alpha"))
	for _, s := range shapes {
		for _, dry := range []bool{true, false} {
			var out [2]string
			for i, noAdopt := range []bool{false, true} {
				dir := t.TempDir()
				s.setup(t, dir)
				report, err := Sync(context.Background(), cfg, Options{Dir: dir, DryRun: dry, NoAdopt: noAdopt})
				if err != nil {
					t.Fatalf("%s dry=%v: %v", s.name, dry, err)
				}
				out[i] = reportJSON(t, report)
			}
			if out[0] != out[1] {
				t.Errorf("%s dry=%v differs with the flag:\n off %s\n on  %s", s.name, dry, out[0], out[1])
			}
		}
	}
}

// NoAdopt is about this call's own take-over, not about a transaction an
// earlier call began: when the pending journal is an interrupted adoption of
// the same folder (begun by a call that asked for adoption), the next call
// with NoAdopt recovers forward, the folder ends up owned and reported
// Unchanged, and no backup is made by this call.
func TestNoAdoptCompletesAnEarlierCallsInterruptedAdoption(t *testing.T) {
	dir := t.TempDir()
	writeUnmanagedSkill(t, dir, "alpha", map[string]string{"SKILL.md": frontmatterSKILL("alpha", "mine")})
	b := adoptionBundle(t, "plugin", "r1", "alpha")
	cfg := config(t, b)

	stateSyncErr := errors.New("state directory sync")
	previous := stateDirectorySync
	stateDirectorySync = func(string) error { return stateSyncErr }
	first, err := Sync(context.Background(), cfg, Options{Dir: dir})
	stateDirectorySync = previous
	if !errors.Is(err, stateSyncErr) {
		t.Fatalf("setup err = %v", err)
	}
	if c := changeFor(t, first, "alpha"); c.Action != Adopted || c.Outcome != Incomplete {
		t.Fatalf("setup change = %#v", c)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("setup left no journal: %v", err)
	}
	backups := listAdoptionBackups(t, dir)
	if len(backups) != 1 {
		t.Fatalf("setup backups = %v", backups)
	}

	if _, err := Sync(context.Background(), cfg, Options{Dir: dir, DryRun: true, NoAdopt: true}); !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("dry run err = %v, want ErrRecoveryPending", err)
	}
	report, err := Sync(context.Background(), cfg, Options{Dir: dir, NoAdopt: true})
	if err != nil {
		t.Fatal(err)
	}
	if c := changeFor(t, report, "alpha"); c.Action != Unchanged || c.Adoptable || c.BackupPath != "" {
		t.Fatalf("change = %#v, want Unchanged", c)
	}
	state, err := readState(dir)
	if err != nil || state.Plugins[b.Plugin.String()].Skills["alpha"] == "" {
		t.Fatalf("folder not owned after completing the earlier adoption: %#v, %v", state, err)
	}
	if got := listAdoptionBackups(t, dir); len(got) != 1 || got[0] != backups[0] {
		t.Fatalf("backups = %v, want only the first call's %v", got, backups)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(data) != frontmatterSKILL("alpha", "bundled body for alpha") {
		t.Fatalf("folder = %q, %v", data, err)
	}
}
