//go:build darwin

package daemonlifecycle

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinProcessIdentitySysctlError(t *testing.T) {
	prev := sysctlKinfoProcSlice
	sysctlKinfoProcSlice = func(name string, args ...int) ([]unix.KinfoProc, error) {
		return nil, errors.New("simulated sysctl error")
	}
	t.Cleanup(func() { sysctlKinfoProcSlice = prev })

	_, err := ProcessIdentity(1234)
	if err == nil || !strings.Contains(err.Error(), "simulated sysctl error") {
		t.Fatalf("ProcessIdentity err = %v, want simulated sysctl error", err)
	}
}
