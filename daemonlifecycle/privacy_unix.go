//go:build !windows

package daemonlifecycle

import (
	"fmt"
	"os"
	"syscall"
)

func protectOwnerOnly(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info.IsDir() {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func protectOwnerOnlyFile(file *os.File) error { return file.Chmod(0o600) }

func validateOwnerOnly(_ string, info os.FileInfo) error {
	return validateUnixOwnerOnly(info)
}

func validateOwnerOnlyFile(_ *os.File, info os.FileInfo) error {
	return validateUnixOwnerOnly(info)
}

func validateUnixOwnerOnly(info os.FileInfo) error {
	want := os.FileMode(0o600)
	if info.IsDir() {
		want = 0o700
	}
	if info.Mode().Perm() != want {
		return fmt.Errorf("mode is %o, want %o", info.Mode().Perm(), want)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("path is not owned by the current user")
	}
	if !info.IsDir() && stat.Nlink != 1 {
		return fmt.Errorf("regular file has %d links, want one", stat.Nlink)
	}
	return nil
}
