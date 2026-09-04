package skillsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type faultFile struct {
	name              string
	writeN            int
	writeErr, syncErr error
	closeErr          error
}

func (f *faultFile) Name() string { return f.name }
func (f *faultFile) Write(data []byte) (int, error) {
	if f.writeN == 0 && f.writeErr == nil {
		return len(data), nil
	}
	return f.writeN, f.writeErr
}
func (f *faultFile) Sync() error  { return f.syncErr }
func (f *faultFile) Close() error { return f.closeErr }

type syncFaultFile struct {
	durableFile
	err error
}

func (f syncFaultFile) Sync() error { return f.err }

type replacementContentFile struct{ durableFile }

func (f replacementContentFile) Write(data []byte) (int, error) {
	return f.durableFile.Write([]byte(strings.Repeat("x", len(data))))
}

func withDurableFileOperations(t *testing.T, mutate func(*durableFileOperationSet)) {
	t.Helper()
	previous := durableFileOperations
	t.Cleanup(func() { durableFileOperations = previous })
	mutate(&durableFileOperations)
}

func TestWriteAndSyncRejectsEveryUncommittedFileResult(t *testing.T) {
	writeErr := errors.New("write")
	syncErr := errors.New("sync")
	closeErr := errors.New("close")
	for _, tc := range []struct {
		name string
		file *faultFile
		want error
	}{
		{name: "write", file: &faultFile{writeErr: writeErr}, want: writeErr},
		{name: "short write", file: &faultFile{writeN: 1}, want: io.ErrShortWrite},
		{name: "sync", file: &faultFile{syncErr: syncErr}, want: syncErr},
		{name: "close", file: &faultFile{closeErr: closeErr}, want: closeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeAndSync(tc.file, []byte("durable")); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestWriteStateRejectsUnencodableTimestamp(t *testing.T) {
	dir := t.TempDir()
	err := writeState(dir, state{Plugins: map[string]pluginState{
		"strongo/plugin": {Legacy: true, Skills: map[string]string{"alpha": strings.Repeat("a", 64)}, SyncedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	}})
	if err == nil {
		t.Fatal("invalid RFC3339 timestamp was persisted")
	}
	if _, err := os.Lstat(statePath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state exists after marshal failure: %v", err)
	}
}

func TestAtomicPublicationFaultsDoNotClaimPublication(t *testing.T) {
	for _, phase := range []string{"temp-create", "temp-sync", "rename", "directory-sync"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			switch phase {
			case "temp-create":
				createErr := errors.New("temp create")
				withDurableFileOperations(t, func(ops *durableFileOperationSet) {
					ops.createTemp = func(string, string) (durableFile, error) { return nil, createErr }
				})
				if published, err := writeAtomically(dir, ".tmp-*", filepath.Join(dir, "marker"), []byte("x"), syncDirectory); published || !errors.Is(err, createErr) {
					t.Fatalf("published=%v err=%v", published, err)
				}
			case "temp-sync":
				withDurableFileOperations(t, func(ops *durableFileOperationSet) {
					original := ops.createTemp
					ops.createTemp = func(parent, pattern string) (durableFile, error) {
						file, err := original(parent, pattern)
						if err != nil {
							return nil, err
						}
						return syncFaultFile{durableFile: file, err: errors.New("temp sync")}, nil
					}
				})
				if published, err := writeAtomically(dir, ".tmp-*", filepath.Join(dir, "marker"), []byte("x"), syncDirectory); published || err == nil {
					t.Fatalf("published=%v err=%v", published, err)
				}
			case "rename":
				withTransactionOperations(t, func(ops *transactionOperationSet) {
					original := ops.rename
					ops.rename = func(root *os.Root, from, to string) error {
						if filepath.Base(to) == "marker" {
							return errors.New("rename")
						}
						return original(root, from, to)
					}
				})
				if published, err := writeAtomically(dir, ".tmp-*", filepath.Join(dir, "marker"), []byte("x"), syncDirectory); published || err == nil {
					t.Fatalf("published=%v err=%v", published, err)
				}
			case "directory-sync":
				directoryErr := errors.New("directory sync")
				if published, err := writeAtomically(dir, ".tmp-*", filepath.Join(dir, "marker"), []byte("x"), func(string) error { return directoryErr }); !published || !errors.Is(err, directoryErr) {
					t.Fatalf("published=%v err=%v", published, err)
				}
			}
		})
	}
}

func TestWriteStateMkdirFailureLeavesNoMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	mkdirErr := errors.New("mkdir")
	withDurableFileOperations(t, func(ops *durableFileOperationSet) {
		ops.mkdirAll = func(string, fs.FileMode) error { return mkdirErr }
	})
	err := writeState(dir, state{Plugins: map[string]pluginState{}})
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Lstat(statePath(dir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("marker exists: %v", err)
	}
}

func TestPreparedAndTargetPublicFailureContracts(t *testing.T) {
	b := bundle(t, "plugin", "prepared", "body")
	if _, err := Prepare(context.Background(), Config{}, Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid prepare=%v", err)
	}
	if _, err := Prepare(context.Background(), config(t, b), Options{PreferNewerCompatible: true}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("resolver prepare=%v", err)
	}
	prepared, err := Prepare(context.Background(), config(t, b), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Sync(context.Background(), Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty target=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(canceled, config(t, b), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepare=%v", err)
	}
	if _, err := ValidateTarget(""); err != nil {
		t.Fatalf("empty target validation=%v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTarget(file); err == nil {
		t.Fatal("file target accepted")
	}
	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTarget(filepath.Join(link, "skills")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestSyncCopyFileSyncFailureLeavesVerifiedOriginal(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "durable-old", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("staged file sync")
	withDurableFileOperations(t, func(ops *durableFileOperationSet) {
		original := ops.createFile
		ops.createFile = func(path string, mode fs.FileMode) (durableFile, error) {
			file, err := original(path, mode)
			if err != nil {
				return nil, err
			}
			return syncFaultFile{durableFile: file, err: syncErr}, nil
		}
	})
	report, err := Sync(context.Background(), config(t, bundle(t, "plugin", "durable-new", "new")), Options{Dir: dir})
	if !errors.Is(err, syncErr) || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if data, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(data) != "old" {
		t.Fatalf("target=%q err=%v", data, readErr)
	}
	if _, readErr := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("journal exists before any mutation: %v", readErr)
	}
}

func TestSyncBackupPersistenceFailureRestoresOriginalAndRetries(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "backup-old", "old")
	newer := bundle(t, "plugin", "backup-new", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	persistenceErr := errors.New("backup parent sync")
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		original := ops.syncDirectory
		backupCalls := 0
		ops.syncDirectory = func(path string) error {
			if filepath.Base(path) == "backup" {
				backupCalls++
				if backupCalls == 2 { // once for backup creation, once after old-target rename
					return persistenceErr
				}
			}
			return original(path)
		}
	})
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if !errors.Is(err, persistenceErr) || report.Changes[0].Outcome != Restored {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if data, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(data) != "old" {
		t.Fatalf("target=%q err=%v", data, readErr)
	}
}

func TestSyncPreservesEditsCapturedBetweenDigestAndBackupRename(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initial     Bundle
		replacement Bundle
		skill       string
	}{
		{
			name:        "update",
			initial:     bundle(t, "plugin", "window-update-old", "old"),
			replacement: bundle(t, "plugin", "window-update-new", "new"),
			skill:       "alpha",
		},
		{
			name:        "removal",
			initial:     bundleWith(t, "plugin", "window-remove-old", map[string]string{"alpha": "keep", "beta": "old"}),
			replacement: bundleWith(t, "plugin", "window-remove-new", map[string]string{"alpha": "keep"}),
			skill:       "beta",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Sync(context.Background(), config(t, tc.initial), Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			withTransactionOperations(t, func(ops *transactionOperationSet) {
				original := ops.rename
				ops.rename = func(root *os.Root, from, to string) error {
					if from == tc.skill && strings.Contains(to, "/backup/"+tc.skill) {
						if err := os.WriteFile(filepath.Join(dir, tc.skill, "SKILL.md"), []byte("user edit made after digest check"), 0o644); err != nil {
							return err
						}
					}
					return original(root, from, to)
				}
			})
			report, err := Sync(context.Background(), config(t, tc.replacement), Options{Dir: dir})
			if !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("sync error=%v report=%#v", err, report)
			}
			var found Change
			for _, change := range report.Changes {
				if change.Name == tc.skill {
					found = change
				}
			}
			if found.Outcome != Incomplete {
				t.Fatalf("foreign backup reported as %q: %#v", found.Outcome, report)
			}
			backups, globErr := filepath.Glob(filepath.Join(dir, transactionPrefix+"*", "backup", tc.skill, "SKILL.md"))
			if globErr != nil || len(backups) != 1 {
				t.Fatalf("backups=%v err=%v", backups, globErr)
			}
			if got, readErr := os.ReadFile(backups[0]); readErr != nil || string(got) != "user edit made after digest check" {
				t.Fatalf("foreign backup lost=%q err=%v", got, readErr)
			}
			if _, statErr := os.Lstat(filepath.Join(dir, recoveryFileName)); statErr != nil {
				t.Fatalf("journal missing: %v", statErr)
			}
			if _, retryErr := Sync(context.Background(), config(t, tc.replacement), Options{Dir: dir}); !errors.Is(retryErr, ErrStateCorrupt) {
				t.Fatalf("retry=%v", retryErr)
			}
			if got, readErr := os.ReadFile(backups[0]); readErr != nil || string(got) != "user edit made after digest check" {
				t.Fatalf("retry lost foreign backup=%q err=%v", got, readErr)
			}
		})
	}
}

func TestSyncFinalizationNeverDiscardsBackupChangedAfterCapture(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initial     Bundle
		replacement Bundle
		skill       string
		phase       string
	}{
		{
			name:        "update",
			initial:     bundle(t, "plugin", "final-update-old", "old"),
			replacement: bundle(t, "plugin", "final-update-new", "new"),
			skill:       "alpha",
			phase:       "publish",
		},
		{
			name:        "removal",
			initial:     bundleWith(t, "plugin", "final-remove-old", map[string]string{"alpha": "keep", "beta": "old"}),
			replacement: bundleWith(t, "plugin", "final-remove-new", map[string]string{"alpha": "keep"}),
			skill:       "beta",
			phase:       "backup",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Sync(context.Background(), config(t, tc.initial), Options{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			previous := transactionBoundary
			t.Cleanup(func() { transactionBoundary = previous })
			transactionBoundary = func(phase string) {
				if phase != tc.phase {
					return
				}
				backups, _ := filepath.Glob(filepath.Join(dir, transactionPrefix+"*", "backup", tc.skill, "SKILL.md"))
				if len(backups) == 1 {
					_ = os.WriteFile(backups[0], []byte("foreign after capture"), 0o644)
				}
			}
			report, err := Sync(context.Background(), config(t, tc.replacement), Options{Dir: dir})
			if !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("sync=%v report=%#v", err, report)
			}
			for _, change := range report.Changes {
				if change.Name == tc.skill && change.Outcome != Incomplete {
					t.Fatalf("change=%#v report=%#v", change, report)
				}
			}
			backups, globErr := filepath.Glob(filepath.Join(dir, transactionPrefix+"*", "backup", tc.skill, "SKILL.md"))
			if globErr != nil || len(backups) != 1 {
				t.Fatalf("backups=%v err=%v", backups, globErr)
			}
			if got, readErr := os.ReadFile(backups[0]); readErr != nil || string(got) != "foreign after capture" {
				t.Fatalf("foreign backup=%q err=%v", got, readErr)
			}
			transactionBoundary = previous
			if _, retryErr := Sync(context.Background(), config(t, tc.replacement), Options{Dir: dir}); !errors.Is(retryErr, ErrStateCorrupt) {
				t.Fatalf("retry=%v", retryErr)
			}
			if got, readErr := os.ReadFile(backups[0]); readErr != nil || string(got) != "foreign after capture" {
				t.Fatalf("retry deleted backup=%q err=%v", got, readErr)
			}
		})
	}
}

func TestTransactionFilesystemHelpersConstrainPathsAndPropagateFaults(t *testing.T) {
	dir := t.TempDir()
	if rel, err := rootRelative(dir, filepath.Join(dir, "alpha")); err != nil || rel != "alpha" {
		t.Fatalf("relative=%q err=%v", rel, err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "..", "outside")} {
		if _, err := rootRelative(dir, path); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unsafe path %q err=%v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "from"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename")
	withTransactionOperations(t, func(ops *transactionOperationSet) {
		ops.rename = func(*os.Root, string, string) error { return renameErr }
	})
	if err := rootedRename(dir, filepath.Join(dir, "from"), filepath.Join(dir, "to")); !errors.Is(err, renameErr) {
		t.Fatalf("rename=%v", err)
	}
	if err := rootedRename(dir, filepath.Join(dir, "..", "from"), filepath.Join(dir, "to")); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("escaping rename=%v", err)
	}
	removeErr := errors.New("remove")
	withTransactionOperations(t, func(ops *transactionOperationSet) { ops.removeAll = func(*os.Root, string) error { return removeErr } })
	if err := rootedRemoveAll(dir, filepath.Join(dir, "from")); !errors.Is(err, removeErr) {
		t.Fatalf("remove=%v", err)
	}
	if err := rootedRemoveAll(dir, filepath.Join(dir, "..", "from")); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("escaping remove=%v", err)
	}

	syncErr := errors.New("sync")
	previousOperations := transactionOperations
	t.Cleanup(func() { transactionOperations = previousOperations })
	transactionOperations.syncDirectory = func(string) error { return syncErr }
	if err := syncTransactionDirectories("", dir, dir); !errors.Is(err, syncErr) {
		t.Fatalf("transaction sync=%v", err)
	}
	transactionOperations = previousOperations
	if err := syncDirectoryChain(dir, filepath.Join(dir, "child")); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("escaping chain=%v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDirectoryAncestry(filepath.Join(file, "child"), os.MkdirAll); err == nil {
		t.Fatal("file ancestor accepted")
	}
	mkdirErr := errors.New("mkdir")
	if _, err := ensureDirectoryAncestry(filepath.Join(dir, "new", "child"), func(string, fs.FileMode) error { return mkdirErr }); !errors.Is(err, mkdirErr) {
		t.Fatalf("mkdir=%v", err)
	}
	if err := syncCreatedDirectoryAncestry([]string{filepath.Join(dir, "missing")}, func(string) error { return syncErr }); !errors.Is(err, syncErr) {
		t.Fatalf("created sync=%v", err)
	}
}

func TestTransactionStartAndRecordRejectUnsafeInput(t *testing.T) {
	for _, digest := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
		if validDigest(digest) {
			t.Fatalf("invalid digest accepted: %q", digest)
		}
	}
	if !validDigest(strings.Repeat("a", 64)) {
		t.Fatal("valid digest rejected")
	}
	dir := filepath.Join(t.TempDir(), "target")
	tx := newTransaction(dir)
	mkdirErr := errors.New("mkdir")
	previousOperations := transactionOperations
	t.Cleanup(func() { transactionOperations = previousOperations })
	transactionOperations.mkdirAll = func(string, fs.FileMode) error { return mkdirErr }
	if err := tx.start(); !errors.Is(err, mkdirErr) {
		t.Fatalf("start=%v", err)
	}
	transactionOperations = previousOperations
	tx = newTransaction(t.TempDir())
	if err := tx.record(txChange{Name: "../unsafe"}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("unsafe record=%v", err)
	}
	if tx.id == "" || len(tx.changes) != 0 {
		t.Fatalf("invalid record changed transaction: %#v", tx)
	}
}

