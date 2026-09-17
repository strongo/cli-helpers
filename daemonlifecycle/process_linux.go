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
)

// procRoot is the procfs mount; tests point it at a fixture tree.
var procRoot = "/proc"

func processIdentity(pid int) (string, error) {
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("process %d: %w", pid, ErrProcessNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("process %d identity: %w", pid, err)
	}
	// The command name (field 2) is parenthesised and may itself contain
	// spaces or parentheses, so fields are counted after its last ")".
	contents := string(stat)
	fields := strings.Fields(contents[strings.LastIndex(contents, ")")+1:])
	// fields[0] is the state (field 3); starttime is field 22.
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", fmt.Errorf("process %d identity: malformed stat", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", fmt.Errorf("process %d has exited: %w", pid, ErrProcessNotFound)
	}
	// starttime is clock ticks since boot, fixed when the process starts. It
	// is used raw: converting it through btime would follow wall-clock steps.
	ticks, err := strconv.ParseUint(fields[startTimeIndex], 10, 64)
	if err != nil {
		return "", fmt.Errorf("process %d identity: %w", pid, err)
	}
	bootID, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", fmt.Errorf("process %d identity: %w", pid, err)
	}
	boot := strings.TrimSpace(string(bootID))
	if boot == "" {
		return "", fmt.Errorf("process %d identity: empty boot_id", pid)
	}
	return "linux:" + boot + ":" + strconv.FormatUint(ticks, 10), nil
}
