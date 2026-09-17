//go:build windows

package daemonlifecycle

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func runPlatformProbe(mode, dir string) error {
	if mode != "jailed-start" {
		return fmt.Errorf("unknown probe mode %q", mode)
	}
	// Join a fresh job that does not allow breakaway, as a sandbox would, then
	// start detached. The first attempt must be refused and the retry succeed.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return err
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		return err
	}
	log, err := os.CreateTemp(dir, "jailed-*.log")
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	command := probeCommand("sleep", dir)
	ConfigureDetached(command)
	command.Stdout = log
	breakawayErr := command.Start()
	if breakawayErr == nil {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	process, err := StartDetached(probeCommand("sleep", dir), log)
	if err != nil {
		return err
	}
	defer func() {
		_ = process.Kill()
		_, _ = process.Wait()
	}()
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var inJob int32
	if ok, _, err := isProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&inJob))); ok == 0 {
		return fmt.Errorf("IsProcessInJob: %w", err)
	}
	fmt.Printf("breakaway-denied=%t in-job=%t\n",
		errors.Is(breakawayErr, windows.ERROR_ACCESS_DENIED), inJob != 0)
	return nil
}

func assertDetachedSession(*testing.T, int) {}

// On Windows the EOF the reader observes while the child runs is the evidence
// that no pipe handle was inherited.
func assertNoInheritedPipe(*testing.T, int, *os.File) {}

// specscore:verifies https://specscore.org/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle#ac:detached-start-journey
func TestStartDetachedRetriesWithoutBreakawayInsideAJob(t *testing.T) {
	output, err := probeCommand("jailed-start", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("jailed start: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "breakaway-denied=true in-job=true" {
		t.Fatalf("jailed start = %q, want a refused breakaway and a child kept in the job", got)
	}
}
