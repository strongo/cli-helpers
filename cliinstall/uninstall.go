package cliinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/strongo/cli-helpers/selfupdate"
)

// UninstallOutcome classifies one target's uninstall result.
type UninstallOutcome int

const (
	// UninstallOutcomeUninstalled means the target was successfully uninstalled.
	UninstallOutcomeUninstalled UninstallOutcome = iota
	// UninstallOutcomeNotInstalled means the target was not installed on this system.
	UninstallOutcomeNotInstalled
	// UninstallOutcomeDryRun means --dry-run reported the planned uninstallation without performing it.
	UninstallOutcomeDryRun
	// UninstallOutcomeDeclined means confirmation was declined.
	UninstallOutcomeDeclined
	// UninstallOutcomeRedirected means a managed uninstallation was reported (print-only).
	UninstallOutcomeRedirected
	// UninstallOutcomeFailed means uninstallation failed.
	UninstallOutcomeFailed
)

// String renders UninstallOutcome as a stable token.
func (o UninstallOutcome) String() string {
	switch o {
	case UninstallOutcomeUninstalled:
		return "uninstalled"
	case UninstallOutcomeNotInstalled:
		return "not_installed"
	case UninstallOutcomeDryRun:
		return "dry_run"
	case UninstallOutcomeDeclined:
		return "declined"
	case UninstallOutcomeRedirected:
		return "redirected"
	case UninstallOutcomeFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// UninstallMethod is how the target is removed.
type UninstallMethod int

const (
	// UninstallMethodDirect means removing the executable file directly from disk.
	UninstallMethodDirect UninstallMethod = iota
	// UninstallMethodHomebrew means running `brew uninstall --cask <token>`.
	UninstallMethodHomebrew
	// UninstallMethodManaged means delegating to an external package manager.
	UninstallMethodManaged
)

// String renders UninstallMethod as a stable token.
func (m UninstallMethod) String() string {
	switch m {
	case UninstallMethodDirect:
		return "direct"
	case UninstallMethodHomebrew:
		return "homebrew"
	case UninstallMethodManaged:
		return "managed"
	default:
		return "unknown"
	}
}

// UninstallResult is one target's planned or executed uninstall outcome.
type UninstallResult struct {
	Target     string
	Outcome    UninstallOutcome
	Method     UninstallMethod
	Path       string
	CaskArgv   []string
	ManagerCmd []string
	Status     Status
	Failure    *selfupdate.Failure
	Warnings   []string
}

// UninstallBatchResult represents the outcome of an uninstall batch.
type UninstallBatchResult struct {
	Host    string
	Results []UninstallResult
}

// Failed reports whether at least one target failed in the batch.
func (b UninstallBatchResult) Failed() bool {
	for _, r := range b.Results {
		if r.Outcome == UninstallOutcomeFailed {
			return true
		}
	}
	return false
}

// Failure returns the aggregated batch failure error, or nil if no failures occurred.
func (b UninstallBatchResult) Failure() error {
	var failures []*selfupdate.Failure
	for _, r := range b.Results {
		if r.Outcome == UninstallOutcomeFailed && r.Failure != nil {
			failures = append(failures, r.Failure)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &BatchFailure{Failures: failures}
}

// UninstalledCount returns the number of targets successfully uninstalled.
func (b UninstallBatchResult) UninstalledCount() int {
	count := 0
	for _, r := range b.Results {
		if r.Outcome == UninstallOutcomeUninstalled {
			count++
		}
	}
	return count
}

// UninstallOptions configures the uninstall operation.
type UninstallOptions struct {
	HostID       string
	Dir          string
	All          bool
	DryRun       bool
	Yes          bool
	Purge        bool
	Env          InstallEnv
	ProbeOptions ProbeOptions
	Confirm      func(targets []UninstallResult) (bool, error)
}

// PlanUninstall probes each target and decides the uninstallation method.
func PlanUninstall(ctx context.Context, names []string, opts UninstallOptions) (UninstallBatchResult, error) {
	_, ok := ByID(opts.HostID)
	if !ok {
		panic("cliinstall: host id " + opts.HostID + " is not in the compiled catalog")
	}

	var entries []Entry
	if opts.All && len(names) == 0 {
		for _, e := range Entries() {
			entries = append(entries, e)
		}
	} else {
		unique := dedupeNames(names)
		var unknown []string
		for _, name := range unique {
			e, ok := ByID(name)
			if !ok {
				unknown = append(unknown, name)
				continue
			}
			entries = append(entries, e)
		}
		if len(unknown) > 0 {
			return UninstallBatchResult{Host: opts.HostID}, unknownTargetsFailure(unknown)
		}
	}

	probeDir := opts.Dir
	if probeDir != "" {
		probeDir = resolveDir(probeDir, goosName, opts.Env.EvalSymlinks)
	}

	statuses := Probe(ctx, entries, probeDir, opts.Env.Env, opts.ProbeOptions)

	results := make([]UninstallResult, 0, len(entries))
	for i, e := range entries {
		status := statuses[i]

		// If --all was passed, skip not installed targets from the candidate list
		if opts.All && len(names) == 0 && status.State != Installed {
			continue
		}

		if status.State != Installed {
			results = append(results, UninstallResult{
				Target:   e.ID,
				Outcome:  UninstallOutcomeNotInstalled,
				Status:   status,
				Warnings: status.Warnings,
			})
			continue
		}

		// Target is installed; determine method
		method := UninstallMethodDirect
		var caskArgv []string
		var path string

		if status.Manager != nil && (strings.EqualFold(status.Manager.Name, "homebrew") || strings.EqualFold(status.Manager.Name, "brew")) && e.CaskToken != "" {
			method = UninstallMethodHomebrew
			caskArgv = []string{"brew", "uninstall", "--cask", e.CaskToken}
		} else {
			method = UninstallMethodDirect
			path = status.Path
		}

		results = append(results, UninstallResult{
			Target:   e.ID,
			Outcome:  UninstallOutcomeDryRun,
			Method:   method,
			Path:     path,
			CaskArgv: caskArgv,
			Status:   status,
			Warnings: status.Warnings,
		})
	}

	return UninstallBatchResult{
		Host:    opts.HostID,
		Results: results,
	}, nil
}

// ExecuteUninstall executes the planned uninstallation.
func ExecuteUninstall(ctx context.Context, batch UninstallBatchResult, opts UninstallOptions) (UninstallBatchResult, error) {
	results := make([]UninstallResult, len(batch.Results))
	copy(results, batch.Results)

	removeFile := os.Remove

	for i, r := range results {
		if r.Outcome != UninstallOutcomeDryRun {
			continue
		}

		if opts.DryRun {
			continue
		}

		switch r.Method {
		case UninstallMethodHomebrew:
			if opts.Env.RunManaged == nil {
				results[i].Outcome = UninstallOutcomeRedirected
				results[i].Warnings = append(results[i].Warnings, "Homebrew cask uninstallation requires running: "+strings.Join(r.CaskArgv, " "))
			} else {
				caskToken := ""
				if len(r.CaskArgv) > 0 {
					caskToken = r.CaskArgv[len(r.CaskArgv)-1]
				}
				if err := opts.Env.RunManaged(ctx, "brew", []string{"uninstall", "--cask", caskToken}); err != nil {
					results[i].Outcome = UninstallOutcomeFailed
					results[i].Failure = &selfupdate.Failure{
						Kind: selfupdate.KindManagedCommand,
						Err:  fmt.Errorf("brew uninstall failed: %w", err),
					}
				} else {
					results[i].Outcome = UninstallOutcomeUninstalled
				}
			}

		case UninstallMethodDirect:
			if r.Path == "" {
				results[i].Outcome = UninstallOutcomeFailed
				results[i].Failure = &selfupdate.Failure{
					Kind: selfupdate.KindUnexpected,
					Err:  errors.New("executable path not found"),
				}
				continue
			}

			if err := removeFile(r.Path); err != nil {
				if os.IsNotExist(err) {
					results[i].Outcome = UninstallOutcomeUninstalled
				} else {
					results[i].Outcome = UninstallOutcomeFailed
					results[i].Failure = &selfupdate.Failure{
						Kind: selfupdate.KindPermission,
						Path: r.Path,
						Err:  fmt.Errorf("remove %s: %w", r.Path, err),
					}
				}
			} else {
				results[i].Outcome = UninstallOutcomeUninstalled
			}

		default:
			results[i].Outcome = UninstallOutcomeFailed
			results[i].Failure = &selfupdate.Failure{
				Kind: selfupdate.KindUnexpected,
				Err:  fmt.Errorf("unsupported uninstall method %v", r.Method),
			}
		}
	}

	return UninstallBatchResult{
		Host:    batch.Host,
		Results: results,
	}, nil
}
