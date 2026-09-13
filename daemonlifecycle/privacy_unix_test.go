//go:build !windows

package daemonlifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOwnerOnlyFileRejectsUnsafeExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
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
	defer file.Close()
	if err := ValidateOwnerOnlyFile(file); err == nil {
		t.Fatal("ValidateOwnerOnlyFile accepted a multiply linked state file")
	}
}