func TestTransactionHelperErrorBoundaries(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rootedRename(file, "from", "to"); err == nil {
		t.Fatal("file root accepted for rename")
	}
	if err := rootedRename(dir, file, filepath.Join(dir, "..", "outside")); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("unsafe rename destination=%v", err)
	}
	if err := rootedRemoveAll(file, "from"); err == nil {
		t.Fatal("file root accepted for remove")
	}
	if err := syncDirectory(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing directory synced")
	}
	if err := syncDirectoryChain(filepath.Join(dir, "missing"), dir); err == nil {
		t.Fatal("missing chain synced")
	}
	if _, err := ensureDirectoryAncestry(filepath.Join(dir, strings.Repeat("a", 300)), os.MkdirAll); err == nil {
		t.Fatal("lstat failure accepted")
	}
	if _, err := ensureDirectoryAncestry(file, os.MkdirAll); err == nil {
		t.Fatal("file itself accepted as directory")
	}
	if err := syncCreatedDirectoryAncestry([]string{dir, dir}, func(string) error { return nil }); err != nil {
		t.Fatalf("duplicate created paths=%v", err)
	}

	t.Run("start sync creation failure", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		syncErr := errors.New("sync created")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.syncDirectory = func(string) error { return syncErr }
		tx := newTransaction(target)
		if err := tx.start(); !errors.Is(err, syncErr) {
			t.Fatalf("start=%v", err)
		}
	})
	t.Run("start temp creation failure", func(t *testing.T) {
		dir := t.TempDir()
		tempErr := errors.New("temp")
		withDurableFileOperations(t, func(ops *durableFileOperationSet) {
			ops.mkdirTemp = func(string, string) (string, error) { return "", tempErr }
		})
		tx := newTransaction(dir)
		if err := tx.start(); !errors.Is(err, tempErr) {
			t.Fatalf("start=%v", err)
		}
	})
	t.Run("record journal failure restores in-memory change", func(t *testing.T) {
		dir := t.TempDir()
		syncErr := errors.New("journal sync")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.syncDirectory = func(string) error { return syncErr }
		tx := newTransaction(dir)
		tx.id = transactionPrefix + "record"
		if err := os.Mkdir(filepath.Join(dir, tx.id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := tx.record(txChange{Name: "alpha", New: strings.Repeat("a", 64)}); !errors.Is(err, syncErr) {
			t.Fatalf("record=%v", err)
		}
		if len(tx.changes) != 0 {
			t.Fatalf("failed record retained=%#v", tx.changes)
		}
	})
	t.Run("record start failure retains no change", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "target")
		syncErr := errors.New("start sync")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.syncDirectory = func(string) error { return syncErr }
		tx := newTransaction(dir)
		if err := tx.record(txChange{Name: "alpha", New: strings.Repeat("a", 64)}); !errors.Is(err, syncErr) {
			t.Fatalf("record=%v", err)
		}
		if len(tx.changes) != 0 {
			t.Fatalf("failed start recorded=%#v", tx.changes)
		}
	})
}

