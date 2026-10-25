//go:build windows

package skillsync

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type testDirectorySyncHandle struct {
	syncErr  error
	closeErr error
}

func (h testDirectorySyncHandle) Sync() error  { return h.syncErr }
func (h testDirectorySyncHandle) Close() error { return h.closeErr }

func TestWindowsDirectorySyncRetainsEveryBoundaryFailure(t *testing.T) {
	openErr := errors.New("open")
	syncErr := errors.New("sync")
	closeErr := errors.New("close")
	for _, tc := range []struct {
		name string
		open func(string) (directorySyncHandle, error)
		want []error
	}{
		{name: "open", open: func(string) (directorySyncHandle, error) { return nil, openErr }, want: []error{openErr}},
		{name: "sync", open: func(string) (directorySyncHandle, error) { return testDirectorySyncHandle{syncErr: syncErr}, nil }, want: []error{syncErr}},
		{name: "close", open: func(string) (directorySyncHandle, error) { return testDirectorySyncHandle{closeErr: closeErr}, nil }, want: []error{closeErr}},
		{name: "sync and close", open: func(string) (directorySyncHandle, error) {
			return testDirectorySyncHandle{syncErr: syncErr, closeErr: closeErr}, nil
		}, want: []error{syncErr, closeErr}},
		{name: "success", open: func(string) (directorySyncHandle, error) { return testDirectorySyncHandle{}, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := openDirectoryForSync
			openDirectoryForSync = tc.open
			t.Cleanup(func() { openDirectoryForSync = previous })
			err := syncDirectory(t.TempDir())
			for _, want := range tc.want {
				if !errors.Is(err, want) {
					t.Fatalf("error=%v does not retain %v", err, want)
				}
			}
			if len(tc.want) == 0 && err != nil {
				t.Fatalf("success error=%v", err)
			}
		})
	}
}

func TestWindowsDirectoryHandleReportsInvalidHandleFailures(t *testing.T) {
	handle := windowsDirectorySyncHandle{handle: syscall.InvalidHandle}
	if err := handle.Sync(); err == nil {
		t.Fatal("invalid handle flushed successfully")
	}
	closeErr := errors.New("close")
	previous := closeDirectoryHandle
	closeDirectoryHandle = func(syscall.Handle) error { return closeErr }
	t.Cleanup(func() { closeDirectoryHandle = previous })
	if err := handle.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error=%v", err)
	}
}

func TestOpenWindowsDirectoryForSyncRejectsInvalidAndMissingPaths(t *testing.T) {
	if _, err := openWindowsDirectoryForSync("bad\x00path"); err == nil {
		t.Fatal("NUL path opened")
	}
	_, err := openWindowsDirectoryForSync(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		t.Fatalf("missing directory error=%v", err)
	}
}

func TestWindowsNativeDirectorySync(t *testing.T) {
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatalf("native directory sync: %v", err)
	}
}

func TestWindowsNativeDirectorySyncAndRecoveryJourney(t *testing.T) {
	dir := t.TempDir()
	old := bundle(t, "plugin", "windows-old", "old")
	newer := bundle(t, "plugin", "windows-new", "new")
	if _, err := Sync(context.Background(), config(t, old), Options{Dir: dir}); err != nil {
		t.Fatalf("initial native sync: %v", err)
	}

	previous := stateDirectorySync
	t.Cleanup(func() { stateDirectorySync = previous })
	stateDirectorySync = func(string) error { return errors.New("injected final state sync failure") }
	report, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err == nil || report.Changes[0].Outcome != Incomplete {
		t.Fatalf("interrupted update report=%#v err=%v", report, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
		t.Fatalf("missing recovery journal: %v", err)
	}
	stateDirectorySync = previous

	recovered, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil || strings.Join(recovered.Names(Unchanged), ",") != "alpha" {
		t.Fatalf("native recovery report=%#v err=%v", recovered, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recovery journal remains: %v", err)
	}
	repeat, err := Sync(context.Background(), config(t, newer), Options{Dir: dir})
	if err != nil || strings.Join(repeat.Names(Unchanged), ",") != "alpha" {
		t.Fatalf("native repeat report=%#v err=%v", repeat, err)
	}
}
