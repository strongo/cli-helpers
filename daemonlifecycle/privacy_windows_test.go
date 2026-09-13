//go:build windows

package daemonlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// specscore:verifies https://specscore.org/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle#ac:protected-lock-journey
func TestProtectOwnerOnlyFileRejectsReplacedDirectoryEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifecycle.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := os.Rename(path, filepath.Join(dir, "original.lock")); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ProtectOwnerOnlyFile(file); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnlyFile(file); err == nil || !strings.Contains(err.Error(), "no longer names") {
		t.Fatalf("replacement validation error = %v", err)
	}
}

// specscore:verifies https://specscore.org/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle#ac:protected-lock-journey
func TestValidateSecurityDescriptorRejectsForeignOwnerAndMissingInheritance(t *testing.T) {
	current, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := ownerOnlyACL(current, windows.NO_INHERITANCE)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		t.Fatal(err)
	}
	foreign, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Equals(current) {
		foreign, err = windows.StringToSid("S-1-5-32-544")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := descriptor.SetOwner(foreign, false); err != nil {
		t.Fatal(err)
	}
	if err := validateSecurityDescriptor(descriptor, false); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("foreign-owner validation error = %v", err)
	}
	if err := descriptor.SetOwner(current, false); err != nil {
		t.Fatal(err)
	}
	if err := validateSecurityDescriptor(descriptor, true); err == nil || !strings.Contains(err.Error(), "inheritable") {
		t.Fatalf("directory-inheritance validation error = %v", err)
	}
}
