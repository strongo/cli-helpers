package daemonlifecycle

import (
	"fmt"
	"os"
)

// ProtectOwnerOnly replaces path's access policy with one granting access only
// to the current user. Directories keep inheritable permissions for children.
func ProtectOwnerOnly(path string) error {
	if err := protectOwnerOnly(path); err != nil {
		return fmt.Errorf("protect %s for current user: %w", path, err)
	}
	return nil
}

// ProtectOwnerOnlyFile replaces an already-open file's access policy with one
// granting access only to the current user. Consumers must subsequently call
// ValidateOwnerOnlyFile before trusting the handle. On Windows, applying the
// policy uses the file name because ordinary os.OpenFile handles do not carry
// WRITE_DAC; the handle-based validation detects any path replacement.
func ProtectOwnerOnlyFile(file *os.File) error {
	if file == nil {
		return fmt.Errorf("protect current-user access: nil file")
	}
	if err := protectOwnerOnlyFile(file); err != nil {
		return fmt.Errorf("protect %s for current user: %w", file.Name(), err)
	}
	return nil
}

// ValidateOwnerOnly verifies that path is a regular file or directory whose
// effective access policy grants access only to the current user.
func ValidateOwnerOnly(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return fmt.Errorf("path is not a regular file or directory: %s", path)
	}
	if err := validateOwnerOnly(path, info); err != nil {
		return fmt.Errorf("validate current-user access for %s: %w", path, err)
	}
	return nil
}

// ValidateOwnerOnlyFile verifies the effective policy of an already-open
// regular file. Consumers that make security decisions after opening a lock or
// state file should prefer this handle-based form.
func ValidateOwnerOnlyFile(file *os.File) error {
	if file == nil {
		return fmt.Errorf("validate current-user access: nil file")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("open handle is not a regular file: %s", file.Name())
	}
	if err := validateOwnerOnlyFile(file, info); err != nil {
		return fmt.Errorf("validate current-user access for %s: %w", file.Name(), err)
	}
	return nil
}
