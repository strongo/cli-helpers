package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InstallResult is what InstallNew placed.
type InstallResult struct {
	// Path is the destination path the verified binary was placed at —
	// always exactly the destPath InstallNew was called with.
	Path string
	// Version is the normalized (no leading "v", no Config.TagPrefix)
	// version of the release that was installed.
	Version string
	// Tag is that release's exact published tag, which may differ from
	// Version by a TagPrefix and/or a leading "v"
	// (REQ: multi-product-repository).
	Tag string
}

// InstallNew resolves this Config's latest stable release exactly as
// Update's own manual self-replace path does
// (REQ: latest-release-source, REQ: multi-product-repository), downloads and
// verifies its asset for the host platform using the identical download,
// checksum, and extraction code self-replace uses
// (REQ: direct-release-install, REQ: download-matching-asset,
// REQ: checksum-before-extract, REQ: unsupported-platform), and places the
// verified binary at destPath with a no-replace operation
// (REQ: install-never-overwrites): a file already at destPath — including
// one that appears after a caller decided on destPath and before this call
// finishes placing it — is never overwritten, and a failed install leaves
// neither a partial destPath nor a staging file behind.
//
// Unlike Update, InstallNew never accepts a version pin
// (REQ: direct-release-install: "pinning a target version is not offered")
// and never resolves or replaces the running binary's own path: destPath is
// entirely the caller's choice. Choosing, creating, and validating that
// destination — the per-user bin directory, the destination denylist, the
// host's own executable directory — is the cliinstall package's job, a
// different, higher-level consumer of this same Config; InstallNew itself
// applies no policy to destPath and never creates its parent directory, so a
// missing directory surfaces as an ordinary staging failure rather than
// being silently created.
//
// A permission failure is reported as KindPermission and, like the rest of
// this package, carries Path (REQ: permission-failure-identifiable); a file
// already at destPath is reported as KindDestinationExists.
func (c Config) InstallNew(ctx context.Context, destPath string) (InstallResult, error) {
	cfg := c.withDefaults()

	if !cfg.platformSupported() {
		return InstallResult{}, &Failure{Kind: KindUnsupportedPlatform, Err: fmt.Errorf("no published asset for %s/%s", goosName, goarchName)}
	}

	tag, err := cfg.latestStableTag(ctx)
	if err != nil {
		return InstallResult{}, &Failure{Kind: KindReleaseLookup, Err: err}
	}
	version := cfg.versionFromTag(tag)

	tmpPath, err := cfg.downloadAndVerify(ctx, tag, version)
	if err != nil {
		return InstallResult{}, err
	}
	defer func() { _ = os.Remove(tmpPath) }()

	if err := placeNoReplace(cfg.BinaryName, destPath, tmpPath); err != nil {
		return InstallResult{}, err
	}

	return InstallResult{Path: destPath, Version: version, Tag: tag}, nil
}

// placeNoReplace stages srcPath into destPath's own directory — reusing
// replace.go's stage helper, so this shares its staging, permission
// (0755), and cleanup-on-failure behavior with the atomic-replace path
// rather than duplicating it — and then places the staged file at destPath
// without ever replacing a file that is already there
// (REQ: install-never-overwrites). destPath's directory is never created:
// choosing and creating the destination directory is the destination-policy
// layer's job (cliinstall), not this primitive's.
//
// A failed placement leaves nothing new at destPath and no staging file
// behind: on any error the staged file is removed.
func placeNoReplace(binaryName, destPath, srcPath string) error {
	dir := filepath.Dir(destPath)

	staged, err := stage(binaryName, dir, srcPath)
	if err != nil {
		kind := KindUnexpected
		if errors.Is(err, fs.ErrPermission) {
			kind = KindPermission
		}
		return &Failure{Kind: kind, Path: destPath, Err: err}
	}

	if err := hardLinkNoReplace(staged, destPath); err != nil {
		_ = os.Remove(staged)
		switch {
		case errors.Is(err, fs.ErrExist):
			return &Failure{Kind: KindDestinationExists, Path: destPath, Err: fmt.Errorf("destination already exists: %w", fs.ErrExist)}
		case errors.Is(err, fs.ErrPermission):
			return &Failure{Kind: KindPermission, Path: destPath, Err: err}
		default:
			return &Failure{Kind: KindUnexpected, Path: destPath, Err: err}
		}
	}
	return nil
}

// osLinkFunc is a test seam over os.Link so placeNoReplace's KindPermission
// and KindUnexpected branches for a failed LINK (as opposed to a failed
// STAGE, which has its own real-filesystem test) are exercisable without a
// filesystem or permission layout that can make os.Link itself fail while
// leaving the same directory's earlier os.CreateTemp staging call — which
// needs the identical write permission — succeeding.
var osLinkFunc = os.Link

// hardLinkNoReplace places oldpath at newpath by giving the same file a
// second name and then removing the first. os.Link is what makes this
// no-replace: it refuses — reported as fs.ErrExist, via each platform's own
// syscall.Errno.Is mapping (EEXIST on POSIX, ERROR_ALREADY_EXISTS on
// Windows) — when newpath already exists, atomically: there is no window in
// which newpath could be observed absent and then overwritten by this call.
//
// This is REQ: install-never-overwrites' own first-named technique ("hard
// link then unlink of the stage") and is used here as the ONLY placement
// primitive, deliberately not "renameat2(RENAME_NOREPLACE) on Linux" or
// "MoveFileEx without MOVEFILE_REPLACE_EXISTING" from that REQ's other named
// alternatives: os.Link needs no platform-specific package or build tag and
// behaves identically on Linux, macOS, and Windows (CreateHardLink there
// refuses the same way), so this single implementation is exercised for
// real by every CI job that runs `go test ./selfupdate/...` at all, rather
// than leaving a platform-specific branch that only a Windows-only job could
// cover. It requires oldpath and newpath to share a filesystem, which
// placeNoReplace guarantees by staging oldpath inside newpath's own
// directory.
//
// A failure other than newpath already existing — for example a
// destination filesystem that does not support hard links at all, such as
// FAT — is returned as-is and leaves newpath untouched. REQ: install-never-
// overwrites permits (but does not require) a check-then-rename fallback
// for exactly that case; this package does not implement one, because
// failing outright still satisfies the requirement it exists for (an
// existing file is never overwritten) without adding a second, racier code
// path for an install destination this fleet does not use.
func hardLinkNoReplace(oldpath, newpath string) error {
	if err := osLinkFunc(oldpath, newpath); err != nil {
		return err
	}
	// newpath now names the same file oldpath did; removing oldpath's own
	// name is cleanup, not correctness — newpath already holds the right
	// content regardless of whether this succeeds.
	_ = os.Remove(oldpath)
	return nil
}
