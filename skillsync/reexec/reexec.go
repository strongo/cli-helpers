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
	info, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("stat skills refresh executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("skills refresh executable is not a regular file: %s", executable)
	}
	args := r.Args
	if len(args) == 0 {
		args = []string{"skills", "sync"}
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
			return fmt.Errorf("skills refresh: %w", runCtx.Err())
		}
		return fmt.Errorf("skills refresh: %w", err)
	}
	return nil
}
