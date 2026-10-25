//go:build linux || darwin

package daemonlifecycle

import (
	"fmt"
	"syscall"
)

var killProcess = syscall.Kill

func terminateIfSameProcess(pid int, identity string) error {
	current, err := ProcessIdentity(pid)
	if err != nil {
		return err
	}
	if current != identity {
		return mismatch(pid, identity, current)
	}
	if err := killProcess(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}
