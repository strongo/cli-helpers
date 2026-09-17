package daemonlifecycle

import (
	"errors"
	"fmt"
	"time"
)

// ErrProcessNotFound reports that no running process has the requested pid.
// Exited processes that have not been reaped yet (zombies) count as not found.
var ErrProcessNotFound = errors.New("process not found")

// ErrProcessMismatch reports that the pid now belongs to a process with a
// different start time than the one recorded, typically after pid reuse.
var ErrProcessMismatch = errors.New("process start time does not match")

// ProcessStartTime returns when the running process pid started, so a pid
// recorded together with its start time can later be checked against the
// process that holds the pid now. Compare values with time.Time.Equal: the
// result is stable for the life of a process (Linux reports 10 ms ticks
// relative to boot, macOS microseconds, Windows 100 ns intervals).
//
// It returns an error wrapping ErrProcessNotFound when no running process has
// that pid. It is implemented for Linux, macOS and Windows without cgo; other
// platforms return an error.
func ProcessStartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("process start time: invalid pid %d", pid)
	}
	return processStartTime(pid)
}

// TerminateIfSameProcess forcibly terminates pid only when its current start
// time equals startedAt. Otherwise it returns an error wrapping
// ErrProcessMismatch (or ErrProcessNotFound) and sends nothing.
//
// On Windows the check and the termination use one process handle, so the pid
// cannot be reused in between. On Unix the check is immediately followed by
// SIGKILL; a pid reused within that instant is not distinguishable.
func TerminateIfSameProcess(pid int, startedAt time.Time) error {
	if pid <= 0 {
		return fmt.Errorf("terminate process: invalid pid %d", pid)
	}
	return terminateIfSameProcess(pid, startedAt)
}

func mismatch(pid int, want, got time.Time) error {
	return fmt.Errorf("terminate process %d: started %s, recorded %s: %w",
		pid, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano), ErrProcessMismatch)
}