func TestCopySkillAndLockFailClosed(t *testing.T) {
	t.Run("copies executable content durably", func(t *testing.T) {
		stage := filepath.Join(t.TempDir(), "stage")
		source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("body")}}
		if err := copySkill(source, "alpha", stage, map[string]bool{"alpha/SKILL.md": true}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(stage, "alpha", "SKILL.md"))
		if err != nil {
			t.Fatalf("mode=%v err=%v", info.Mode(), err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
			t.Fatalf("mode=%v", info.Mode())
		}
	})
	for _, tc := range []struct {
		name   string
		source fs.FS
	}{
		{name: "symlink", source: fstest.MapFS{"alpha/link": &fstest.MapFile{Mode: fs.ModeSymlink}}},
		{name: "non-regular", source: fstest.MapFS{"alpha/fifo": &fstest.MapFile{Mode: fs.ModeNamedPipe}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := copySkill(tc.source, "alpha", filepath.Join(t.TempDir(), "stage"), nil); err == nil {
				t.Fatal("unsafe source copied")
			}
		})
	}
	t.Run("write failure preserves no completed content", func(t *testing.T) {
		createErr := errors.New("create")
		withDurableFileOperations(t, func(ops *durableFileOperationSet) {
			ops.createFile = func(string, fs.FileMode) (durableFile, error) { return nil, createErr }
		})
		err := copySkill(fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("body")}}, "alpha", filepath.Join(t.TempDir(), "stage"), nil)
		if !errors.Is(err, createErr) {
			t.Fatalf("copy=%v", err)
		}
	})

	t.Run("lock serializes and observes cancellation", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "skills")
		unlock, err := lock(context.Background(), dir, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := lock(ctx, dir, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("contended lock=%v", err)
		}
	})
	t.Run("symlinked lock is refused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "skills")
		parent := filepath.Dir(dir)
		if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(parent, ".cli-helpers-skills-lock")); err != nil {
			t.Fatal(err)
		}
		if _, err := lock(context.Background(), dir, time.Second); err == nil {
			t.Fatal("symlinked lock accepted")
		}
	})
	t.Run("copy sync failure is retained", func(t *testing.T) {
		syncErr := errors.New("copy sync")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.syncDirectory = func(string) error { return syncErr }
		err := copySkill(fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("body")}}, "alpha", filepath.Join(t.TempDir(), "stage"), nil)
		if !errors.Is(err, syncErr) {
			t.Fatalf("copy sync=%v", err)
		}
	})
}

