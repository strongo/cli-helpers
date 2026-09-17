//go:build linux

package daemonlifecycle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procRoot is the procfs mount; tests point it at a fixture tree.
var procRoot = "/proc"

// procClockTicksPerSecond is USER_HZ, the unit of /proc start times. It is 100
// on every Linux architecture independently of the kernel's own HZ.
const procClockTicksPerSecond = 100

func processStartTime(pid int) (time.Time, error) {
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, fmt.Errorf("process %d: %w", pid, ErrProcessNotFound)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d start time: %w", pid, err)
	}
	// The command name (field 2) is parenthesised and may itself contain
	// spaces or parentheses, so fields are counted after its last ")".
	contents := string(stat)
	fields := strings.Fields(contents[strings.LastIndex(contents, ")")+1:])
	// fields[0] is the state (field 3); starttime is field 22.
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return time.Time{}, fmt.Errorf("process %d start time: malformed stat", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return time.Time{}, fmt.Errorf("process %d has exited: %w", pid, ErrProcessNotFound)
	}
	ticks, err := strconv.ParseUint(fields[startTimeIndex], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d start time: %w", pid, err)
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, fmt.Errorf("process %d start time: %w", pid, err)
	}
	return boot.Add(time.Duration(ticks) * (time.Second / procClockTicksPerSecond)), nil
}

func bootTime() (time.Time, error) {
	system, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(system), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("parse btime: %w", err)
			}
			return time.Unix(seconds, 0).UTC(), nil
		}
	}
	return time.Time{}, errors.New("btime missing from stat")
}
