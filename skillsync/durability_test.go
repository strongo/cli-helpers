package skillsync

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