func TestTransactionMutationPreconditionsFailClosed(t *testing.T) {
	digest := strings.Repeat("a", 64)
	t.Run("backup helper accepts additions and rejects captured change", func(t *testing.T) {
		dir := t.TempDir()
		if err := verifyBackupChange(filepath.Join(dir, transactionPrefix+"missing"), recoveryChange{Name: "alpha"}); err != nil {
			t.Fatalf("addition backup=%v", err)
		}
		tx := filepath.Join(dir, transactionPrefix+"changed")
		writeRecoverySkill(t, filepath.Join(tx, "backup"), "alpha", "foreign")
		if err := verifyBackupChange(tx, recoveryChange{Name: "alpha", Old: digest, Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("changed backup=%v", err)
		}
	})
	t.Run("replace rejects invalid stage and source", func(t *testing.T) {
		dir := t.TempDir()
		tx := newTransaction(dir)
		tx.id = transactionPrefix + "stage"
		if err := os.MkdirAll(filepath.Join(dir, tx.id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tx.id, "stage"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := tx.replace(fstest.MapFS{}, nil, "alpha", "", digest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unsafe stage=%v", err)
		}
		tx = newTransaction(t.TempDir())
		if err := tx.replace(failingFS{err: errors.New("source")}, nil, "alpha", "", digest); err == nil {
			t.Fatal("failing source copied")
		}
	})
	t.Run("replace refuses target changed after planning", func(t *testing.T) {
		dir := t.TempDir()
		writeRecoverySkill(t, dir, "alpha", "current")
		tx := newTransaction(dir)
		source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("new")}}
		newDigest, err := subtreeDigest(source, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.replace(source, nil, "alpha", strings.Repeat("a", 64), newDigest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("changed target=%v", err)
		}
	})
	t.Run("remove is idempotent and rejects changed target", func(t *testing.T) {
		tx := newTransaction(t.TempDir())
		if err := tx.remove("alpha", digest); err != nil {
			t.Fatalf("missing remove=%v", err)
		}
		dir := t.TempDir()
		writeRecoverySkill(t, dir, "alpha", "current")
		tx = newTransaction(dir)
		if err := tx.remove("alpha", digest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("changed remove=%v", err)
		}
	})
}

func TestTransactionMutationFaultSeams(t *testing.T) {
	t.Run("backup validation errors", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"backup")
		if err := os.MkdirAll(tx, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tx, "backup"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyBackupChange(tx, recoveryChange{Name: "alpha", Old: strings.Repeat("a", 64), Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("backup directory=%v", err)
		}
		if err := os.Remove(filepath.Join(tx, "backup")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(tx, "backup"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dir, filepath.Join(tx, "backup", "alpha")); err != nil {
			t.Fatal(err)
		}
		if err := verifyBackupChange(tx, recoveryChange{Name: "alpha", Old: strings.Repeat("a", 64), Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("backup content=%v", err)
		}
	})
	t.Run("replace checks staged and proof content", func(t *testing.T) {
		dir := t.TempDir()
		source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("body")}}
		actual, err := subtreeDigest(source, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		tx := newTransaction(dir)
		if err := tx.replace(source, nil, "alpha", "", strings.Repeat("a", 64)); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("stage mismatch=%v", err)
		}
		tx = newTransaction(t.TempDir())
		if err := tx.replace(source, []string{"missing"}, "alpha", "", actual); err == nil {
			t.Fatal("invalid executable set accepted")
		}
	})
	t.Run("copy propagates source and read failures", func(t *testing.T) {
		if err := copySkill(failingFS{err: errors.New("walk")}, "alpha", filepath.Join(t.TempDir(), "stage"), nil); err == nil {
			t.Fatal("walk failure accepted")
		}
		base := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("body")}}
		readErr := errors.New("read")
		source := openFailureFS{FS: base, path: "alpha/SKILL.md", err: readErr}
		if err := copySkill(source, "alpha", filepath.Join(t.TempDir(), "stage"), nil); !errors.Is(err, readErr) {
			t.Fatalf("read=%v", err)
		}
	})
}

func TestReplaceAndRemoveRetainRecoveryEvidenceOnIOFaults(t *testing.T) {
	source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("new")}}
	newDigest, err := subtreeDigest(source, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	setupUpdate := func(t *testing.T) (string, *transaction, string) {
		t.Helper()
		dir := t.TempDir()
		oldDigest := writeRecoverySkill(t, dir, "alpha", "old")
		tx := newTransaction(dir)
		return dir, &tx, oldDigest
	}
	t.Run("replace proof copy failure", func(t *testing.T) {
		dir, tx, oldDigest := setupUpdate(t)
		copyErr := errors.New("proof copy")
		withDurableFileOperations(t, func(ops *durableFileOperationSet) {
			original, calls := ops.createFile, 0
			ops.createFile = func(path string, mode fs.FileMode) (durableFile, error) {
				calls++
				if calls == 2 {
					return nil, copyErr
				}
				return original(path, mode)
			}
		})
		if err := tx.replace(source, nil, "alpha", oldDigest, newDigest); !errors.Is(err, copyErr) {
			t.Fatalf("replace=%v", err)
		}
		if got, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(got) != "old" {
			t.Fatalf("target=%q err=%v", got, readErr)
		}
	})
	t.Run("replace backup rename failure", func(t *testing.T) {
		dir, tx, oldDigest := setupUpdate(t)
		renameErr := errors.New("backup rename")
		withTransactionOperations(t, func(ops *transactionOperationSet) {
			original := ops.rename
			ops.rename = func(root *os.Root, from, to string) error {
				if from == "alpha" && strings.Contains(to, "/backup/alpha") {
					return renameErr
				}
				return original(root, from, to)
			}
		})
		if err := tx.replace(source, nil, "alpha", oldDigest, newDigest); !errors.Is(err, renameErr) {
			t.Fatalf("replace=%v", err)
		}
		if got, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(got) != "old" {
			t.Fatalf("target=%q err=%v", got, readErr)
		}
	})
	t.Run("remove backup rename failure", func(t *testing.T) {
		dir := t.TempDir()
		oldDigest := writeRecoverySkill(t, dir, "alpha", "old")
		tx := newTransaction(dir)
		renameErr := errors.New("remove rename")
		withTransactionOperations(t, func(ops *transactionOperationSet) {
			original := ops.rename
			ops.rename = func(root *os.Root, from, to string) error {
				if from == "alpha" && strings.Contains(to, "/backup/alpha") {
					return renameErr
				}
				return original(root, from, to)
			}
		})
		if err := tx.remove("alpha", oldDigest); !errors.Is(err, renameErr) {
			t.Fatalf("remove=%v", err)
		}
		if got, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(got) != "old" {
			t.Fatalf("target=%q err=%v", got, readErr)
		}
	})
}

