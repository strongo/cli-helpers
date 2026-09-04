// Package reexec runs the newly installed CLI for a post-update skills sync.
// It deliberately never invokes a shell, and routes its child output to
// stderr so the caller's existing JSON stdout remains a single valid value.
package reexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Runner struct {
	Args    []string
	Timeout time.Duration
	Stderr  io.Writer
}

func (r Runner) Run(ctx context.Context, executable string) error {
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("skills refresh executable must be absolute: %q", executable)
	}
	args := r.Args
	if len(args) == 0 {
		args = []string{"skills", "sync"}
	}
	info, err := os.Stat(executable)
	if err != nil {
		return refreshError(executable, args, fmt.Errorf("stat executable: %w", err))
	}
	if !info.Mode().IsRegular() {
		return refreshError(executable, args, fmt.Errorf("executable is not a regular file"))
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, args...)
	cmd.Stdout = r.Stderr
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return refreshError(executable, args, runCtx.Err())
		}
		return refreshError(executable, args, err)
	}
	return nil
}

func refreshError(executable string, args []string, cause error) error {
	return fmt.Errorf(
		"skills refresh failed for executable %q with arguments %q; retry with direct execution using the same executable and arguments: %w",
		executable,
		args,
		cause,
	)
}
