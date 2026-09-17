//go:build !linux && !darwin && !windows

package daemonlifecycle

import (
	"fmt"
	"runtime"
)

func processIdentity(pid int) (string, error) {
	return "", fmt.Errorf("process %d identity: unsupported on %s", pid, runtime.GOOS)
}

func terminateIfSameProcess(pid int, _ string) error {
	_, err := processIdentity(pid)
	return err
}