func TestRemoveFailureBoundariesPreserveTarget(t *testing.T) {
	digest := strings.Repeat("a", 64)
	t.Run("start and digest failures", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "target")
		startErr := errors.New("start")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.mkdirAll = func(string, fs.FileMode) error { return startErr }
		tx := newTransaction(dir)
		if err := tx.remove("alpha", digest); !errors.Is(err, startErr) {
			t.Fatalf("start=%v", err)
		}
		transactionOperations = previous
		tx = newTransaction(t.TempDir())
		if err := tx.remove(strings.Repeat("a", 300), digest); err == nil {
			t.Fatal("digest lookup failure accepted")
		}
	})
	t.Run("unsafe backup leaves old target", func(t *testing.T) {
		dir := t.TempDir()
		oldDigest := writeRecoverySkill(t, dir, "alpha", "old")
		tx := newTransaction(dir)
		tx.id = transactionPrefix + "unsafe-backup"
		if err := os.Mkdir(filepath.Join(dir, tx.id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tx.id, "backup"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := tx.remove("alpha", oldDigest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("remove=%v", err)
		}
		if got, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(got) != "old" {
			t.Fatalf("target=%q err=%v", got, readErr)
		}
	})
}

func TestReplaceAndCopyRemainingFaultPaths(t *testing.T) {
	source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("new")}}
	newDigest, err := subtreeDigest(source, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("replace start failure", func(t *testing.T) {
		tx := newTransaction(filepath.Join(t.TempDir(), "target"))
		startErr := errors.New("start")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.mkdirAll = func(string, fs.FileMode) error { return startErr }
		if err := tx.replace(source, nil, "alpha", "", newDigest); !errors.Is(err, startErr) {
			t.Fatalf("replace=%v", err)
		}
	})
	t.Run("replace rejects unsafe proof and target", func(t *testing.T) {
		dir := t.TempDir()
		tx := newTransaction(dir)
		tx.id = transactionPrefix + "proof"
		if err := os.Mkdir(filepath.Join(dir, tx.id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tx.id, "proof"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := tx.replace(source, nil, "alpha", "", newDigest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("proof=%v", err)
		}
		dir = t.TempDir()
		if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(dir, "alpha")); err != nil {
			t.Fatal(err)
		}
		tx = newTransaction(dir)
		if err := tx.replace(source, nil, "alpha", "", newDigest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("target=%v", err)
		}
	})
	t.Run("replace rejects corrupted proof bytes", func(t *testing.T) {
		tx := newTransaction(t.TempDir())
		withDurableFileOperations(t, func(ops *durableFileOperationSet) {
			original, calls := ops.createFile, 0
			ops.createFile = func(path string, mode fs.FileMode) (durableFile, error) {
				file, err := original(path, mode)
				if err != nil {
					return nil, err
				}
				calls++
				if calls == 2 {
					return replacementContentFile{durableFile: file}, nil
				}
				return file, nil
			}
		})
		if err := tx.replace(source, nil, "alpha", "", newDigest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("proof=%v", err)
		}
	})
	t.Run("replace rejects unsafe backup", func(t *testing.T) {
		dir := t.TempDir()
		old := writeRecoverySkill(t, dir, "alpha", "old")
		tx := newTransaction(dir)
		tx.id = transactionPrefix + "backup"
		if err := os.Mkdir(filepath.Join(dir, tx.id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tx.id, "backup"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := tx.replace(source, nil, "alpha", old, newDigest); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("backup=%v", err)
		}
	})
	t.Run("copy mkdir failure", func(t *testing.T) {
		mkdirErr := errors.New("mkdir")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		transactionOperations.mkdirAll = func(string, fs.FileMode) error { return mkdirErr }
		if err := copySkill(source, "alpha", filepath.Join(t.TempDir(), "stage"), nil); !errors.Is(err, mkdirErr) {
			t.Fatalf("copy=%v", err)
		}
	})
}

func TestMutationBackupDirectoryFaults(t *testing.T) {
	source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("new")}}
	newDigest, err := subtreeDigest(source, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, removal := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace", true: "remove"}[removal], func(t *testing.T) {
			dir := t.TempDir()
			old := writeRecoverySkill(t, dir, "alpha", "old")
			tx := newTransaction(dir)
			mkdirErr := errors.New("backup mkdir")
			previous := transactionOperations
			t.Cleanup(func() { transactionOperations = previous })
			transactionOperations.mkdirAll = func(path string, mode fs.FileMode) error {
				if filepath.Base(path) == "backup" {
					return mkdirErr
				}
				return previous.mkdirAll(path, mode)
			}
			var got error
			if removal {
				got = tx.remove("alpha", old)
			} else {
				got = tx.replace(source, nil, "alpha", old, newDigest)
			}
			if !errors.Is(got, mkdirErr) {
				t.Fatalf("mutation=%v", got)
			}
			if data, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(data) != "old" {
				t.Fatalf("target=%q err=%v", data, readErr)
			}
		})
	}
}

func TestRemainingMutationDurabilityFaults(t *testing.T) {
	source := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte("new")}}
	newDigest, err := subtreeDigest(source, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		removal  bool
		failAt   int
		failPath func(dir string, path string) bool
	}{
		{name: "backup directory sync", failPath: func(_ string, path string) bool { return filepath.Base(path) == "backup" }},
		{name: "backup phase journal", failAt: 4, failPath: func(dir, path string) bool { return path == dir }},
		{name: "publish parent sync", failAt: 5, failPath: func(dir, path string) bool { return path == dir }},
		{name: "remove journal", removal: true, failAt: 2, failPath: func(dir, path string) bool { return path == dir }},
		{name: "remove backup sync", removal: true, failPath: func(_ string, path string) bool { return filepath.Base(path) == "backup" }},
		{name: "remove rename parent", removal: true, failAt: 3, failPath: func(dir, path string) bool { return path == dir }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			old := writeRecoverySkill(t, dir, "alpha", "old")
			tx := newTransaction(dir)
			syncErr := errors.New(tc.name)
			previous := transactionOperations
			t.Cleanup(func() { transactionOperations = previous })
			calls := 0
			transactionOperations.syncDirectory = func(path string) error {
				if tc.failPath(dir, path) {
					calls++
					if tc.failAt == 0 || calls == tc.failAt {
						return syncErr
					}
				}
				return previous.syncDirectory(path)
			}
			var got error
			if tc.removal {
				got = tx.remove("alpha", old)
			} else {
				got = tx.replace(source, nil, "alpha", old, newDigest)
			}
			if !errors.Is(got, syncErr) {
				t.Fatalf("mutation=%v calls=%d", got, calls)
			}
		})
	}
	t.Run("copy parent mkdir and sorting", func(t *testing.T) {
		parentErr := errors.New("parent mkdir")
		previous := transactionOperations
		t.Cleanup(func() { transactionOperations = previous })
		calls := 0
		transactionOperations.mkdirAll = func(path string, mode fs.FileMode) error {
			calls++
			if calls == 2 {
				return parentErr
			}
			return previous.mkdirAll(path, mode)
		}
		if err := copySkill(source, "alpha", filepath.Join(t.TempDir(), "stage"), nil); !errors.Is(err, parentErr) {
			t.Fatalf("parent mkdir=%v", err)
		}
		transactionOperations = previous
		nested := fstest.MapFS{
			"alpha/a/SKILL.md": &fstest.MapFile{Data: []byte("nested")},
			"alpha/SKILL.md":   &fstest.MapFile{Data: []byte("root")},
		}
		if err := copySkill(nested, "alpha", filepath.Join(t.TempDir(), "stage"), nil); err != nil {
			t.Fatalf("sorted copy=%v", err)
		}
	})
}

