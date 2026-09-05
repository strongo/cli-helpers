package cliui

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/strongo/cli-helpers/selfupdate"
)

// ManagedCommandRunner returns a framework-neutral runner that passes the
// configured executable and argv directly to the operating system. It never
// parses a display command or invokes a shell. Process input and output are
// streamed through the caller-owned readers and writers.
func ManagedCommandRunner(in io.Reader, out, errOut io.Writer) selfupdate.ManagedCommandRunner {
	return func(ctx context.Context, executable string, args []string) error {
		cmd := exec.CommandContext(ctx, executable, args...) //nolint:gosec // executable and argv are consumer-configured, never parsed from user input
		cmd.Stdin = in
		cmd.Stdout = out
		cmd.Stderr = errOut
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("execute %s: %w", executable, err)
		}
		return nil
	}
}

// VerifyManagedBinary locates binary on PATH after a successful manager
// command and runs its configured version probe. When expectedVersion is
// known, the output must contain that exact release version; accepting any
// non-empty output would let stale package-manager metadata report success
// while retaining an older binary.
func VerifyManagedBinary(ctx context.Context, binary string, args []string, expectedVersion string) error {
	path, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("locate updated %s on PATH: %w", binary, err)
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput() //nolint:gosec // binary and probe args are consumer-configured
	if err != nil {
		return fmt.Errorf("probe updated %s: %w (output: %s)", binary, err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("probe updated %s returned no version output", binary)
	}
	if expectedVersion != "" && !strings.Contains(string(out), expectedVersion) {
		return fmt.Errorf("probe updated %s did not report expected version %q (got %q)", binary, expectedVersion, strings.TrimSpace(string(out)))
	}
	return nil
}
