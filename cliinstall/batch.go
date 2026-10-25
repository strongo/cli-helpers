package cliinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/strongo/cli-helpers/selfupdate"
)

// planConcurrency bounds how many direct-install targets Plan resolves a
// release for at once, matching Probe's own default concurrency
// convention (task-5 review B1: "PlanInstall... ≤4 concurrent").
const planConcurrency = 4

// Plan validates every name, probes the running host's current status, and
// resolves an install method, destination (or cask) and — for a direct
// install that is not already installed — the EXACT release to install,
// once per target (cli-install#req:install-dry-run,
// cli-install#req:direct-release-install). It asks no confirmation,
// downloads nothing, runs no manager command, and creates no directory:
// Plan alone is `--dry-run`'s complete answer, and it is the SAME plan
// Execute later installs from — the version, tag and asset URL a caller
// shows before confirming are never re-resolved a second time (task-5
// review B1).
//
// Every name in names MUST be a valid catalog id before ANY target is
// probed or looked up (cli-install#req:unknown-target-refused: "MUST fail
// before any confirmation, network request or write" — task-5 review S1):
// one misspelled name in a batch refuses the WHOLE batch immediately, so a
// typo never half-installs the targets that were spelled correctly. That
// case is reported as the returned error (every unknown name, and the
// valid ids); BatchResult carries no Results, since nothing was probed.
// Every other outcome — including every per-target failure — is reported
// only in BatchResult.Results, never as a returned error.
func Plan(ctx context.Context, names []string, opts Options) (BatchResult, error) {
	hostEntry, ok := ByID(opts.HostID)
	if !ok {
		panic("cliinstall: host id " + opts.HostID + " is not in the compiled catalog")
	}

	unique := dedupeNames(names)
	entries := make([]Entry, len(unique))
	var unknown []string
	for i, name := range unique {
		e, ok := ByID(name)
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		entries[i] = e
	}
	if len(unknown) > 0 {
		return BatchResult{Host: opts.HostID}, unknownTargetsFailure(unknown)
	}

	hostDir, err := opts.Env.HostDir()
	if err != nil {
		hostDir = ""
	}

	// S6: resolve --dir exactly once. planMethod resolves opts.Dir again
	// internally for the destination it returns (a pure, deterministic
	// function of the same raw value — see resolveDir's own doc comment —
	// so the two calls converge on the identical path); what matters here
	// is that Probe searches that SAME resolved location, not the raw
	// --dir a relative path, a symlink, or a Windows case variant would
	// otherwise let silently diverge from.
	probeDir := opts.Dir
	if probeDir != "" {
		probeDir = resolveDir(probeDir, goosName, opts.Env.EvalSymlinks)
	}

	statuses := Probe(ctx, entries, probeDir, opts.Env.Env, opts.ProbeOptions)

	results := make([]Result, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, planConcurrency)

	for i, e := range entries {
		status := statuses[i]

		if status.State == Installed {
			results[i] = alreadyInstalledResult(e, status)
			continue
		}

		method, destDir, caskToken, createDir, failure := planMethod(hostEntry, hostDir, e, opts)
		if failure != nil {
			results[i] = Result{Target: e.ID, Outcome: OutcomeFailed, Method: method, Status: status, Failure: failure}
			continue
		}

		var warnings []string
		if method == MethodDirect && status.State == Unrecognized {
			destPath := installFilePath(goosName, destDir, e.ID)
			// S6: compare cleaned, resolved, case-folded (Windows/macOS)
			// paths — a raw "==" misses a relative, symlinked or
			// different-case --dir naming the same file the located copy
			// already occupies, letting a full download run before the
			// no-replace placement discovers the collision on its own.
			if samePath(destPath, status.Path, goosName) {
				results[i] = Result{
					Target: e.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath, Status: status,
					Failure: &selfupdate.Failure{
						Kind: selfupdate.KindDestinationExists, Path: destPath,
						Err: fmt.Errorf("an unrecognized copy of %s already occupies %s; it is never trusted or overwritten — remove it first, or choose a different --dir", e.ID, destPath),
					},
				}
				continue
			}
			if w := shadowWarning(opts.Env.PathDirs(), status, destDir); w != "" {
				warnings = append(warnings, w)
			}
		}

		if method == MethodHomebrew {
			results[i] = planHomebrewResult(e, caskToken, status, warnings)
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(i int, e Entry, destDir string, status Status, warnings []string, createDir bool) {
			defer wg.Done()
			defer func() { <-sem }()
			r := planDirectResult(ctx, e, destDir, status, warnings, opts)
			r.createDir = createDir
			results[i] = r
		}(i, e, destDir, status, warnings, createDir)
	}
	wg.Wait()

	return BatchResult{Host: opts.HostID, Results: results}, nil
}

// Execute installs every still-pending target in plan (Outcome ==
// OutcomeDryRun) — asking at most one confirmation covering all of them
// (unless opts.Yes), then installing EXACTLY the release, or running
// EXACTLY the cask command, Plan already resolved: Execute never re-probes
// a target's status or re-resolves its release (task-5 review B1). Every
// other Result in plan (already installed, or a failure Plan already
// decided) passes through unchanged.
//
// Execute always returns a fully populated BatchResult — one Result per
// target, covering every outcome including a non-interactive confirmation
// refusal — never an empty one (task-5 review S2: "the JSON one-document
// rule is broken on refusal"). Its own returned error exists only so a
// caller that wants one classification can still get it (the confirmation
// gate's own non-interactive refusal, or another error Confirm itself
// returned); BatchResult.Failure() is the way to see every target's own
// typed failure.
func Execute(ctx context.Context, plan BatchResult, opts Options) (BatchResult, error) {
	results := make([]Result, len(plan.Results))
	copy(results, plan.Results)

	var pendingIdx []int
	for i, r := range results {
		if r.Outcome == OutcomeDryRun {
			pendingIdx = append(pendingIdx, i)
		}
	}

	if len(pendingIdx) > 0 {
		proceed := opts.Yes
		if !opts.Yes {
			pendingResults := make([]Result, len(pendingIdx))
			for j, i := range pendingIdx {
				pendingResults[j] = results[i]
			}

			if opts.Confirm == nil {
				refusal := &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errNoConfirmCallback}
				markFailed(results, pendingIdx, refusal)
				return BatchResult{Host: plan.Host, Results: results}, refusal
			}

			var confirmErr error
			proceed, confirmErr = opts.Confirm(pendingResults)
			if confirmErr != nil {
				var f *selfupdate.Failure
				if !errors.As(confirmErr, &f) {
					f = &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: confirmErr}
				}
				markFailed(results, pendingIdx, f)
				return BatchResult{Host: plan.Host, Results: results}, confirmErr
			}
		}
		if !proceed {
			for _, i := range pendingIdx {
				r := results[i]
				results[i] = Result{
					Target: r.Target, Outcome: OutcomeDeclined, Method: r.Method,
					Destination: r.Destination, CaskArgv: r.CaskArgv, Status: r.Status, Warnings: r.Warnings,
				}
			}
			return BatchResult{Host: plan.Host, Results: results}, nil
		}
	}

	for _, i := range pendingIdx {
		r := results[i]
		e, _ := ByID(r.Target) // guaranteed valid: only Plan's own OutcomeDryRun rows, all built from a catalog Entry, ever reach here
		if r.Method == MethodHomebrew && opts.HomebrewPrintOnly {
			results[i] = Result{
				Target: r.Target, Outcome: OutcomeRedirected, Method: MethodHomebrew,
				CaskArgv: r.CaskArgv, Status: r.Status, Warnings: r.Warnings,
			}
			continue
		}
		results[i] = executeInstall(ctx, e, r, r.createDir, opts)
	}

	return BatchResult{Host: plan.Host, Results: results}, nil
}