func TestRecoveryRetriesDirectorySyncBeforeDiscardingEvidence(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "retry-old", "old")
	newer := bundle(t, "plugin", "retry-new", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	stateSyncErr := errors.New("state directory sync")
	previous := stateDirectorySync
	t.Cleanup(func() { stateDirectorySync = previous })
	stateDirectorySync = func(string) error { return stateSyncErr }
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if !errors.Is(err, stateSyncErr) || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, readErr := os.Lstat(filepath.Join(dir, recoveryFileName)); readErr != nil {
		t.Fatalf("missing recovery journal: %v", readErr)
	}
	if data, readErr := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); readErr != nil || string(data) != "new" {
		t.Fatalf("published target=%q err=%v", data, readErr)
	}
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir}); !errors.Is(err, stateSyncErr) {
		t.Fatalf("retry error=%v", err)
	}
	if _, readErr := os.Lstat(filepath.Join(dir, recoveryFileName)); readErr != nil {
		t.Fatalf("retry discarded evidence: %v", readErr)
	}
	stateDirectorySync = previous
	if _, err := Sync(context.Background(), config(t, newer), Options{Dir: dir}); err != nil {
		t.Fatalf("durable retry: %v", err)
	}
	if _, readErr := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(readErr, fs.ErrNotExist) {
		t.Fatalf("journal remains: %v", readErr)
	}
}

