//go:build linux

package daemonlifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// statLine builds a /proc/<pid>/stat line whose command name contains the
// separators a naive parser trips over.
func statLine(state, startTicks string) string {
	fields := []string{state}
	for i := 4; i < 22; i++ {
		fields = append(fields, "0")
	}
	return "42 (a) (b c) " + strings.Join(append(fields, startTicks, "0"), " ")
}

func TestLinuxProcessStartTimeParsesProcfs(t *testing.T) {
	root := t.TempDir()
	previous := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = previous })
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expectError := func(pid int, want string) {
		t.Helper()
		if _, err := ProcessStartTime(pid); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("ProcessStartTime(%d) = %v, want %q", pid, err, want)
		}
	}

	expectError(7, "not found")
	if err := os.MkdirAll(filepath.Join(root, "8", "stat"), 0o700); err != nil {
		t.Fatal(err)
	}
	expectError(8, "is a directory")
	write("9/stat", "9 (short) S 1 2")
	expectError(9, "malformed")
	write("10/stat", statLine("Z", "5"))
	if _, err := ProcessStartTime(10); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("zombie = %v, want ErrProcessNotFound", err)
	}
	write("11/stat", statLine("S", "soon"))
	expectError(11, "invalid syntax")
	write("12/stat", statLine("S", "250"))
	expectError(12, "stat")
	write("stat", "cpu 1 2 3\nbtime later\n")
	expectError(12, "parse btime")
	write("stat", "cpu 1 2 3\n")
	expectError(12, "btime missing")
	write("stat", "cpu 1 2 3\nbtime 1700000000\nprocesses 9\n")
	started, err := ProcessStartTime(12)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1700000002, int64(500*time.Millisecond)); !started.Equal(want) {
		t.Fatalf("start = %v, want %v", started, want)
	}

	// A matching start time on a pid no process holds reaches kill(2), whose
	// failure is reported rather than treated as success.
	write("2147483000/stat", statLine("S", "250"))
	if err := TerminateIfSameProcess(2147483000, started); err == nil || !strings.Contains(err.Error(), "terminate process") {
		t.Fatalf("kill of an absent pid = %v", err)
	}
}
