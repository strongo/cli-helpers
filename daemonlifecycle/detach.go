package daemonlifecycle

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// StartDetached starts the program described by template as a process that
// outlives its caller and never holds the caller's standard streams.
//
// Only template's Path, Args, Env and Dir are used; template itself is never
// started, so its Stdin, Stdout, Stderr, ExtraFiles, SysProcAttr, Cancel and
// WaitDelay must be unset: a detached child does not follow a context. The
// child reads from the null device and writes both output streams to log, which the caller owns and may close once StartDetached returns. No
// other handle is inherited: on Unix every other descriptor is close-on-exec,
// and on Windows only the three standard handles are listed for inheritance.
// A caller whose own stdout is a pipe therefore sees EOF as soon as it exits,
// even while the child keeps running.
//
// The child is configured with ConfigureDetached. On Windows, when the
// caller's job object forbids CREATE_BREAKAWAY_FROM_JOB and process creation
// fails with ERROR_ACCESS_DENIED, StartDetached retries once without the
// breakaway flag; that child stays in the job and dies with it, which the
// caller detects as a readiness failure.
//
// The returned process is the caller's to Wait on (to observe an early exit)
// or Release. Readiness, timeouts and stopping remain the caller's policy.
func StartDetached(template *exec.Cmd, log *os.File) (*os.Process, error) {
	if err := validateDetachedTemplate(template, log); err != nil {
		return nil, err
	}
	return startDetached(template, log, detachAttempts, retryDetachedStart)
}

func validateDetachedTemplate(template *exec.Cmd, log *os.File) error {
	switch {
	case template == nil:
		return errors.New("start detached: nil command")
	case log == nil:
		return errors.New("start detached: nil log file")
	case template.Err != nil:
		return fmt.Errorf("start detached: %w", template.Err)
	case template.Process != nil:
		return errors.New("start detached: command already started")
	case template.Stdin != nil || template.Stdout != nil || template.Stderr != nil ||
		len(template.ExtraFiles) != 0:
		return errors.New("start detached: command must not set Stdin, Stdout, Stderr or ExtraFiles")
	case template.Cancel != nil || template.WaitDelay != 0:
		return errors.New("start detached: command must not use CommandContext, Cancel or WaitDelay")
	case template.SysProcAttr != nil:
		return errors.New("start detached: command must not set SysProcAttr")
	}
	return nil
}

// startDetached tries each configuration in turn, moving to the next one only
// while retry accepts the previous failure. An exec.Cmd cannot be started
// twice, so every attempt starts a fresh copy of template.
func startDetached(
	template *exec.Cmd, log *os.File, attempts []func(*exec.Cmd), retry func(error) bool,
) (*os.Process, error) {
	var err error
	for _, configure := range attempts {
		command := &exec.Cmd{
			Path:   template.Path,
			Args:   template.Args,
			Env:    template.Env,
			Dir:    template.Dir,
			Stdout: log,
			Stderr: log,
		}
		configure(command)
		if err = command.Start(); err == nil {
			return command.Process, nil
		}
		if !retry(err) {
			break
		}
	}
	return nil, fmt.Errorf("start detached %s: %w", template.Path, err)
}