func TestRestoreChangeRetriesPersistenceWhenBytesAlreadyMatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string) recoveryChange
	}{
		{name: "added target already removed", setup: func(*testing.T, string) recoveryChange {
			return recoveryChange{Name: "alpha", New: strings.Repeat("a", 64), Phase: "published"}
		}},
		{name: "original already restored", setup: func(t *testing.T, dir string) recoveryChange {
			if err := os.Mkdir(filepath.Join(dir, "alpha"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			digest, err := installedDigest(dir, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			return recoveryChange{Name: "alpha", Old: digest, New: strings.Repeat("b", 64), Existed: true, Phase: "backed_up"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			change := tc.setup(t, dir)
			persistenceErr := errors.New("retry parent sync")
			withTransactionOperations(t, func(ops *transactionOperationSet) {
				original := ops.syncDirectory
				calls := 0
				ops.syncDirectory = func(path string) error {
					if path == dir {
						calls++
						if calls == 1 {
							return persistenceErr
						}
					}
					return original(path)
				}
			})
			if err := restoreChange(dir, filepath.Join(dir, transactionPrefix+"retry"), change); !errors.Is(err, persistenceErr) {
				t.Fatalf("first restore error=%v", err)
			}
			if err := restoreChange(dir, filepath.Join(dir, transactionPrefix+"retry"), change); err != nil {
				t.Fatalf("persistence retry error=%v", err)
			}
		})
	}
}

func TestRestoreChangeUsesOnlyVerifiedProofOrBackup(t *testing.T) {
	t.Run("removes verified added target", func(t *testing.T) {
		dir, tx := t.TempDir(), filepath.Join(t.TempDir(), "unused")
		if err := os.Mkdir(filepath.Join(dir, "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		newDigest, err := installedDigest(dir, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(tx, "proof", "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tx, "proof", "alpha", "SKILL.md"), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest, Phase: "published"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "alpha")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("added target remains: %v", err)
		}
	})
	t.Run("rejects mismatched backup", func(t *testing.T) {
		dir, tx := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(tx, "backup", "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tx, "backup", "alpha", "SKILL.md"), []byte("wrong"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: strings.Repeat("a", 64), New: strings.Repeat("b", 64), Existed: true, Phase: "published"}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCommittedRecoveryValidationAndFinalizationFaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := installedDigest(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyCommittedChange(dir, recoveryChange{Name: "alpha", New: digest, Phase: "published"}); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommittedChange(dir, recoveryChange{Name: "alpha", New: strings.Repeat("a", 64), Phase: "published"}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("mismatch=%v", err)
	}
	if err := verifyCommittedChange(dir, recoveryChange{Name: "alpha", Phase: "backed_up"}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("reappeared removal=%v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommittedChange(dir, recoveryChange{Name: "alpha", Phase: "backed_up"}); err != nil {
		t.Fatal(err)
	}

	tx := filepath.Join(dir, transactionPrefix+"cleanup")
	if err := os.Mkdir(tx, 0o755); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, recoveryFileName)
	if err := os.WriteFile(journal, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeErr := errors.New("journal remove")
	withTransactionOperations(t, func(ops *transactionOperationSet) { ops.remove = func(string) error { return removeErr } })
	if err := finalizeJournal(journal, tx); !errors.Is(err, removeErr) {
		t.Fatalf("finalize=%v", err)
	}
	if _, err := os.Lstat(journal); err != nil {
		t.Fatalf("journal evidence lost: %v", err)
	}
}

func TestReadRecoveryJournalRejectsUnsafeMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recoveryFileName)
	for _, raw := range [][]byte{[]byte("{"), []byte(`{"schema":2,"id":"x","transaction":"bad","changes":[]}`)} {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := readRecoveryJournal(dir); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readRecoveryJournal(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing=%v", err)
	}
}

func writeRecoveryJournal(t *testing.T, dir, id string, changes []recoveryChange) string {
	t.Helper()
	raw, err := json.Marshal(recoveryJournal{Schema: 2, ID: id, Transaction: transactionPrefix + id, Changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, recoveryFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecoveryValidationHelpersRejectEveryUnsafeShape(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := recoveryJournal{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: []recoveryChange{{Name: "alpha", New: digest, Phase: "prepared"}}}
	if !validJournal(valid) {
		t.Fatal("valid journal rejected")
	}
	for _, journal := range []recoveryJournal{
		{Schema: 1, ID: "safe", Transaction: transactionPrefix + "safe", Changes: valid.Changes},
		{Schema: 2, Transaction: transactionPrefix + "safe", Changes: valid.Changes},
		{Schema: 2, ID: "safe", Transaction: "wrong", Changes: valid.Changes},
		{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: nil},
		{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: []recoveryChange{{Name: "../escape", New: digest, Phase: "prepared"}}},
		{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: []recoveryChange{{Name: "alpha", New: digest, Phase: "bad"}}},
		{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: []recoveryChange{{Name: "alpha", Existed: true, New: digest, Phase: "prepared"}}},
		{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: []recoveryChange{{Name: "alpha", Old: digest, Phase: "prepared"}}},
		{Schema: 2, ID: "safe", Transaction: transactionPrefix + "safe", Changes: []recoveryChange{{Name: "alpha", New: digest, Phase: "prepared"}, {Name: "alpha", New: digest, Phase: "prepared"}}},
	} {
		if validJournal(journal) {
			t.Fatalf("unsafe journal accepted: %#v", journal)
		}
	}
	if !stateOwnsDigest(state{Plugins: map[string]pluginState{"one/two": {Skills: map[string]string{"alpha": digest}}}}, "alpha", digest) {
		t.Fatal("known ownership not found")
	}
	if stateOwnsDigest(state{Plugins: map[string]pluginState{"one/two": {Skills: map[string]string{"alpha": digest}}}}, "alpha", strings.Repeat("b", 64)) {
		t.Fatal("wrong digest accepted as owned")
	}

	dir := t.TempDir()
	if err := validTransactionDir(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing transaction rejected: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validTransactionDir(file); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("file transaction=%v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := validTransactionDir(link); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("symlink transaction=%v", err)
	}
	if err := checkTransactionSubdir(dir, "missing"); err != nil {
		t.Fatalf("missing subdir=%v", err)
	}
	if err := checkTransactionSubdir(dir, "file"); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("file subdir=%v", err)
	}
	if err := checkTransactionSubdir(dir, "link"); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("symlink subdir=%v", err)
	}
}

func TestDigestAtConfinesAndVerifiesDirectoryContent(t *testing.T) {
	dir := t.TempDir()
	if exists, _, err := digestAt(filepath.Join(dir, "missing"), "alpha"); exists || err != nil {
		t.Fatalf("missing root exists=%v err=%v", exists, err)
	}
	if exists, _, err := digestAt(dir, "missing"); exists || err != nil {
		t.Fatalf("missing skill exists=%v err=%v", exists, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, _, err := digestAt(dir, "file"); !exists || !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("file exists=%v err=%v", exists, err)
	}
	if err := os.Symlink(filepath.Join(dir, "file"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if exists, _, err := digestAt(dir, "link"); !exists || !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("link exists=%v err=%v", exists, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if exists, digest, err := digestAt(dir, "alpha"); !exists || digest == "" || err != nil {
		t.Fatalf("directory exists=%v digest=%q err=%v", exists, digest, err)
	}
}

func TestRecoverTransactionRejectsUnownedPriorContentAndRetriesFinalization(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "owned-old", "old")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	oldDigest, err := installedDigest(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	txID := transactionPrefix + "owned"
	if err := os.MkdirAll(filepath.Join(dir, txID, "backup", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, txID, "backup", "alpha", "SKILL.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, txID, "backup", "alpha", "reference.md"), []byte("reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	newDigest, err := installedDigest(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryJournal(t, dir, "owned", []recoveryChange{{Name: "alpha", Old: oldDigest, New: newDigest, Existed: true, Phase: "published"}})
	stateRaw, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("unowned recovery=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(data) != "new" {
		t.Fatalf("target lost=%q err=%v", data, err)
	}
	if err := os.WriteFile(statePath(dir), stateRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir); err != nil {
		t.Fatalf("owned recovery=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(data) != "old" {
		t.Fatalf("target not restored=%q err=%v", data, err)
	}
}

func TestPlanningFailureAndUnstartedRollbackPreserveTarget(t *testing.T) {
	longName := strings.Repeat("a", 300)
	b := bundleWith(t, "plugin", "too-long-for-target", map[string]string{longName: "body"})
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "dry-run"}[dryRun], func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "unrelated.txt")
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Sync(context.Background(), config(t, b), Options{Dir: dir, DryRun: dryRun}); err == nil {
				t.Fatal("too-long target component was accepted")
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
				t.Fatalf("planning failure deleted target content=%q err=%v", got, err)
			}
		})
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx := newTransaction(dir)
	if err := tx.rollback(); err != nil {
		t.Fatalf("unstarted rollback=%v", err)
	}
	prepared, err := Prepare(context.Background(), config(t, bundle(t, "plugin", "cancel", "body")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	report := Report{Dir: dir, CLI: prepared.cfg.CLI, CLIVersion: prepared.cfg.CurrentVersion}
	if _, err := syncLocked(canceled, prepared.cfg, prepared.bundles, Options{Dir: dir}, report); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-transaction cancellation=%v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("cancellation deleted target content=%q err=%v", got, err)
	}
}

func TestFinalizeJournalRejectsUnownedCleanupPaths(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, recoveryFileName)
	if err := os.WriteFile(journal, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeJournal(journal, dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("unowned transaction cleanup=%v", err)
	}
	if got, err := os.ReadFile(journal); err != nil || string(got) != "keep" {
		t.Fatalf("unsafe cleanup removed journal=%q err=%v", got, err)
	}
}

func writeRecoverySkill(t *testing.T, root, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := installedDigest(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestRecoveryHelpersPropagateFilesystemFaultsWithoutMutation(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readRecoveryJournal(parentFile); err == nil {
		t.Fatal("journal lstat fault accepted")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, recoveryFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readRecoveryJournal(dir); err == nil {
		t.Fatal("journal read fault accepted")
	}
	if err := validTransactionDir(filepath.Join(parentFile, "child")); err == nil {
		t.Fatal("transaction lstat fault accepted")
	}
	if err := checkTransactionSubdir(parentFile, "child"); err == nil {
		t.Fatal("subdir lstat fault accepted")
	}
	if _, _, err := digestAt(parentFile, "alpha"); err == nil {
		t.Fatal("root open fault accepted")
	}
	if _, _, err := digestAt(t.TempDir(), strings.Repeat("a", 300)); err == nil {
		t.Fatal("content lstat fault accepted")
	}
	unsafeTree := t.TempDir()
	if err := os.Mkdir(filepath.Join(unsafeTree, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parentFile, filepath.Join(unsafeTree, "alpha", "unsafe")); err != nil {
		t.Fatal(err)
	}
	if exists, _, err := digestAt(unsafeTree, "alpha"); !exists || !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("unsafe tree exists=%v err=%v", exists, err)
	}
	if err := verifyCommittedChange(t.TempDir(), recoveryChange{Name: strings.Repeat("a", 300), New: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("committed verification accepted invalid filesystem lookup")
	}
}

func TestRestoreChangeFailsClosedOnEveryMissingRecoveryProof(t *testing.T) {
	t.Run("added target needs proof", func(t *testing.T) {
		dir, tx := t.TempDir(), t.TempDir()
		newDigest := writeRecoverySkill(t, dir, "alpha", "new")
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("missing proof=%v", err)
		}
		if got, err := os.ReadFile(filepath.Join(dir, "alpha", "SKILL.md")); err != nil || string(got) != "new" {
			t.Fatalf("target changed=%q err=%v", got, err)
		}
	})
	t.Run("added target must equal proof", func(t *testing.T) {
		dir, tx := t.TempDir(), t.TempDir()
		newDigest := writeRecoverySkill(t, dir, "alpha", "new")
		writeRecoverySkill(t, filepath.Join(tx, "proof"), "alpha", "other")
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("mismatched proof=%v", err)
		}
	})
	t.Run("original target needs backup", func(t *testing.T) {
		dir, tx := t.TempDir(), t.TempDir()
		oldDigest := strings.Repeat("a", 64)
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: strings.Repeat("b", 64), Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("missing backup=%v", err)
		}
	})
	t.Run("original target rejects unrelated replacement", func(t *testing.T) {
		dir, tx := t.TempDir(), t.TempDir()
		oldDigest := writeRecoverySkill(t, filepath.Join(tx, "backup"), "alpha", "old")
		writeRecoverySkill(t, dir, "alpha", "unrelated")
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: strings.Repeat("b", 64), Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unrelated target=%v", err)
		}
	})
	t.Run("rename failure retains verified backup", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"rename")
		oldDigest := writeRecoverySkill(t, filepath.Join(tx, "backup"), "alpha", "old")
		renameErr := errors.New("rename")
		withTransactionOperations(t, func(ops *transactionOperationSet) {
			ops.rename = func(*os.Root, string, string) error { return renameErr }
		})
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: strings.Repeat("b", 64), Existed: true}); !errors.Is(err, renameErr) {
			t.Fatalf("rename error=%v", err)
		}
		if _, err := os.Lstat(filepath.Join(tx, "backup", "alpha", "SKILL.md")); err != nil {
			t.Fatalf("backup lost=%v", err)
		}
	})
}

func TestRestoreChangeCoversVerifiedRecoveryFaults(t *testing.T) {
	t.Run("target digest read failure", func(t *testing.T) {
		if err := restoreChange(t.TempDir(), t.TempDir(), recoveryChange{Name: strings.Repeat("a", 300)}); err == nil {
			t.Fatal("target digest lookup failure accepted")
		}
	})
	t.Run("unsafe proof directory", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"proof-dir")
		newDigest := writeRecoverySkill(t, dir, "alpha", "new")
		if err := os.MkdirAll(tx, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tx, "proof"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unsafe proof directory=%v", err)
		}
	})
	t.Run("unsafe proof content", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"proof-content")
		newDigest := writeRecoverySkill(t, dir, "alpha", "new")
		if err := os.MkdirAll(filepath.Join(tx, "proof"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "alpha"), filepath.Join(tx, "proof", "alpha")); err != nil {
			t.Fatal(err)
		}
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unsafe proof content=%v", err)
		}
	})
	t.Run("target differs from verified proof", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"proof-mismatch")
		writeRecoverySkill(t, dir, "alpha", "target")
		newDigest := writeRecoverySkill(t, filepath.Join(tx, "proof"), "alpha", "proof")
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("target differs=%v", err)
		}
	})
	t.Run("remove failure retains added target", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"remove-added")
		newDigest := writeRecoverySkill(t, dir, "alpha", "new")
		writeRecoverySkill(t, filepath.Join(tx, "proof"), "alpha", "new")
		removeErr := errors.New("remove added")
		withTransactionOperations(t, func(ops *transactionOperationSet) { ops.removeAll = func(*os.Root, string) error { return removeErr } })
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", New: newDigest}); !errors.Is(err, removeErr) {
			t.Fatalf("remove added=%v", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "alpha", "SKILL.md")); err != nil {
			t.Fatalf("target lost=%v", err)
		}
	})
	t.Run("unsafe backup directory", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"backup-dir")
		if err := os.MkdirAll(tx, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tx, "backup"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: strings.Repeat("a", 64), New: strings.Repeat("b", 64), Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unsafe backup directory=%v", err)
		}
	})
	t.Run("unsafe backup content", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"backup-content")
		if err := os.MkdirAll(filepath.Join(tx, "backup"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dir, filepath.Join(tx, "backup", "alpha")); err != nil {
			t.Fatal(err)
		}
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: strings.Repeat("a", 64), New: strings.Repeat("b", 64), Existed: true}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("unsafe backup content=%v", err)
		}
	})
	t.Run("already restored target is durable", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"already-restored")
		oldDigest := writeRecoverySkill(t, dir, "alpha", "old")
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: strings.Repeat("b", 64), Existed: true}); err != nil {
			t.Fatalf("already restored=%v", err)
		}
	})
	t.Run("existing old target is durable without backup", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"old")
		oldDigest := writeRecoverySkill(t, filepath.Join(tx, "backup"), "alpha", "old")
		writeRecoverySkill(t, dir, "alpha", "old")
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: strings.Repeat("b", 64), Existed: true}); err != nil {
			t.Fatalf("existing old target=%v", err)
		}
	})
	t.Run("remove old published target failure retains backup", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"remove-old")
		oldDigest := writeRecoverySkill(t, filepath.Join(tx, "backup"), "alpha", "old")
		newDigest := writeRecoverySkill(t, dir, "alpha", "new")
		removeErr := errors.New("remove old")
		withTransactionOperations(t, func(ops *transactionOperationSet) { ops.removeAll = func(*os.Root, string) error { return removeErr } })
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: newDigest, Existed: true}); !errors.Is(err, removeErr) {
			t.Fatalf("remove old=%v", err)
		}
		if _, err := os.Lstat(filepath.Join(tx, "backup", "alpha", "SKILL.md")); err != nil {
			t.Fatalf("backup lost=%v", err)
		}
	})
	t.Run("mkdir failure retains backup", func(t *testing.T) {
		dir := t.TempDir()
		tx := filepath.Join(dir, transactionPrefix+"mkdir")
		oldDigest := writeRecoverySkill(t, filepath.Join(tx, "backup"), "alpha", "old")
		mkdirErr := errors.New("mkdir")
		withTransactionOperations(t, func(ops *transactionOperationSet) { ops.mkdirAll = func(string, fs.FileMode) error { return mkdirErr } })
		if err := restoreChange(dir, tx, recoveryChange{Name: "alpha", Old: oldDigest, New: strings.Repeat("b", 64), Existed: true}); !errors.Is(err, mkdirErr) {
			t.Fatalf("mkdir=%v", err)
		}
		if _, err := os.Lstat(filepath.Join(tx, "backup", "alpha", "SKILL.md")); err != nil {
			t.Fatalf("backup lost=%v", err)
		}
	})
}

