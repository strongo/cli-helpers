//go:build !windows

package daemonlifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func runPlatformProbe(mode, _ string) error { return fmt.Errorf("unknown probe mode %q", mode) }

// assertDetachedSession checks the observable effect of Setsid: the child
// leads a new process group, so a signal to the caller's group misses it.
func assertDetachedSession(t *testing.T, pid int) {
	t.Helper()
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if ours, _ := syscall.Getpgid(0); group != pid || group == ours {
		t.Fatalf("detached child group = %d (caller %d), want its own pid %d", group, ours, pid)
	}
}

// assertNoInheritedPipe lists the child's descriptors on Linux. Elsewhere the
// EOF the reader observes while the child runs is the evidence.
func assertNoInheritedPipe(t *testing.T, pid int, pipe *os.File) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	pipeName, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", pipe.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	fdDir := filepath.Join("/proc", fmt.Sprint(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err == nil && target == pipeName {
			t.Fatalf("detached child fd %s holds the caller's pipe %s", entry.Name(), pipeName)
		}
	}
}
