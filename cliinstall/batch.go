package cliinstall

import (
	"context"
	"errors"
	"fmt"

	"github.com/strongo/cli-helpers/selfupdate"
)

// pendingTarget is a target whose plan is settled and which still needs
// confirmation (or opts.Yes) before executeInstall runs.
type pendingTarget struct {
	index           int
	entry           Entry
	method          Method
	destDir         string
	caskToken       string
	createIfMissing bool
	warnings        []string
}

// Install processes every name in names against the running host, in the
// order given, de-duplicated (cli-install#req:multi-target-batch). It
// looks up each name in the compiled catalog, probes every valid target's
// current status, plans an install method and destination for whichever
// are not already installed, asks at most one confirmation covering every
// target that would actually be installed, and — once confirmed —
// installs each one independently: one earlier failure never stops a
// later target. The returned error is non-nil only for a batch-level
// refusal before any target-specific outcome was decided (the
// confirmation gate's own non-interactive refusal); every other outcome,
// including every per-target failure, is reported in BatchResult.Results.
func Install(ctx context.Context, names []string, opts Options) (BatchResult, error) {
	hostEntry, ok := ByID(opts.HostID)
	if !ok {
		panic("cliinstall: host id " + opts.HostID + " is not in the compiled catalog")
	}
	hostDir, err := opts.Env.HostDir()
	if err != nil {
		hostDir = ""
	}

	unique := dedupeNames(names)
	results := make([]Result, len(unique))

	var validEntries []Entry
	var validIdx []int
	for i, name := range unique {
		e, ok := ByID(name)
		if !ok {
			results[i] = Result{Target: name, Outcome: OutcomeFailed, Failure: unknownTargetFailure(name)}
			continue
		}
		validEntries = append(validEntries, e)
		validIdx = append(validIdx, i)
	}

	statuses := Probe(ctx, validEntries, opts.Dir, opts.Env.Env, opts.ProbeOptions)

	var pending []pendingTarget
	for k, e := range validEntries {
		i := validIdx[k]
		status := statuses[k]

		if status.State == Installed {
			results[i] = alreadyInstalledResult(e, status)
			continue
		}

		method, destDir, caskToken, createIfMissing, failure := planMethod(hostEntry, hostDir, e, opts)
		if failure != nil {
			results[i] = Result{Target: e.ID, Outcome: OutcomeFailed, Method: method, Status: status, Failure: failure}
			continue
		}

		var warnings []string
		if method == MethodDirect && status.State == Unrecognized {
			destPath := installFilePath(goosName, destDir, e.ID)
			if destPath == status.Path {
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

		if opts.DryRun {
			results[i] = dryRunResult(ctx, e, method, destDir, caskToken, status, warnings, opts)
			continue
		}

		if method == MethodHomebrew && opts.HomebrewPrintOnly {
			results[i] = Result{Target: e.ID, Outcome: OutcomeRedirected, Method: MethodHomebrew, CaskArgv: caskArgv(caskToken), Status: status, Warnings: warnings}
			continue
		}

		pending = append(pending, pendingTarget{
			index: i, entry: e, method: method, destDir: destDir, caskToken: caskToken,
			createIfMissing: createIfMissing, warnings: warnings,
		})
	}

	if len(pending) > 0 {
		proceed := opts.Yes
		if !opts.Yes {
			if opts.Confirm == nil {
				return BatchResult{}, &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errNoConfirmCallback}
			}
			names := make([]string, len(pending))
			for j, p := range pending {
				names[j] = p.entry.ID
			}
			proceed, err = opts.Confirm(names)
			if err != nil {
				return BatchResult{}, err
			}
		}
		if !proceed {
			for _, p := range pending {
				results[p.index] = Result{
					Target: p.entry.ID, Outcome: OutcomeDeclined, Method: p.method,
					Destination: declinedDestination(p), CaskArgv: caskArgv(p.caskToken), Warnings: p.warnings,
				}
			}
			pending = nil
		}
	}

	for _, p := range pending {
		results[p.index] = executeInstall(ctx, p.entry, p.method, p.destDir, p.caskToken, p.createIfMissing, p.warnings, opts)
	}

	return BatchResult{Host: opts.HostID, Results: results}, nil
}

// declinedDestination reports the destination a declined MethodDirect
// target would have been installed at; empty for MethodHomebrew.
func declinedDestination(p pendingTarget) string {
	if p.method != MethodDirect {
		return ""
	}
	return installFilePath(goosName, p.destDir, p.entry.ID)
}

var errNoConfirmCallback = errors.New("no confirmation callback configured; pass --yes for non-interactive use")
