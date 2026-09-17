package cliinstall

import (
	"context"
	"errors"
	"fmt"

	"github.com/strongo/cli-helpers/selfupdate"
)

// targetConfig builds target's selfupdate.Config for a fresh install: no
// running version to compare against (InstallNew never uses one), with
// opts.ConfigureRelease applied when set — the release-endpoint injection
// point cli-install#req:no-network-in-tests requires.
func targetConfig(target Entry, opts Options) selfupdate.Config {
	cfg := target.Config("")
	if opts.ConfigureRelease != nil {
		cfg = opts.ConfigureRelease(target, cfg)
	}
	return cfg
}

// dryRunResult builds an OutcomeDryRun Result, walking the full decision
// path including release resolution for a MethodDirect plan
// (cli-install#req:install-dry-run) without downloading, running brew,
// creating a directory, or asking for confirmation. A release-lookup
// failure is reported as OutcomeFailed instead: a dry run that cannot
// resolve what it would install has not walked the full decision path.
func dryRunResult(ctx context.Context, target Entry, method Method, destDir, caskToken string, status Status, warnings []string, opts Options) Result {
	if method == MethodHomebrew {
		return Result{
			Target: target.ID, Outcome: OutcomeDryRun, Method: MethodHomebrew,
			CaskArgv: caskArgv(caskToken), Status: status, Warnings: warnings,
		}
	}

	destPath := installFilePath(goosName, destDir, target.ID)
	result, err := targetConfig(target, opts).Check(ctx)
	if err != nil {
		// Config.Check always returns a *selfupdate.Failure (its own
		// release-lookup failure path wraps every error that way), so this
		// is never anything else to fall back on.
		var f *selfupdate.Failure
		errors.As(err, &f)
		return Result{Target: target.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath, Status: status, Failure: f, Warnings: warnings}
	}

	return Result{
		Target: target.ID, Outcome: OutcomeDryRun, Method: MethodDirect,
		Destination: destPath, Version: result.Latest, Tag: plannedTag(target, result.Latest),
		Status: status, Warnings: warnings,
	}
}

// plannedTag reconstructs the exact tag a bare version most likely
// published under, for display only: TagPrefix + "v" + version, the
// fleet's own tagging convention (see selfupdate.Config.TagPrefix and
// e.g. synchestra's "cli-v0.15.1"). Never used to choose what is
// downloaded — InstallNew resolves and downloads by its own tag lookup.
func plannedTag(target Entry, version string) string {
	return target.TagPrefix + "v" + version
}

// executeInstall performs target's already-confirmed install: a direct
// release download/verify/place through selfupdate's InstallNew, or a
// Homebrew cask install through opts.Env.RunManaged, then post-install
// verification (cli-install#req:post-install-verification).
func executeInstall(ctx context.Context, target Entry, method Method, destDir, caskToken string, createIfMissing bool, warnings []string, opts Options) Result {
	if method == MethodHomebrew {
		return executeHomebrewInstall(ctx, target, caskToken, warnings, opts)
	}
	return executeDirectInstall(ctx, target, destDir, createIfMissing, warnings, opts)
}

// executeDirectInstall places target's latest verified release at destDir,
// creating destDir first only when planMethod chose the per-user bin
// directory and it was missing (cli-install#req:per-user-bin-dir).
func executeDirectInstall(ctx context.Context, target Entry, destDir string, createIfMissing bool, warnings []string, opts Options) Result {
	destPath := installFilePath(goosName, destDir, target.ID)

	if createIfMissing {
		if err := opts.Env.MkdirAll(destDir, 0o755); err != nil {
			return Result{
				Target: target.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath,
				Failure:  &selfupdate.Failure{Kind: selfupdate.KindPermission, Path: destDir, Err: fmt.Errorf("create %s: %w", destDir, err)},
				Warnings: warnings,
			}
		}
	}

	installResult, err := targetConfig(target, opts).InstallNew(ctx, destPath)
	if err != nil {
		// InstallNew always returns a *selfupdate.Failure — every error
		// path inside it (release lookup, download, checksum, staging,
		// placement) wraps that way — so this is never anything else to
		// fall back on.
		var f *selfupdate.Failure
		errors.As(err, &f)
		return Result{Target: target.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath, Failure: f, Warnings: warnings}
	}

	finalStatus, verifyWarnings := verifyInstalled(ctx, target, opts, MethodDirect, destPath, installResult.Version)
	warnings = append(warnings, verifyWarnings...)
	if !dirOnPath(opts.Env.PathDirs(), destDir, goosName) {
		warnings = append(warnings, fmt.Sprintf("%s is not on PATH; add it to PATH to run %s directly", destDir, target.ID))
	}
	warnings = append(warnings, shellCacheRefreshHint(target.ID))

	return Result{
		Target: target.ID, Outcome: OutcomeInstalled, Method: MethodDirect,
		Destination: destPath, Version: installResult.Version, Tag: installResult.Tag,
		Status: finalStatus, Warnings: warnings,
	}
}

