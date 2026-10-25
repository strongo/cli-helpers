//go:build !windows

package skillsync

import "os"

// syncDirectory preserves the POSIX implementation: directory fsync errors
// are transactional failures, while Close is best-effort after the sync.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
