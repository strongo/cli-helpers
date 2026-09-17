package cliinstall

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/strongo/cli-helpers/selfupdate"
)

// goosName is a test seam over runtime.GOOS, following the same pattern as
// selfupdate's own goosName var, so the platform executable suffix
// (status-locate's ".exe" rule) is exercisable for every OS from any host,
// including in the GOOS=windows/GOOS=darwin `go vet` gates this package's
// plan requires.
var goosName = runtime.GOOS

// State is a located target's install state
// (cli-install#req:status-probe-order, cli-install#req:list-relevant).
type State int

const (
	// NotInstalled means no executable named after the target's id (with
	// the platform suffix) was found on PATH, in the host directory, or in
	// dir.
	NotInstalled State = iota
	// Installed means a located copy's identity was confirmed by one of
	// the three status-probe-order steps.
	Installed
	// Unrecognized means a copy was located but no probe step confirmed
	// this target's identity — never trusted or reported as installed
	// (cli-install#req:unrecognized-copy-not-trusted).
	Unrecognized
)

// String renders State as a stable, lower_snake_case token suitable for
// machine-readable output, matching selfupdate.Action/FailureKind's
// convention.
func (s State) String() string {
	switch s {
	case NotInstalled:
		return "not_installed"
	case Installed:
		return "installed"
	case Unrecognized:
		return "unrecognized"
	default:
		return "unknown"
	}
}

// VersionSource identifies which status-probe-order step produced a
// Status's Version/Commit/Date/DateSource fields. Meaningful only when
// State is Installed.
type VersionSource int

const (
	// VersionSourceNone means no step produced version information —
	// State is not Installed.
	VersionSourceNone VersionSource = iota
	// VersionSourceJSON means step 1, `version --json`, succeeded.
	VersionSourceJSON
	// VersionSourceText means step 2, plain `version` text, succeeded.
	VersionSourceText
	// VersionSourceFlag means step 3, `--version` matching a declared
	// legacy signature, succeeded.
	VersionSourceFlag
)

// String renders VersionSource as the stable token REQ: machine-readable-
// output's "version_source" JSON field carries.
func (v VersionSource) String() string {
	switch v {
	case VersionSourceJSON:
		return "version_json"
	case VersionSourceText:
		return "version_text"
	case VersionSourceFlag:
		return "version_flag"
	default:
		return ""
	}
}

// Status is one target's located and probed install state — the type
// task-4's planner and task-5's output writers consume. It is produced
// entirely offline and read-only by Probe (cli-install#req:list-offline-
// read-only).
type Status struct {
	// ID is the catalog id this Status describes.
	ID string
	// State is this target's install state.
	State State

	// Path is the primary reported copy: the first PATH match, or — when
	// no copy is on PATH — the first copy found in the host directory or
	// dir (cli-install#req:status-locate). Empty when State is
	// NotInstalled.
	Path string
	// OnPath reports whether Path itself was found on an absolute PATH
	// entry. False means Path was found only in the host directory or
	// dir, in which case Warnings carries a not-on-PATH warning.
	OnPath bool
	// OtherPaths lists every other located copy of this target, in the
	// order they were found (cli-install#req:status-locate: "additional
	// copies... count in text, full paths in JSON").
	OtherPaths []string

	// Method classifies Path's install method, checking both Path itself
	// and its symlink-resolved form against every manager declared
	// anywhere in the compiled catalog, preferring managed
	// (cli-install#req:status-locate). Meaningful only when State is
	// Installed or Unrecognized.
	Method selfupdate.InstallMethod
	// Manager identifies the owning package manager when Method is
	// selfupdate.Managed; nil otherwise.
	Manager *selfupdate.Manager

	// Version, Commit, Date and DateSource are read from whichever step
	// VersionSource names. Commit and Date are "" when unknown; a raw
	// "none" or "unknown" token from a text probe is normalized to ""
	// (cli-install#req:status-probe-order). Meaningful only when State is
	// Installed.
	Version    string
	Commit     string
	Date       string
	DateSource string
	// VersionSource names the step that produced Version/Commit/Date/
	// DateSource.
	VersionSource VersionSource

	// Output is the trimmed combined output of the last probe step that
	// produced any, kept for display when State is Unrecognized
	// (cli-install#req:status-probe-order: "reported with its path and
	// the output that was seen").
	Output string

	// Warnings are human-readable, non-fatal notes: a not-on-PATH warning
	// when OnPath is false, or a timeout warning when this target's probe
	// budget was exhausted.
	Warnings []string
}

