package selfupdate

import (
	"fmt"
	"net/http"
)

// Platform identifies one OS/architecture pair a consumer publishes release
// assets for. An empty Config.SupportedPlatforms means "every platform the
// host Go toolchain runs on" — most CLIs that don't cross-compile narrowly
// don't need to populate this at all.
type Platform struct {
	GOOS   string
	GOARCH string
}

// Config carries everything one CLI has already decided about itself: its
// identity, the managers that might own its install, and the naming/network
// rules for its own releases (REQ: consumer-configured-identity). Nothing in
// this package hard-codes any one CLI's values — that's what lets two
// consumers with incompatible exit-code conventions, manager sets, and
// version placeholders both build a working self-update command from the
// same Config shape (see the cobracmd subpackage and AC: two-cli-contracts-
// coexist).
//
// Every method on Config takes it by value and never mutates the caller's
// copy; the optional func/string fields left zero fall back to the
// GoReleaser-shaped defaults documented on each field.
type Config struct {
	// BinaryName is the executable name inside the release archive and the
	// first component of the default asset/checksums naming.
	BinaryName string
	// Repository is "owner/repo" on GitHub, e.g. "sneat-dev/wb".
	Repository string
	// CurrentVersion is the running build's own version string, typically
	// stamped at link time. See UndeterminedVersions for builds that can't
	// know this (e.g. a local `go build`).
	CurrentVersion string
	// UndeterminedVersions lists the CurrentVersion values that mean "this
	// build cannot say its version" (e.g. {"dev"} or {"unknown"}). Such a
	// version is reported Undetermined rather than UpToDate or
	// UpdateAvailable, and disables the pinned-downgrade guard, because
	// direction can't be established without a real version to compare from
	// (REQ: undetermined-version). Defaults to {"dev"} when empty — a Go
	// pseudo-version is never in this set implicitly; it orders below its
	// release like any other known version.
	UndeterminedVersions []string
	// Managers are the package managers that might own this binary's
	// install, checked in order by Classify. Empty means the CLI is never
	// distributed through a package manager the caller wants recognized —
	// every install classifies as Manual or Ambiguous.
	Managers []Manager
	// SupportedPlatforms restricts self-replace to the listed GOOS/GOARCH
	// pairs (REQ: unsupported-platform). Empty means all platforms the host
	// Go toolchain runs on are assumed supported.
	SupportedPlatforms []Platform
	// VersionProbeArgs are the arguments run against the newly installed
	// binary to confirm it reports the expected version after a swap
	// (REQ: post-swap-version-check). Defaults to {"--version"}.
	VersionProbeArgs []string
	// AssetName names the release archive for one binary/version/platform
	// combination. Defaults to GoReleaser's own convention:
	// "<binary>_<version>_<os>_<arch>.tar.gz" (".zip" on windows), with a
	// leading "v" in version stripped.
	AssetName func(binary, version, goos, goarch string) string
	// ChecksumsName names the release's checksums file for one version.
	// Defaults to GoReleaser's "<binary>_<version>_checksums.txt", with a
	// leading "v" in version stripped.
	ChecksumsName func(binary, version string) string
	// ReleasesAPIURL is the GitHub REST endpoint listing this repository's
	// releases, newest first. Defaults to
	// "https://api.github.com/repos/<Repository>/releases". Overriding it is
	// how this package's own tests point at an httptest.Server instead of
	// the real GitHub API.
	ReleasesAPIURL string
	// DownloadURL builds the download URL for one asset (or the checksums
	// file, which is downloaded the same way) within a specific release.
	// Defaults to that release's OWN GitHub Releases URL —
	// "https://github.com/<repository>/releases/download/<tag>/<asset>" —
	// never the "/releases/latest/download/" alias, which would silently
	// fetch whatever is currently latest instead of the pinned release
	// (REQ: pinned-exact-tag).
	DownloadURL func(repository, tag, asset string) string
	// HTTPClient is used for every GitHub request. Defaults to
	// http.DefaultClient.
	HTTPClient *http.Client
}

// withDefaults returns a copy of c with every optional field that was left
// zero filled in. It never mutates the receiver, so callers can hold one
// Config and pass it to Check and Update repeatedly without the defaulting
// of one call leaking into another.
func (c Config) withDefaults() Config {
	if len(c.UndeterminedVersions) == 0 {
		c.UndeterminedVersions = []string{"dev"}
	}
	if len(c.VersionProbeArgs) == 0 {
		c.VersionProbeArgs = []string{"--version"}
	}
	if c.AssetName == nil {
		c.AssetName = defaultAssetName
	}
	if c.ChecksumsName == nil {
		c.ChecksumsName = defaultChecksumsName
	}
	if c.ReleasesAPIURL == "" {
		c.ReleasesAPIURL = "https://api.github.com/repos/" + c.Repository + "/releases"
	}
	if c.DownloadURL == nil {
		c.DownloadURL = defaultDownloadURL
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	return c
}

// defaultAssetName implements GoReleaser's own archive-naming convention, so
// a consumer that ships via GoReleaser (the common case) needs to set
// nothing at all.
func defaultAssetName(binary, version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", binary, normalize(version), goos, goarch, ext)
}

// defaultChecksumsName implements GoReleaser's own checksums-file-naming
// convention.
func defaultChecksumsName(binary, version string) string {
	return fmt.Sprintf("%s_%s_checksums.txt", binary, normalize(version))
}

// defaultDownloadURL points at the given release's own GitHub Releases
// assets, never at the "/releases/latest/download/" alias — see the
// DownloadURL field doc for why that distinction matters for pinned
// installs.
func defaultDownloadURL(repository, tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repository, tag, asset)
}

// isUndetermined reports whether v is one of c.UndeterminedVersions. Callers
// invoke this on an already-withDefaults'd Config, so the slice is never
// empty here.
func (c Config) isUndetermined(v string) bool {
	for _, u := range c.UndeterminedVersions {
		if v == u {
			return true
		}
	}
	return false
}

// platformSupported reports whether the host platform (goosName/goarchName —
// package-level so tests can drive the unsupported-platform path on any
// host) is one c.SupportedPlatforms lists. An empty list supports every
// platform.
func (c Config) platformSupported() bool {
	if len(c.SupportedPlatforms) == 0 {
		return true
	}
	for _, p := range c.SupportedPlatforms {
		if p.GOOS == goosName && p.GOARCH == goarchName {
			return true
		}
	}
	return false
}
