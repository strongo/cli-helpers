//go:build !windows

package daemonlifecycle

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestValidateOwnerOnlyFileRejectsUnsafeExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnly(path); err == nil {
		t.Fatal("ValidateOwnerOnly accepted group/world-readable state")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := ValidateOwnerOnlyFile(file); err == nil {
		t.Fatal("ValidateOwnerOnlyFile accepted group/world-readable state")
	}
	if err := ProtectOwnerOnlyFile(file); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnlyFile(file); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerOnlyValidationErrorPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := ProtectOwnerOnly(missing); err == nil {
		t.Fatal("ProtectOwnerOnly accepted a missing path")
	}
	if err := ValidateOwnerOnly(missing); err == nil {
		t.Fatal("ValidateOwnerOnly accepted a missing path")
	}
	if err := ProtectOwnerOnlyFile(nil); err == nil {
		t.Fatal("ProtectOwnerOnlyFile accepted nil")
	}
	if err := ValidateOwnerOnlyFile(nil); err == nil {
		t.Fatal("ValidateOwnerOnlyFile accepted nil")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnly(link); err == nil {
		t.Fatal("ValidateOwnerOnly accepted a symlink")
	}
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnlyFile(directory); err == nil {
		t.Fatal("ValidateOwnerOnlyFile accepted a directory handle")
	}
	_ = directory.Close()

	closed, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ProtectOwnerOnlyFile(closed); err == nil {
		t.Fatal("ProtectOwnerOnlyFile accepted a closed file")
	}
	if err := ValidateOwnerOnlyFile(closed); err == nil {
		t.Fatal("ValidateOwnerOnlyFile accepted a closed file")
	}

	if err := validateUnixOwnerOnly(fakeFileInfo{mode: 0o600, stat: &syscall.Stat_t{Uid: uint32(os.Getuid()) + 1, Nlink: 1}}); err == nil {
		t.Fatal("validateUnixOwnerOnly accepted another owner")
	}
}

type fakeFileInfo struct {
	mode os.FileMode
	stat *syscall.Stat_t
}

func (info fakeFileInfo) Name() string       { return "fake" }
func (info fakeFileInfo) Size() int64        { return 0 }
func (info fakeFileInfo) Mode() os.FileMode  { return info.mode }
func (info fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info fakeFileInfo) Sys() any           { return info.stat }

func TestValidateOwnerOnlyFileRejectsHardLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.lock")
	other := filepath.Join(dir, "other.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, other); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := ValidateOwnerOnlyFile(file); err == nil {
		t.Fatal("ValidateOwnerOnlyFile accepted a multiply linked state file")
	}
}
