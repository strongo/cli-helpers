package skillsync

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type coverageInfo struct{ mode fs.FileMode }

func (i coverageInfo) Name() string       { return "x" }
func (i coverageInfo) Size() int64        { return 0 }
func (i coverageInfo) Mode() fs.FileMode  { return i.mode }
func (i coverageInfo) ModTime() time.Time { return time.Time{} }
func (i coverageInfo) IsDir() bool        { return i.mode.IsDir() }
func (i coverageInfo) Sys() any           { return nil }

type coverageLock struct {
	locked bool
	err    error
}

func (l *coverageLock) TryLock() (bool, error) { return l.locked, l.err }
func (*coverageLock) Unlock() error            { return nil }
func (*coverageLock) Close() error             { return nil }

func TestFilesystemOperationFailureBoundaries(t *testing.T) {
	original := filesystemOperations
	originalLock := newSkillsLock
	t.Cleanup(func() { filesystemOperations = original })
	t.Cleanup(func() { newSkillsLock = originalLock })
	failure := errors.New("path operation")
	filesystemOperations.abs = func(string) (string, error) { return "", failure }
	if _, err := ValidateTarget("x"); !errors.Is(err, failure) {
		t.Fatalf("abs=%v", err)
	}
	if _, err := lock(context.Background(), "x", time.Second); !errors.Is(err, failure) {
		t.Fatalf("lock abs=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return nil, failure }
	if _, err := canonicalSystemAlias("/tmp/x"); !errors.Is(err, failure) {
		t.Fatalf("alias lstat=%v", err)
	}
	if err := validateExistingAncestry("/Users/x"); !errors.Is(err, failure) {
		t.Fatalf("ancestry lstat=%v", err)
	}
	if _, err := lock(context.Background(), "/tmp/x", time.Second); !errors.Is(err, failure) {
		t.Fatalf("lock lstat=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return coverageInfo{mode: 0o755}, nil }
	if got, err := canonicalSystemAlias("/tmp/x"); err != nil || got != "/tmp/x" {
		t.Fatalf("non-link=%q err=%v", got, err)
	}
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return coverageInfo{mode: os.ModeSymlink}, nil }
	filesystemOperations.eval = func(string) (string, error) { return "", failure }
	if _, err := canonicalSystemAlias("/tmp/x"); !errors.Is(err, failure) {
		t.Fatalf("eval=%v", err)
	}
	filesystemOperations.eval = func(string) (string, error) { return "/wrong", nil }
	if _, err := canonicalSystemAlias("/tmp/x"); err == nil {
		t.Fatal("wrong alias accepted")
	}
	filesystemOperations = original
	filesystemOperations.mkdir = func(string, fs.FileMode) error { return failure }
	if _, err := lock(context.Background(), filepath.Join(t.TempDir(), "missing", "target"), time.Second); !errors.Is(err, failure) {
		t.Fatalf("mkdir=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.sync = func(string) error { return failure }
	if _, err := lock(context.Background(), filepath.Join(t.TempDir(), "missing", "target"), time.Second); !errors.Is(err, failure) {
		t.Fatalf("sync=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.abs = func(string) (string, error) { return "/Users/x", nil }
	filesystemOperations.lstat = func(name string) (fs.FileInfo, error) {
		if strings.HasSuffix(name, ".cli-helpers-skills-lock") {
			return nil, failure
		}
		return coverageInfo{mode: fs.ModeDir}, nil
	}
	if _, err := lock(context.Background(), "x", time.Second); !errors.Is(err, failure) {
		t.Fatalf("lock-file=%v", err)
	}
	newSkillsLock = func(string) skillsLock { return &coverageLock{err: failure} }
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return coverageInfo{mode: fs.ModeDir}, nil }
	if _, err := lock(context.Background(), "x", time.Second); !errors.Is(err, failure) {
		t.Fatalf("try-lock=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	if _, err := ensureDirectoryAncestry(string(filepath.Separator), os.Mkdir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("root ancestry=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.abs = func(string) (string, error) { return "/tmp/x", nil }
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return nil, failure }
	if _, err := ValidateTarget("x"); !errors.Is(err, failure) {
		t.Fatalf("target alias=%v", err)
	}
	filesystemOperations = original
	filesystemOperations.abs = func(string) (string, error) { return "/Users/x", nil }
	filesystemOperations.lstat = func(string) (fs.FileInfo, error) { return coverageInfo{mode: os.ModeSymlink}, nil }
	if _, err := lock(context.Background(), "x", time.Second); err == nil {
		t.Fatal("lock symlink ancestry accepted")
	}
	if err := validateExistingAncestry(string(filepath.Separator)); err != nil {
		t.Fatalf("root ancestry=%v", err)
	}
	filesystemOperations.lstat = func(name string) (fs.FileInfo, error) {
		if name == "/Users/file" {
			return coverageInfo{mode: 0o644}, nil
		}
		return coverageInfo{mode: fs.ModeDir}, nil
	}
	if err := validateExistingAncestry("/Users/file/child"); err == nil {
		t.Fatal("file ancestor accepted")
	}
}

func TestFilesystemAncestryAndTargetConfinement(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "one", "two")
	created, err := ensureDirectoryAncestry(target, os.Mkdir)
	if err != nil || strings.Join(created, ",") != filepath.Join(base, "one", "two")+","+filepath.Join(base, "one") {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if again, err := ensureDirectoryAncestry(target, os.Mkdir); err != nil || len(again) != 0 {
		t.Fatalf("existing created=%v err=%v", again, err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDirectoryAncestry(filepath.Join(file, "child"), os.Mkdir); err == nil {
		t.Fatal("file ancestor accepted")
	}
	mkdirErr := errors.New("mkdir")
	if _, err := ensureDirectoryAncestry(filepath.Join(base, "failed"), func(string, fs.FileMode) error { return mkdirErr }); !errors.Is(err, mkdirErr) {
		t.Fatalf("mkdir error=%v", err)
	}
	if err := syncCreatedDirectoryAncestry([]string{filepath.Join(base, "one"), filepath.Join(base, "one"), filepath.Join(base, "one", "two")}, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("sync")
	if err := syncCreatedDirectoryAncestry([]string{filepath.Join(base, "one")}, func(string) error { return syncErr }); !errors.Is(err, syncErr) {
		t.Fatalf("sync error=%v", err)
	}

	wantTarget, err := canonicalSystemAlias(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateTarget(target); err != nil || got != wantTarget {
		t.Fatalf("target=%q err=%v", got, err)
	}
	if _, err := ValidateTarget(file); err == nil {
		t.Fatal("file target accepted")
	}
	if err := os.Symlink(filepath.Join(base, "one"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTarget(filepath.Join(base, "link", "child")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	if err := validateExistingAncestry(filepath.Join("/Users", "shared", "missing", "child")); err != nil {
		t.Fatalf("missing ancestry rejected: %v", err)
	}
}

func TestSystemAliasesAndRootConfinement(t *testing.T) {
	for _, alias := range []string{"/tmp", "/var"} {
		got, err := canonicalSystemAlias(filepath.Join(alias, "cli-helpers-skills-test"))
		if err != nil {
			t.Fatalf("%s: %v", alias, err)
		}
		want := "/private" + alias + "/cli-helpers-skills-test"
		if got != want {
			t.Fatalf("%s alias=%q want %q", alias, got, want)
		}
	}
	plain := filepath.Join("/Users", "shared", "plain")
	if got, err := canonicalSystemAlias(plain); err != nil || got != plain {
		t.Fatalf("plain=%q err=%v", got, err)
	}
	root := t.TempDir()
	if _, err := rootRelative(root, root); err == nil {
		t.Fatal("transaction root accepted as child")
	}
	if _, err := rootRelative(root, filepath.Join(filepath.Dir(root), "outside")); err == nil {
		t.Fatal("outside path accepted")
	}
	old := filepath.Join(root, "old")
	new := filepath.Join(root, "new")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rootedRename(root, old, new); err != nil {
		t.Fatal(err)
	}
	if err := rootedRemoveAll(root, new); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(new); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rooted removal=%v", err)
	}
}

func TestLockSerializesAndRespectsCancellation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	unlock, err := lock(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lock(ctx, dir, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock error=%v", err)
	}
	if _, err := lock(context.Background(), dir, 1*time.Millisecond); err == nil {
		t.Fatal("contended lock did not time out")
	}
	lockPath := filepath.Join(filepath.Dir(dir), ".cli-helpers-skills-lock")
	unlock()
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := lock(context.Background(), dir, time.Second); err == nil {
		t.Fatal("symlinked lock accepted")
	}
}