func TestRecoverCommittedMismatchAndFinalizePersistenceFaults(t *testing.T) {
	dir := t.TempDir()
	digest := writeRecoverySkill(t, dir, "alpha", "stable")
	state := state{RecoveryID: "committed", Plugins: map[string]pluginState{"strongo/plugin": {Legacy: true, Skills: map[string]string{"alpha": digest}}}}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	writeRecoveryJournal(t, dir, "committed", []recoveryChange{{Name: "alpha", New: strings.Repeat("a", 64), Phase: "published"}})
	if err := recoverTransaction(dir); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("committed mismatch=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("journal removed=%v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir, tx, journal string)
	}{
		{name: "unsafe transaction", setup: func(t *testing.T, dir, tx, _ string) {
			if err := os.Symlink(dir, tx); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "sync after transaction cleanup", setup: func(t *testing.T, _ string, tx, _ string) {
			if err := os.Mkdir(tx, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tx := filepath.Join(root, transactionPrefix+"finalize")
			journal := filepath.Join(root, recoveryFileName)
			if err := os.WriteFile(journal, []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, root, tx, journal)
			if tc.name == "sync after transaction cleanup" {
				syncErr := errors.New("sync cleanup")
				withTransactionOperations(t, func(ops *transactionOperationSet) { ops.syncDirectory = func(string) error { return syncErr } })
				if err := finalizeJournal(journal, tx); !errors.Is(err, syncErr) {
					t.Fatalf("sync cleanup=%v", err)
				}
				if _, err := os.Lstat(journal); err != nil {
					t.Fatalf("journal removed before persistence=%v", err)
				}
				return
			}
			if err := finalizeJournal(journal, tx); !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("unsafe transaction=%v", err)
			}
		})
	}
}
