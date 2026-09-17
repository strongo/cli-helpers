//go:build !linux && !darwin && !windows

package daemonlifecycle

import (
	"fmt"
	"runtime"
	"time"
)

func processStartTime(pid int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("process %d start time: unsupported on %s", pid, runtime.GOOS)
}