// markFailed replaces every results[i] for i in idx with an OutcomeFailed
// Result carrying failure, preserving that Result's own Method,
// Destination, CaskArgv, Status and Warnings.
func markFailed(results []Result, idx []int, failure *selfupdate.Failure) {
	for _, i := range idx {
		r := results[i]
		results[i] = Result{
			Target: r.Target, Outcome: OutcomeFailed, Method: r.Method,
			Destination: r.Destination, CaskArgv: r.CaskArgv, Status: r.Status,
			Failure: failure, Warnings: r.Warnings,
		}
	}
}

// Install is the Plan + confirm + Execute convenience: it plans the whole
// batch, and — unless opts.DryRun — executes it. `--dry-run` never calls
// Execute at all, so Plan's own OutcomeDryRun Results are the returned
// answer unchanged (cli-install#req:install-dry-run). The returned error
// is non-nil only for a batch-level refusal before any target-specific
// outcome was decided (an unknown name, per Plan, or the confirmation
// gate's own non-interactive refusal, per Execute); every other outcome,
// including every per-target failure, is reported in BatchResult.Results.
func Install(ctx context.Context, names []string, opts Options) (BatchResult, error) {
	plan, err := Plan(ctx, names, opts)
	if err != nil {
		return plan, err
	}
	if opts.DryRun {
		return plan, nil
	}
	return Execute(ctx, plan, opts)
}

// unknownTargetsFailure builds cli-install#req:unknown-target-refused's
// typed failure for one or more names that are not catalog ids, naming
// every one of them and every valid id.
func unknownTargetsFailure(names []string) *selfupdate.Failure {
	return &selfupdate.Failure{
		Kind: selfupdate.KindUnknownTarget,
		Err:  fmt.Errorf("%s: not a known install target; valid ids: %s", strings.Join(names, ", "), strings.Join(IDs(), ", ")),
	}
}

var errNoConfirmCallback = errors.New("no confirmation callback configured; pass --yes for non-interactive use")
