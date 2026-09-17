package cliinstall

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/strongo/cli-helpers/selfupdate"
)

// Method is how a target will be, or was, installed.
type Method int

const (
	// MethodDirect means a verified release asset is downloaded and placed
	// at a destination path this package chose
	// (cli-install#req:direct-release-install).
	MethodDirect Method = iota
	// MethodHomebrew means `brew install --cask <token>` places the target
	// (cli-install#req:homebrew-cask-install).
	MethodHomebrew
)

// String renders Method as a stable, lower_snake_case token, matching this
// module's other String() conventions.
func (m Method) String() string {
	switch m {
	case MethodDirect:
		return "direct"
	case MethodHomebrew:
		return "homebrew"
	default:
		return "unknown"
	}
}

// Outcome classifies one target's batch install result
// (cli-install#req:multi-target-batch).
type Outcome int

const (
	// OutcomeInstalled means the target was freshly installed and
	// post-install verification ran.
	OutcomeInstalled Outcome = iota
	// OutcomeAlreadyInstalled means the target's status was already
	// Installed; nothing was downloaded, reinstalled or replaced
	// (cli-install#req:already-installed-no-op).
	OutcomeAlreadyInstalled
	// OutcomeRedirected means a Homebrew install was configured print-only:
	// the command was reported, never run
	// (cli-install#req:homebrew-cask-install).
	OutcomeRedirected
	// OutcomeDryRun means --dry-run reported the planned action without
	// performing it (cli-install#req:install-dry-run).
	OutcomeDryRun
	// OutcomeDeclined means the batch confirmation was asked and declined;
	// nothing was installed and this is not a failure
	// (cli-install#req:confirmation-gate).
	OutcomeDeclined
	// OutcomeFailed means installing (or planning to install) this target
	// failed with the typed Failure.
	OutcomeFailed
)

