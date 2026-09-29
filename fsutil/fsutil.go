package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// Seams for the operating-system calls, replaced in tests to reach error paths.
var (
	userHomeDir = os.UserHomeDir
	stat        = os.Stat
)

// DirExists reports whether path exists and is a directory. A missing path is
// (false, nil); any other stat failure is returned as the error.
func DirExists(path string) (bool, error) {
	info, err := stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// ExpandHome expands a leading "~" or "~/" in p to the user's home directory.
// Any other path, or a failure to determine the home directory, returns p
// unchanged.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := userHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return strings.TrimSuffix(filepath.Join(home, strings.TrimPrefix(p, "~/")), "/")
}
