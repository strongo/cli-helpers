//go:build linux || darwin || windows

package daemonlifecycle

import "fmt"

func mismatch(pid int, want, got string) error {
	return fmt.Errorf("terminate process %d: identity %q, recorded %q: %w", pid, got, want, ErrProcessMismatch)
}
