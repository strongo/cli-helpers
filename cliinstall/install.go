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

// planDirectResult resolves target's latest stable release exactly once —
// through selfupdate.Config.PlanInstall, which also performs the platform
// check (REQ: unsupported-platform) — without downloading, verifying, or
// writing anything (cli-install#req:install-dry-run,
// cli-install#req:direct-release-install). The returned Result carries
// OutcomeDryRun with Version, Tag and AssetURL already resolved: this is
// BOTH `--dry-run`'s own final answer AND, for a real (non-dry) batch, the
// single planned Result Execute later installs from — Plan never resolves a
// target's release a second time (task-5 review B1: "resolve once →
// confirm → install exactly that tag"). A release-lookup or
// unsupported-platform failure is reported as OutcomeFailed instead: a plan
// that cannot resolve what it would install has not walked the full
// decision path.
func planDirectResult(ctx context.Context, target Entry, destDir string, status Status, warnings []string, opts Options) Result {
	destPath := installFilePath(goosName, destDir, target.ID)
	plan, err := targetConfig(target, opts).PlanInstall(ctx)
	if err != nil {
		// PlanInstall always returns a *selfupdate.Failure (KindUnsupported
		// Platform or KindReleaseLookup), so this is never anything else to
		// fall back on.
		var f *selfupdate.Failure
		errors.As(err, &f)
		return Result{Target: target.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath, Status: status, Failure: f, Warnings: warnings}
	}

	return Result{
		Target: target.ID, Outcome: OutcomeDryRun, Method: MethodDirect,
		Destination: destPath, Version: plan.Version, Tag: plan.Tag, AssetURL: plan.AssetURL,
		Status: status, Warnings: warnings,
	}
}

// planHomebrewResult builds an OutcomeDryRun Result for a Homebrew plan —
// no network lookup is needed: the cask token is a compiled catalog
// constant, and the exact command is known without resolving anything.
func planHomebrewResult(target Entry, caskToken string, status Status, warnings []string) Result {
	return Result{
		Target: target.ID, Outcome: OutcomeDryRun, Method: MethodHomebrew,
		CaskArgv: caskArgv(caskToken), Status: status, Warnings: warnings,
	}
}

// executeInstall performs target's already-planned-and-confirmed install: a
// direct release download/verify/place through selfupdate's InstallNew,
// installing EXACTLY planned.Tag (never re-resolving "latest"), or a
// Homebrew cask install through opts.Env.RunManaged, then post-install
// verification (cli-install#req:post-install-verification). planned is one
// of Plan's own OutcomeDryRun Results — Execute never re-probes or
// re-resolves before calling this.
func executeInstall(ctx context.Context, target Entry, planned Result, createIfMissing bool, opts Options) Result {
	if planned.Method == MethodHomebrew {
		return executeHomebrewInstall(ctx, target, planned, opts)
	}
	return executeDirectInstall(ctx, target, planned, createIfMissing, opts)
}

// executeDirectInstall places target's already-planned release
// (planned.Tag/Version/AssetURL) at planned.Destination, creating its
// directory first only when planMethod chose the per-user bin directory and
// it was missing (cli-install#req:per-user-bin-dir).
func executeDirectInstall(ctx context.Context, target Entry, planned Result, createIfMissing bool, opts Options) Result {
	destPath := planned.Destination
	warnings := planned.Warnings

	if createIfMissing {
		if err := opts.Env.MkdirAll(dirOf(destPath), 0o755); err != nil {
			return Result{
				Target: target.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath,
				Failure:  &selfupdate.Failure{Kind: selfupdate.KindPermission, Path: dirOf(destPath), Err: fmt.Errorf("create %s: %w", dirOf(destPath), err)},
				Warnings: warnings,
			}
		}
	}

	installPlan := selfupdate.InstallPlan{Tag: planned.Tag, Version: planned.Version, AssetURL: planned.AssetURL}
	installResult, err := targetConfig(target, opts).InstallNew(ctx, destPath, installPlan)
	if err != nil {
		// InstallNew always returns a *selfupdate.Failure — every error
		// path inside it (download, checksum, staging, placement) wraps
		// that way — so this is never anything else to fall back on.
		var f *selfupdate.Failure
		errors.As(err, &f)
		return Result{Target: target.ID, Outcome: OutcomeFailed, Method: MethodDirect, Destination: destPath, Failure: f, Warnings: warnings}
	}

	destDir := dirOf(destPath)
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
func executeHomebrewInstall(ctx context.Context, target Entry, planned Result, opts Options) Result {
	argv := planned.CaskArgv
	warnings := planned.Warnings
	caskToken := ""
	if len(argv) > 0 {
		caskToken = argv[len(argv)-1]
	}

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
