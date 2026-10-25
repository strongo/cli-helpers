//go:build !windows

package daemonlifecycle

import (
	"os/exec"
	"syscall"
)

// ConfigureDetached puts command's child in its own session, so it survives
// the process that started it, has no controlling terminal, and a later
// process-group signal stays scoped to that child and its descendants.
//
// On Windows it sets DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP |
// CREATE_BREAKAWAY_FROM_JOB and hides the window. Starting such a command fails
// with ERROR_ACCESS_DENIED inside a job that forbids breakaway; StartDetached
// handles that retry.
func ConfigureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Unix has a single detached configuration and nothing to retry.
var detachAttempts = []func(*exec.Cmd){ConfigureDetached}

func retryDetachedStart(error) bool { return false }
