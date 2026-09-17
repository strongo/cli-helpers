//go:build windows

package daemonlifecycle

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const detachedFlags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP

// ConfigureDetached starts command's child with no console, in its own process
// group and outside the caller's job object, so it survives the process that
// started it. See the Unix documentation for the cross-platform contract.
func ConfigureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedFlags | windows.CREATE_BREAKAWAY_FROM_JOB,
		HideWindow:    true,
	}
}

// configureDetachedInJob is the fallback for a job that forbids breakaway.
func configureDetachedInJob(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedFlags, HideWindow: true}
}

var detachAttempts = []func(*exec.Cmd){ConfigureDetached, configureDetachedInJob}

func retryDetachedStart(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) }
