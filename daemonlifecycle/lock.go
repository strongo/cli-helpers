package daemonlifecycle

import (
	"context"
	"os"
	"time"
)

// TryLock obtains an exclusive non-blocking advisory lock on file.
func TryLock(file *os.File) (bool, error) { return tryLock(file) }

// Lock waits for an exclusive advisory lock, polling at interval until ctx is
// cancelled. A non-positive interval uses 25 milliseconds.
func Lock(ctx context.Context, file *os.File, interval time.Duration) error {
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	for {
		locked, err := TryLock(file)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Unlock releases a lock obtained with TryLock or Lock.
func Unlock(file *os.File) error { return unlock(file) }
