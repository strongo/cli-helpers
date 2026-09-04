//go:build windows

package skillsync

import (
	"errors"
	"fmt"
	"syscall"
)

// directorySyncHandle keeps the Windows open, flush, and close boundaries
// independently testable. Every boundary remains an error: a successful
// rename without a durable parent-directory update is not a completed write.
type directorySyncHandle interface {
	Sync() error
	Close() error
}

var openDirectoryForSync = openWindowsDirectoryForSync

// syncDirectory flushes the directory entry mutations that follow an atomic
// file rename. os.Open supplies GENERIC_READ on Windows, but
// FlushFileBuffers requires GENERIC_WRITE, so this opens the directory with
// the requested access and FILE_FLAG_BACKUP_SEMANTICS instead.
func syncDirectory(path string) error {
	dir, err := openDirectoryForSync(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func openWindowsDirectoryForSync(path string) (directorySyncHandle, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode directory path for sync: %w", err)
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open directory for sync: %w", err)
	}
	return windowsDirectorySyncHandle{handle: handle}, nil
}

type windowsDirectorySyncHandle struct {
	handle syscall.Handle
}

func (h windowsDirectorySyncHandle) Sync() error {
	if err := syscall.FlushFileBuffers(h.handle); err != nil {
		return fmt.Errorf("flush directory buffers: %w", err)
	}
	return nil
}

func (h windowsDirectorySyncHandle) Close() error {
	if err := syscall.CloseHandle(h.handle); err != nil {
		return fmt.Errorf("close directory sync handle: %w", err)
	}
	return nil
}
