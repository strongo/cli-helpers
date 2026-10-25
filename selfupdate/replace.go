package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Test seams: overridable indirections so the Windows rename path, the
// staging error branches, and the unsupported-platform check are
// exercisable on any host without a real permission failure or a real
// non-native platform.
var (
	goosName       = runtime.GOOS
	goarchName     = runtime.GOARCH
	renameFunc     = os.Rename
	stageCreateTmp = func(dir, pattern string) (tempFile, error) {
		return os.CreateTemp(dir, pattern)
	}
)

// replaceExecutable atomically swaps the binary at newBinaryPath into
// targetPath. The new binary is first staged to a temp file in the same
// directory as targetPath (same filesystem, so the rename is atomic), with
// executable permissions (0755), then renamed over the target.
//
// On POSIX, os.Rename within one directory atomically replaces the target —
// the install location is never left with a partial or truncated file: it
// holds either the original or the complete new binary. On Windows, a
// running .exe cannot be overwritten, so the existing target is first
// renamed aside to targetPath+".old" before the new binary is moved into
// place.
//
// Any staging error before the rename returns the error and leaves
// targetPath untouched (REQ: failure-leaves-working-binary).
func replaceExecutable(binaryName, targetPath, newBinaryPath string) error {
	dir := filepath.Dir(targetPath)

	staged, err := stage(binaryName, dir, newBinaryPath)
	if err != nil {
		return err
	}

	if goosName == "windows" {
		// A running .exe cannot be overwritten, but it can be renamed. Move
		// the current target aside, then move the new binary into place. The
		// .old copy may stay locked while the old process runs; removing it
		// is best-effort and not required for correctness.
		old := targetPath + ".old"
		_ = os.Remove(old)
		if err := renameFunc(targetPath, old); err != nil {
			_ = os.Remove(staged)
			return fmt.Errorf("move aside current executable: %w", err)
		}
		if err := renameFunc(staged, targetPath); err != nil {
			// Best effort: restore the original so the install stays
			// runnable even though this swap failed.
			_ = renameFunc(old, targetPath)
			_ = os.Remove(staged)
			return fmt.Errorf("install new executable: %w", err)
		}
		_ = os.Remove(old)
		return nil
	}

	// POSIX: a single rename atomically replaces the target.
	if err := renameFunc(staged, targetPath); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("install new executable: %w", err)
	}
	return nil
}

// stage copies src into a new temp file in dir with 0755 permissions and
// returns its path. On any error the temp file is removed so a failed
// update leaves nothing behind beside the (untouched) target.
func stage(binaryName, dir, src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open new binary: %w", err)
	}
	defer func() { _ = in.Close() }()

	tmp, err := stageCreateTmp(dir, "."+binaryName+"-stage-*")
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("stage new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("close staging file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("chmod staging file: %w", err)
	}
	return tmp.Name(), nil
}

// runVersionProbe runs path with args and returns its combined output. It
// is a plain function rather than a package-level seam because, unlike the
// filesystem/network seams above, its own tests exercise it against a real
// disposable script file (see replace_test.go) rather than needing to fake
// process execution itself — REQ: no-network-in-tests forbids network access
// and replacing a REAL installed binary, not running a throwaway temp
// script.
func runVersionProbe(path string, args []string) (string, error) {
	out, err := exec.Command(path, args...).CombinedOutput() //nolint:gosec // path/args are caller-configured, not attacker input
	return string(out), err
}

// verifyBinaryVersion runs "<path> <args...>" and returns an error unless
// the combined output contains wantVersion. It is kept separate from
// replaceExecutable so the swap and the post-swap sanity check are
// independently testable, and its error is never fatal to Update — a
// successful swap stays successful even when the confirmation probe itself
// fails (REQ: post-swap-version-check).
func verifyBinaryVersion(path string, args []string, wantVersion string) error {
	out, err := runVersionProbe(path, args)
	if err != nil {
		return fmt.Errorf("run %s %s: %w", path, strings.Join(args, " "), err)
	}
	if !strings.Contains(out, wantVersion) {
		return fmt.Errorf("version check failed: %s %s did not report %q (got %q)",
			path, strings.Join(args, " "), wantVersion, strings.TrimSpace(out))
	}
	return nil
}
