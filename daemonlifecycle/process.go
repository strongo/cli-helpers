package daemonlifecycle

import (
	"errors"
	"fmt"
)

// ErrProcessNotFound reports that no running process has the requested pid.
// Exited processes that have not been reaped yet (zombies) count as not found.
var ErrProcessNotFound = errors.New("process not found")

// ErrProcessMismatch reports that the pid now belongs to a different process
// than the one whose identity was recorded, typically after pid reuse.
var ErrProcessMismatch = errors.New("process identity does not match")

// ProcessIdentity returns an opaque token that names the running process pid
// for its whole life, so a pid recorded together with its identity can later
// be checked against the process that holds the pid now. Two calls for the
// same process return equal strings; a process that later reuses the pid gets
// a different one. Compare tokens only for equality and do not parse them.
//
// The token is built from values the kernel records once and never
// recomputes, so wall-clock steps do not change it: on Linux the boot id plus
// the start time in clock ticks since boot, on macOS the kernel's start
// timeval, and on Windows the creation FILETIME.
//
// It returns an error wrapping ErrProcessNotFound when no running process has
// that pid. It is implemented for Linux, macOS and Windows without cgo; other
// platforms return an error.
func ProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("process identity: invalid pid %d", pid)
	}
	return processIdentity(pid)
}

// TerminateIfSameProcess forcibly terminates pid only when its current
// identity equals identity, as returned earlier by ProcessIdentity. Otherwise
// it returns an error wrapping ErrProcessMismatch (or ErrProcessNotFound) and
// sends nothing.
//
// On Windows the check and the termination use one process handle, so the pid
// cannot be reused in between. On Unix the check is immediately followed by
// SIGKILL; a pid reused within that instant is not distinguishable.
func TerminateIfSameProcess(pid int, identity string) error {
	if pid <= 0 {
		return fmt.Errorf("terminate process: invalid pid %d", pid)
	}
	return terminateIfSameProcess(pid, identity)
}