// String renders Outcome as a stable, lower_snake_case token.
func (o Outcome) String() string {
	switch o {
	case OutcomeInstalled:
		return "installed"
	case OutcomeAlreadyInstalled:
		return "already_installed"
	case OutcomeRedirected:
		return "redirected"
	case OutcomeDryRun:
		return "dry_run"
	case OutcomeDeclined:
		return "declined"
	case OutcomeFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Result is one named target's outcome from a batch Install call — the
// shape task-5's output writers and Cobra adapter consume. Every fact this
// module's REQs require appears here as a field, in the same shape Status
// itself uses for probed identity, so a writer can flatten Result directly
// into cli-install#req:machine-readable-output's JSON document.
type Result struct {
	// Target is the catalog id this result describes, or — only for
	// OutcomeFailed with a KindUnknownTarget Failure — the raw name that
	// was not a catalog id.
	Target string
	// Outcome classifies what happened to Target.
	Outcome Outcome
	// Method is how Target will be, or was, installed. Meaningful only when
	// Outcome is OutcomeInstalled, OutcomeRedirected, OutcomeDryRun, or
	// OutcomeFailed after a destination was already planned.
	Method Method
	// Destination is the full destination file path for a MethodDirect
	// plan or install; empty for MethodHomebrew.
	Destination string
	// CaskArgv is the exact `brew install --cask <token>` argv for a
	// MethodHomebrew plan, redirect, or install; nil for MethodDirect.
	CaskArgv []string
	// Version and Tag are the release that was (or, for a dry run, would
	// be) installed. Tag is the exact published tag, which may differ from
	// Version by a repository's TagPrefix and/or a leading "v".
	Version string
	Tag     string
	// UpdateHint names the command that updates Target, set only for
	// OutcomeAlreadyInstalled (cli-install#req:already-installed-no-op).
	UpdateHint string
	// Status is Target's probed install state: the pre-install probe for
	// every outcome except OutcomeInstalled, which carries the
	// post-install re-probe (cli-install#req:post-install-verification).
	// Zero when Target was never a valid catalog id.
	Status Status
	// Failure is set exactly when Outcome is OutcomeFailed.
	Failure *selfupdate.Failure
	// Warnings are human-readable, non-fatal notes: a shell command cache
	// hint after a real install, a shadowing notice, a PATH or
	// post-install-verification remedy, or Status's own warnings.
	Warnings []string
}

// BatchResult is the outcome of one batch Install call: the running host's
// own catalog id and one Result per de-duplicated named target, in the
// order names first named them (cli-install#req:multi-target-batch).
type BatchResult struct {
	Host    string
	Results []Result
}

// Failed reports whether at least one Result in b has OutcomeFailed
// (cli-install#req:multi-target-batch: "the command fails when at least
// one target failed"). A host's Cobra adapter uses this, together with
// each failed Result's typed Failure, to decide its own exit code.
func (b BatchResult) Failed() bool {
	for _, r := range b.Results {
		if r.Outcome == OutcomeFailed {
			return true
		}
	}
	return false
}

// InstallEnv extends Env with the additional side-effecting dependencies
// planning and installing need beyond status probing: per-user directory
// lookup, directory creation, and the managed command runner Homebrew
// installs run through (cli-install#req:no-network-in-tests: "the managed
// command runner, the per-user bin directory... MUST be injectable"). Env
// is embedded so an InstallEnv is usable anywhere an Env is required, e.g.
// passing opts.Env.Env straight to Probe.
type InstallEnv struct {
	Env
	// UserHomeDir returns the current user's home directory, used only on
	// non-Windows platforms to build the per-user bin directory.
	UserHomeDir func() (string, error)
	// Getenv reads one environment variable, used for %LOCALAPPDATA% and
	// the destination denylist's Windows roots and $GOROOT.
	Getenv func(string) string
	// MkdirAll creates the per-user bin directory (mode 0755) only when a
	// real, non-dry-run install actually needs it
	// (cli-install#req:per-user-bin-dir: "created only for a real
	// install... when missing").
	MkdirAll func(dir string, perm fs.FileMode) error
	// RunManaged executes `brew install --cask <token>` as structured
	// argv, streaming its own output
	// (cli-install#req:homebrew-cask-install). Required whenever a batch
	// actually runs a (non-print-only) Homebrew install; nil fails that
	// install with KindManagedCommand rather than panicking (see
	// executeHomebrewInstall).
	//
	// This package builds no default implementation itself: doing so would
	// mean deciding what to stream brew's output to, and this core layer
	// MUST NOT touch the terminal or assume any I/O streams
	// (cli-install#req:core-framework-neutral). A caller wires one from its
	// own owned streams — e.g. selfupdate/cliui.ManagedCommandRunner(in,
	// out, errOut) — exactly as selfupdate's own cobracmd adapter wires
	// Options.RunManaged for self-replace.
	RunManaged selfupdate.ManagedCommandRunner
}

// DefaultInstallEnv returns an InstallEnv wired to the real host: the real
// DefaultEnv, the real user home directory and environment, and real
// directory creation. RunManaged is left nil — see its own doc comment —
// and MUST be set by the caller before a batch that might run a real
// Homebrew install. It is what a production `install` command starts from;
// tests use a purpose-built InstallEnv instead.
func DefaultInstallEnv() InstallEnv {
	return InstallEnv{
		Env:         DefaultEnv(),
		UserHomeDir: os.UserHomeDir,
		Getenv:      os.Getenv,
		MkdirAll:    os.MkdirAll,
	}
}

// Options configures Install. Every side-effecting dependency is
// injectable (cli-install#req:no-network-in-tests).
type Options struct {
	// HostID is the running host's own catalog id
	// (cli-install#req:host-identity-from-catalog). It MUST be a valid
	// catalog id; an absent one is a programming error Install panics on,
	// per that REQ's "caught by the host's tests, not a runtime state
	// users see."
	HostID string
	// Dir is the --dir flag's value; empty when it was not given.
	Dir string
	// Yes skips the confirmation gate (cli-install#req:confirmation-gate),
	// matching selfupdate's own --yes convention.
	Yes bool
	// DryRun walks the full decision path without any write, brew
	// invocation, directory creation, or confirmation
	// (cli-install#req:install-dry-run).
	DryRun bool
	// HomebrewPrintOnly reports a Homebrew install's command instead of
	// running it (cli-install#req:homebrew-cask-install).
	HomebrewPrintOnly bool
	// Env carries every side-effecting dependency.
	Env InstallEnv
	// Confirm asks whether to proceed with every target that would be
	// installed, called at most once per batch, only when at least one
	// target needs it and Yes is false
	// (cli-install#req:confirmation-gate). Its own refusal — no
	// interactive terminal and Yes false — is reported by returning a
	// *selfupdate.Failure{Kind: selfupdate.KindNonInteractive}, exactly as
	// selfupdate.Options.Confirm documents
	// (self-update#req:non-interactive-refusal); Install has no
	// interactive-terminal opinion of its own; it relies entirely on this
	// callback to enforce it.
	Confirm func(names []string) (bool, error)
	// ConfigureRelease optionally overrides a target's resolved
	// selfupdate.Config before it is used to install or resolve that
	// target's release — the release-endpoint injection point
	// cli-install#req:no-network-in-tests requires. Nil keeps the
	// catalog's own defaults (the real GitHub API).
	ConfigureRelease func(target Entry, cfg selfupdate.Config) selfupdate.Config
	// ProbeOptions tunes status probing; the zero value is
	// production-correct (see ProbeOptions).
	ProbeOptions ProbeOptions
}

// caskArgv returns the exact `brew install --cask <token>` argv, or nil
// when token is empty (a MethodDirect plan/result).
func caskArgv(token string) []string {
	if token == "" {
		return nil
	}
	return []string{"brew", "install", "--cask", token}
}

// shellCacheRefreshHint carries the self-update#req:shell-command-cache-
// refresh remedy on a Result after a real install
// (cli-install#req:post-install-verification: "The result MUST carry the
// shell-command-cache-refresh remedy").
func shellCacheRefreshHint(id string) string {
	return fmt.Sprintf("if this shell does not yet find %s, run `hash -r` or start a new shell", id)
}

// shadowWarning implements cli-install#req:unrecognized-copy-not-trusted's
// shadowing note: an existing Unrecognized copy that is earlier on PATH
// than destDir will still be the copy a bare PATH lookup finds after this
// install, even though the new copy is the one this module just verified.
// Returns "" when status is not an on-PATH Unrecognized copy, or when
// destDir is itself earlier (or the same) on PATH.
func shadowWarning(pathDirs []string, status Status, destDir string) string {
	if status.State != Unrecognized || !status.OnPath || status.Path == "" {
		return ""
	}
	unrecognizedIdx := indexOfDir(pathDirs, dirOf(status.Path))
	if unrecognizedIdx < 0 {
		return ""
	}
	destIdx := indexOfDir(pathDirs, destDir)
	if destIdx < 0 || destIdx > unrecognizedIdx {
		return fmt.Sprintf("an unrecognized copy at %s is earlier on PATH and will shadow this install", status.Path)
	}
	return ""
}

// dirOf returns path's directory using "/" as the separator, matching how
// status.go's own probeOne builds Status.Path (filepath.Join, which on the
// platform this module actually runs on is "/" outside Windows and "\" on
// it — this package's own tests keep fixture paths POSIX-style even under
// an injected Windows goos, exactly as status_test.go's fixtures already
// do, so a single separator here is sufficient).
func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return path
}

// indexOfDir returns dir's index in pathDirs (cleaned comparison), or -1
// when absent.
func indexOfDir(pathDirs []string, dir string) int {
	want := cleanForCompare(dir, goosName)
	for i, d := range pathDirs {
		if cleanForCompare(d, goosName) == want {
			return i
		}
	}
	return -1
}

// alreadyInstalledResult builds an OutcomeAlreadyInstalled Result
// (cli-install#req:already-installed-no-op).
func alreadyInstalledResult(target Entry, status Status) Result {
	return Result{
		Target:     target.ID,
		Outcome:    OutcomeAlreadyInstalled,
		Version:    status.Version,
		Status:     status,
		UpdateHint: target.ID + " self-update",
		Warnings:   status.Warnings,
	}
}

// unknownTargetFailure builds cli-install#req:unknown-target-refused's
// typed failure, naming every valid id.
func unknownTargetFailure(name string) *selfupdate.Failure {
	return &selfupdate.Failure{
		Kind: selfupdate.KindUnknownTarget,
		Err:  fmt.Errorf("%q is not a known install target; valid ids: %s", name, strings.Join(IDs(), ", ")),
	}
}

// dedupeNames returns names with later duplicates removed, preserving
// first-seen order (cli-install#req:multi-target-batch: "in the order
// given, de-duplicated").
func dedupeNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
