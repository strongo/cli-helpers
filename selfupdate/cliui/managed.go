package cliui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/strongo/cli-helpers/selfupdate"
)

var (
	managedLookPath     = exec.LookPath
	managedEvalSymlinks = filepath.EvalSymlinks
	managedAbsPath      = filepath.Abs
	managedGetenv       = os.Getenv
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

// VerifyManagedBinary inspects every PATH candidate after a successful manager
// command, ignores candidates that are not owned by the detected manager, and
// returns the one exact executable identity that passed the configured version
// probe. This avoids selecting a development binary that happens to precede a
// Homebrew, Scoop, or WinGet launcher on PATH. The returned identity is reused
// by AfterUpdate, so verification and reexecution cannot disagree.
func VerifyManagedBinary(ctx context.Context, detection selfupdate.Detection, binary string, args []string, expectedVersion string) (selfupdate.ExecutableIdentity, error) {
	candidates := managedBinaryCandidates(binary)
	if len(candidates) == 0 {
		return selfupdate.ExecutableIdentity{}, fmt.Errorf("locate updated %s on PATH: executable not found", binary)
	}
	var failures []error
	for _, path := range candidates {
		resolved, err := managedEvalSymlinks(path)
		if err != nil {
			failures = append(failures, fmt.Errorf("resolve %s: %w", path, err))
			continue
		}
		if detection.Manager != nil && selfupdate.Classify(resolved, []selfupdate.Manager{*detection.Manager}).Method != selfupdate.Managed {
			continue
		}
		out, err := exec.CommandContext(ctx, path, args...).CombinedOutput() //nolint:gosec // paths come from PATH and must match the detected manager
		if err != nil {
			failures = append(failures, fmt.Errorf("probe updated %s at %s: %w (output: %s)", binary, path, err, strings.TrimSpace(string(out))))
			continue
		}
		if strings.TrimSpace(string(out)) == "" {
			failures = append(failures, fmt.Errorf("probe updated %s at %s returned no version output", binary, path))
			continue
		}
		if expectedVersion != "" && !strings.Contains(string(out), expectedVersion) {
			failures = append(failures, fmt.Errorf("probe updated %s at %s did not report expected version %q (got %q)", binary, path, expectedVersion, strings.TrimSpace(string(out))))
			continue
		}
		absolute, err := managedAbsPath(path)
		if err != nil {
			failures = append(failures, fmt.Errorf("make updated executable path absolute: %w", err))
			continue
		}
		return selfupdate.ExecutableIdentity{Path: absolute, ResolvedPath: resolved}, nil
	}
	manager := "configured package manager"
	if detection.Manager != nil {
		manager = detection.Manager.Name
	}
	if len(failures) == 0 {
		return selfupdate.ExecutableIdentity{}, fmt.Errorf("locate updated %s owned by %s on PATH: no matching executable", binary, manager)
	}
	return selfupdate.ExecutableIdentity{}, fmt.Errorf("verify updated %s owned by %s: %w", binary, manager, errors.Join(failures...))
}

func managedBinaryCandidates(binary string) []string {
	if filepath.IsAbs(binary) || strings.ContainsAny(binary, `/\\`) {
		if path, err := managedLookPath(binary); err == nil {
			return []string{path}
		}
		return nil
	}
	seen := make(map[string]bool)
	var candidates []string
	for _, directory := range filepath.SplitList(managedGetenv("PATH")) {
		if directory == "" {
			directory = "."
		}
		path, err := managedLookPath(filepath.Join(directory, binary))
		if err == nil && !seen[path] {
			seen[path] = true
			candidates = append(candidates, path)
		}
	}
	return candidates
}
