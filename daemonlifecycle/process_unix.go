//go:build !windows

package daemonlifecycle

import (
	"fmt"
	"syscall"
	"time"
)

func terminateIfSameProcess(pid int, startedAt time.Time) error {
	started, err := ProcessStartTime(pid)
	if err != nil {
		return err
	}
	if !started.Equal(startedAt) {
		return mismatch(pid, startedAt, started)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}