// ProbeOptions tunes Probe's concurrency and per-target time budget. The
// zero value is production-correct per cli-install#req:status-probe-
// bounded (a 3 second per-target budget, at least four targets probed
// concurrently); tests override both fields to stay fast and deterministic
// without faking Env.Run's own timeout behavior.
type ProbeOptions struct {
	// Concurrency is how many targets are located and probed at once.
	// Zero defaults to 4.
	Concurrency int
	// Budget is the per-target time budget covering every probe step.
	// Zero defaults to 3 seconds.
	Budget time.Duration
}

const (
	defaultConcurrency = 4
	defaultBudget      = 3 * time.Second
)

func (o ProbeOptions) withDefaults() ProbeOptions {
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.Budget <= 0 {
		o.Budget = defaultBudget
	}
	return o
}

// Probe locates and identifies every entry in targets — searching absolute
// PATH entries, the host executable's directory, and dir (empty when no
// --dir was given) — and returns one Status per target, in targets' own
// order (cli-install#req:status-locate, cli-install#req:status-probe-order,
// cli-install#req:status-probe-bounded). It makes no network requests and
// writes, moves or deletes nothing (cli-install#req:list-offline-read-
// only); every side-effecting operation goes through env, so tests need
// exec no real installed binary.
func Probe(ctx context.Context, targets []Entry, dir string, env Env, opts ProbeOptions) []Status {
	opts = opts.withDefaults()

	dirs := searchDirs(env, dir)
	managers := allCatalogManagers()

	results := make([]Status, len(targets))
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, target Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = probeOne(ctx, target, dirs, env, managers, opts.Budget)
		}(i, target)
	}
	wg.Wait()

	return results
}

// resolvedDirs is the ordered set of directories Locate searches, alongside
// which of them are PATH entries: order matters both for which directory is
// searched first and for REQ: status-locate's "first PATH match is the
// reported copy" rule.
type resolvedDirs struct {
	dirs   []string
	onPath []bool
}

// searchDirs builds the ordered directory list Probe searches for every
// target: absolute PATH entries (env.PathDirs is trusted to already exclude
// relative ones, but a defensive filter here means the rule holds
// regardless of a caller's Env implementation), then the host directory,
// then dir. env.PathDirs and env.HostDir are required (like env.Run, a nil
// func value panics); DefaultEnv always sets both.
func searchDirs(env Env, dir string) resolvedDirs {
	var rd resolvedDirs

	for _, d := range env.PathDirs() {
		if !filepath.IsAbs(d) {
			continue
		}
		rd.dirs = append(rd.dirs, d)
		rd.onPath = append(rd.onPath, true)
	}

	if hostDir, err := env.HostDir(); err == nil && hostDir != "" {
		rd.dirs = append(rd.dirs, hostDir)
		rd.onPath = append(rd.onPath, false)
	}

	if dir != "" {
		rd.dirs = append(rd.dirs, dir)
		rd.onPath = append(rd.onPath, false)
	}

	return rd
}

