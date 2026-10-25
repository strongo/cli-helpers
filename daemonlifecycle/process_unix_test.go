//go:build linux || darwin

package daemonlifecycle

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestTerminateIfSameProcessKillError(t *testing.T) {
	selfPID := os.Getpid()
	identity, err := ProcessIdentity(selfPID)
	if err != nil {
		t.Fatalf("ProcessIdentity(self) = %v", err)
	}

	prev := killProcess
	killProcess = func(pid int, sig syscall.Signal) error {
		return errors.New("simulated kill error")
	}
	t.Cleanup(func() { killProcess = prev })

	err = TerminateIfSameProcess(selfPID, identity)
	if err == nil || !strings.Contains(err.Error(), "simulated kill error") {
		t.Fatalf("TerminateIfSameProcess err = %v, want simulated kill error", err)
	}
}
