//go:build windows

package daemonlifecycle

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

const queryAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE

func processIdentity(pid int) (string, error) {
	handle, err := openProcess(pid, queryAccess)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return handleIdentity(pid, handle)
}

func terminateIfSameProcess(pid int, identity string) error {
	handle, err := openProcess(pid, queryAccess|windows.PROCESS_TERMINATE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	current, err := handleIdentity(pid, handle)
	if err != nil {
		return err
	}
	if current != identity {
		return mismatch(pid, identity, current)
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

func handleIdentity(pid int, handle windows.Handle) (string, error) {
	// A process handle is signalled once the process exits. Unlike
	// GetExitCodeProcess, this cannot confuse an exit code of 259 with
	// STILL_ACTIVE.
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", fmt.Errorf("process %d state: %w", pid, err)
	}
	if event != uint32(windows.WAIT_TIMEOUT) {
		return "", fmt.Errorf("process %d has exited: %w", pid, ErrProcessNotFound)
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", fmt.Errorf("process %d identity: %w", pid, err)
	}
	return fmt.Sprintf("windows:%d", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime)), nil
}
