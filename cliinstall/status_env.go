package cliinstall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Env carries every side-effecting dependency Probe uses: PATH and process
// execution, filesystem access, and symlink resolution
// (cli-install#req:no-network-in-tests: "PATH and environment, executable
// probing... MUST be injectable"). A caller that wants Probe to see the
// real host passes DefaultEnv(); a test passes a fake, purpose-built Env
// and never touches a real installed binary or a real PATH.
//
// Every field is required — DefaultEnv sets all of them, and Probe calls
// them unconditionally, so an Env built by hand must do the same or the
// missing field's nil func value panics on first use, the same as calling
// any other nil func.
type Env struct {
	// PathDirs returns the current PATH's directories, in order. It need
	// not exclude relative entries itself — Probe filters those
	// defensively — but DefaultEnv's implementation does anyway, since a
	// relative entry is never meaningful to report as a directory that
	// was searched.
	PathDirs func() []string
	// HostDir returns the running host CLI's own executable directory
	// (cli-install#req:status-locate). Returning a non-nil error is
	// treated as "no host directory to search", not a fatal Probe error.
	HostDir func() (string, error)
	// IsExecutable reports whether path names an executable regular file
	// — "not merely a file of that name" (cli-install#req:status-locate).
	IsExecutable func(path string) bool
	// EvalSymlinks resolves path's symlinks for classification
	// (cli-install#req:status-locate). May be left nil, in which case
	// classification uses only the unresolved path.
	EvalSymlinks func(path string) (string, error)
	// Run executes path with args directly, without a shell, with empty
	// stdin, "NO_COLOR=1" set, and returns the combined stdout+stderr —
	// so a probed binary's error or usage text is still available for
	// Status.Output when a step doesn't recognize the subcommand. Run
	// MUST honor ctx's deadline by killing the process when it expires
	// (cli-install#req:status-probe-bounded) and MUST NOT itself retry,
	// write any file, or make a network request.
	Run func(ctx context.Context, path string, args []string) ([]byte, error)
}

// DefaultEnv returns an Env wired to the real host: the real "PATH"
// environment variable, the real running executable's directory, real
// filesystem and symlink checks, and real (network-free, no-shell) process
// execution. It is the Env a production `install`/`version`-probing
// command wires in; tests use a purpose-built Env instead.
func DefaultEnv() Env {
	return Env{
		PathDirs:     defaultPathDirs,
		HostDir:      defaultHostDir,
		IsExecutable: defaultIsExecutable,
		EvalSymlinks: filepath.EvalSymlinks,
		Run:          defaultRun,
	}
}

// osExecutable and evalSymlinksFunc are test seams over os.Executable and
// filepath.EvalSymlinks, named to match selfupdate/detect.go's own seams
// for the identical fallback pattern: DetectSelf-style symlink resolution
// that falls back to the unresolved path on error rather than failing.
var (
	osExecutable     = os.Executable
	evalSymlinksFunc = filepath.EvalSymlinks
)

// defaultPathDirs splits the real "PATH" environment variable using the
// host's own list separator, dropping relative entries — REQ: status-
// locate: "Relative PATH entries MUST be ignored" — so an empty PATH entry
// (which POSIX shells read as the current directory) is dropped along with
// any other non-absolute one.
func defaultPathDirs() []string {
	raw := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
	dirs := make([]string, 0, len(raw))
	for _, d := range raw {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		dirs = append(dirs, d)
	}
	return dirs
}

// defaultHostDir resolves the running executable's own directory,
// following symlinks first (the same fallback-to-unresolved behavior as
// selfupdate.Config.DetectSelf, for the same reason: a Homebrew cask shim
// is typically a symlink, and a directory that can't be resolved is still
// worth reporting as-is rather than failing the whole probe).
func defaultHostDir() (string, error) {
	exe, err := osExecutable()
	if err != nil {
		return "", err
	}
	resolved, err := evalSymlinksFunc(exe)
	if err != nil {
		resolved = exe
	}
	return filepath.Dir(resolved), nil
}

// defaultIsExecutable reports whether path names an executable regular
// file: present, not a directory, and — on POSIX, where DefaultEnv's
// caller already appended the platform executable suffix to the filename
// it is checking — carrying at least one execute permission bit. Windows
// has no such bit; DefaultEnv's caller has already matched on the ".exe"
// suffix, so existence as a regular file is the whole check there.
func defaultIsExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if goosName == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// waitDelay bounds how long defaultRun's Wait keeps reading stdout/stderr
// after the process itself has been killed. Without it, a probed binary
// that forked a grandchild before being killed (a shell script's "sleep"
// child, say) can leave that grandchild holding the same stdout/stderr
// pipe open — Wait would then block until the grandchild itself exits,
// silently defeating cli-install#req:status-probe-bounded's kill. This is
// exec.Cmd.WaitDelay's documented purpose: past this grace period past
// cancellation, Wait force-closes the I/O pipes instead of waiting for
// them to close on their own.
const waitDelay = 500 * time.Millisecond

// defaultRun executes path with args directly (no shell), with empty
// stdin, "NO_COLOR=1" added to the ambient environment, and returns
// combined stdout+stderr. ctx's deadline kills the process — exec.
// CommandContext's own documented behavior — satisfying cli-install#req:
// status-probe-bounded without this package managing the kill itself;
// WaitDelay (see above) keeps that kill effective even against orphaned
// grandchildren.
func defaultRun(ctx context.Context, path string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path/args are caller-configured (a located catalog binary), not attacker input
	cmd.Env = append(probeEnv(os.Environ()), "NO_COLOR=1")
	cmd.WaitDelay = waitDelay
	return cmd.CombinedOutput()
}

// probeTokenVars are environment variables that authenticate GitHub API
// requests. Probing a target (even for a bare `install` listing, per
// cli-install#req:list-offline-read-only) executes any binary named after a
// catalog id found on PATH, the host directory, or --dir; that binary is
// this package's own catalog CLI in the overwhelmingly common case, but
// could be anything with that name (task-5 review M13). Stripping these
// keeps a caller's own GH_TOKEN/GITHUB_TOKEN from ever reaching a probed
// process, the same "least exposure" posture selfupdate's own release
// lookups apply to the token they DO need.
var probeTokenVars = []string{"GH_TOKEN", "GITHUB_TOKEN"}

// probeEnv returns env with every probeTokenVars entry removed.
func probeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		strip := false
		for _, tok := range probeTokenVars {
			if strings.HasPrefix(kv, tok+"=") {
				strip = true
				break
			}
		}
		if !strip {
			out = append(out, kv)
		}
	}
	return out
}