// executeHomebrewInstall runs `brew install --cask <token>` through
// opts.Env.RunManaged (cli-install#req:homebrew-cask-install). A missing
// runner or a non-zero exit both fail with KindManagedCommand, naming
// `brew update` and --dir as remedies, exactly as that REQ requires.
func executeHomebrewInstall(ctx context.Context, target Entry, caskToken string, warnings []string, opts Options) Result {
	argv := caskArgv(caskToken)

	if opts.Env.RunManaged == nil {
		return Result{
			Target: target.ID, Outcome: OutcomeFailed, Method: MethodHomebrew, CaskArgv: argv,
			Failure:  &selfupdate.Failure{Kind: selfupdate.KindManagedCommand, Err: errors.New("no Homebrew command runner is configured")},
			Warnings: warnings,
		}
	}

	if err := opts.Env.RunManaged(ctx, "brew", []string{"install", "--cask", caskToken}); err != nil {
		return Result{
			Target: target.ID, Outcome: OutcomeFailed, Method: MethodHomebrew, CaskArgv: argv,
			Failure: &selfupdate.Failure{
				Kind: selfupdate.KindManagedCommand,
				Err:  fmt.Errorf("brew install --cask %s: %w; try `brew update` first, or pass --dir for a direct install", caskToken, err),
			},
			Warnings: warnings,
		}
	}

	finalStatus, verifyWarnings := verifyInstalled(ctx, target, opts, MethodHomebrew, "", "")
	warnings = append(warnings, verifyWarnings...)
	warnings = append(warnings, shellCacheRefreshHint(target.ID))

	return Result{
		Target: target.ID, Outcome: OutcomeInstalled, Method: MethodHomebrew,
		CaskArgv: argv, Version: finalStatus.Version, Status: finalStatus, Warnings: warnings,
	}
}

// verifyInstalled re-probes target after a real install — at expectDestPath
// for a direct install, or wherever brew placed it ("the first PATH copy",
// expectDestPath == "") for a Homebrew one — per cli-install#req:post-
// install-verification. A failed identification, a version mismatch (when
// expectVersion is known), or — for a direct install — PATH resolving to a
// different copy are reported as warnings, never as a failed install: the
// write already succeeded. Status's own warnings (not-on-PATH, timeout)
// are carried through unchanged.
func verifyInstalled(ctx context.Context, target Entry, opts Options, method Method, expectDestPath, expectVersion string) (Status, []string) {
	statuses := Probe(ctx, []Entry{target}, opts.Dir, opts.Env.Env, opts.ProbeOptions)
	status := statuses[0]

	var warnings []string
	switch {
	case status.State != Installed:
		if method == MethodHomebrew {
			warnings = append(warnings, fmt.Sprintf(
				"could not confirm %s after the Homebrew install; if macOS blocked an unsigned binary, remove the quarantine attribute with `xattr -d com.apple.quarantine <path>` (find it with `which %s`) and try again",
				target.ID, target.ID))
		} else {
			warnings = append(warnings, fmt.Sprintf("could not confirm %s reports the installed version at %s", target.ID, expectDestPath))
		}
	case expectVersion != "" && status.Version != expectVersion:
		warnings = append(warnings, fmt.Sprintf("%s reports version %s, expected the newly installed %s", target.ID, status.Version, expectVersion))
	case method == MethodDirect && status.Path != expectDestPath:
		warnings = append(warnings, fmt.Sprintf("PATH resolves %s to %s instead of the newly installed %s", target.ID, status.Path, expectDestPath))
	}

	warnings = append(warnings, status.Warnings...)
	return status, warnings
}
