//go:build darwin

package daemonlifecycle

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// darwinZombie is SZOMB from <sys/proc.h>.
const darwinZombie = 5

func processIdentity(pid int) (string, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("process %d identity: %w", pid, err)
	}
	if len(procs) == 0 || procs[0].Proc.P_stat == darwinZombie {
		return "", fmt.Errorf("process %d: %w", pid, ErrProcessNotFound)
	}
	started := procs[0].Proc.P_starttime
	return fmt.Sprintf("darwin:%d.%06d", started.Sec, started.Usec), nil
}
