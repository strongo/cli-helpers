//go:build windows

package daemonlifecycle

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE, the exit code of a process that has not exited.
const stillActive = 259

func processStartTime(pid int) (time.Time, error) {
	handle, err := openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return handleStartTime(pid, handle)
}

func terminateIfSameProcess(pid int, startedAt time.Time) error {
	handle, err := openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	started, err := handleStartTime(pid, handle)
	if err != nil {
		return err
	}
	if !started.Equal(startedAt) {
		return mismatch(pid, startedAt, started)
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}

func openProcess(pid int, access uint32) (windows.Handle, error) {
	handle, err := windows.OpenProcess(access, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return 0, fmt.Errorf("process %d: %w", pid, ErrProcessNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("open process %d: %w", pid, err)
	}
	return handle, nil
}

func handleStartTime(pid int, handle windows.Handle) (time.Time, error) {
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return time.Time{}, fmt.Errorf("process %d exit code: %w", pid, err)
	}
	if code != stillActive {
		return time.Time{}, fmt.Errorf("process %d has exited: %w", pid, ErrProcessNotFound)
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, fmt.Errorf("process %d start time: %w", pid, err)
	}
	return time.Unix(0, creation.Nanoseconds()).UTC(), nil
}