// probeOne locates target across dirs, classifies the primary copy found,
// and — when one is found — runs the status-probe-order identity steps
// against it.
func probeOne(ctx context.Context, target Entry, dirs resolvedDirs, env Env, managers []selfupdate.Manager, budget time.Duration) Status {
	status := Status{ID: target.ID}

	filename := target.ID
	if goosName == "windows" {
		filename += ".exe"
	}

	var (
		paths  []string
		onPath []bool
	)
	for i, d := range dirs.dirs {
		candidate := filepath.Join(d, filename)
		if !env.IsExecutable(candidate) {
			continue
		}
		if containsPath(paths, candidate) {
			continue
		}
		paths = append(paths, candidate)
		onPath = append(onPath, dirs.onPath[i])
	}

	if len(paths) == 0 {
		status.State = NotInstalled
		return status
	}

	primary, primaryOnPath, other := choosePrimary(paths, onPath)
	status.Path = primary
	status.OnPath = primaryOnPath
	status.OtherPaths = other
	if !primaryOnPath {
		status.Warnings = append(status.Warnings, fmt.Sprintf("%s is not on PATH", primary))
	}

	det := classifyLocated(primary, env, managers)
	status.Method = det.Method
	status.Manager = det.Manager

	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	result := identify(probeCtx, env, primary, target)
	if result.timedOut {
		status.State = Unrecognized
		status.Warnings = append(status.Warnings, fmt.Sprintf("timed out probing %s after %s", primary, budget))
		return status
	}

	if result.matched {
		status.State = Installed
		status.Version = result.version
		status.Commit = result.commit
		status.Date = result.date
		status.DateSource = result.dateSource
		status.VersionSource = result.source
		return status
	}

	status.State = Unrecognized
	status.Output = result.output
	return status
}

// containsPath reports whether path is already in paths, so the same
// directory named twice on PATH (or equal to the host directory) is
// reported once, not as its own "additional copy".
func containsPath(paths []string, path string) bool {
	for _, p := range paths {
		if p == path {
			return true
		}
	}
	return false
}

// choosePrimary implements REQ: status-locate's "first PATH match is the
// reported copy; any other copies MUST be reported as additional paths"
// rule: the first PATH-found copy wins when one exists, otherwise the
// first copy found anywhere (host directory or dir, in search order).
func choosePrimary(paths []string, onPath []bool) (primary string, primaryOnPath bool, other []string) {
	primaryIndex := 0
	for i, p := range onPath {
		if p {
			primaryIndex = i
			break
		}
	}
	primary = paths[primaryIndex]
	primaryOnPath = onPath[primaryIndex]
	for i, p := range paths {
		if i == primaryIndex {
			continue
		}
		other = append(other, p)
	}
	return primary, primaryOnPath, other
}

// allCatalogManagers returns every Manager declared by any entry in the
// whole compiled-in catalog, deduplicated by Name+PathMarkers — REQ:
// status-locate: "checked against the markers of every manager used
// anywhere in the catalog", not merely the target's own entry, which is
// what lets a Snap-dispatched binary classify correctly even for a target
// whose own catalog entry declares no Snap manager.
func allCatalogManagers() []selfupdate.Manager {
	var out []selfupdate.Manager
	seen := make(map[string]bool)
	for _, e := range Entries() {
		for _, m := range e.Managers {
			key := m.Name + "\x00" + strings.Join(m.PathMarkers, "\x00")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// classifyLocated classifies path (as found, never resolved) and its
// symlink-resolved form against managers, preferring managed when either
// matches (cli-install#req:status-locate) — this is how a Snap shim such
// as "/snap/bin/ingitdb", a symlink into a generic dispatcher binary that
// itself carries no recognizable marker, is still recognized as Snap-
// managed from its unresolved PATH location, and how a manager whose
// layout only shows up after resolving a symlink is still caught.
func classifyLocated(path string, env Env, managers []selfupdate.Manager) selfupdate.Detection {
	det := selfupdate.Classify(path, managers)
	if det.Method == selfupdate.Managed {
		return det
	}
	if env.EvalSymlinks == nil {
		return det
	}
	resolved, err := env.EvalSymlinks(path)
	if err != nil || resolved == path {
		return det
	}
	if rdet := selfupdate.Classify(resolved, managers); rdet.Method == selfupdate.Managed {
		return selfupdate.Detection{Method: selfupdate.Managed, Manager: rdet.Manager, Path: path}
	}
	return det
}
