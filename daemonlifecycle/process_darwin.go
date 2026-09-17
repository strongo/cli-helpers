//go:build darwin

package daemonlifecycle

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// darwinZombie is SZOMB from <sys/proc.h>.
const darwinZombie = 5

func processStartTime(pid int) (time.Time, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d start time: %w", pid, err)
	}
	if len(procs) == 0 || procs[0].Proc.P_stat == darwinZombie {
		return time.Time{}, fmt.Errorf("process %d: %w", pid, ErrProcessNotFound)
	}
	started := procs[0].Proc.P_starttime
	return time.Unix(started.Sec, int64(started.Usec)*int64(time.Microsecond)).UTC(), nil
}
