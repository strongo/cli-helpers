# selfupdate

`github.com/strongo/selfupdate` lets a Go CLI update its own binary in
place — safely. It decides how the running binary was installed before it
touches anything: a package-manager-owned install (Homebrew, Scoop, WinGet)
is never overwritten, only redirected to that manager's own upgrade command;
a manual install (a release archive someone unpacked, or a `go install`
target) is downloaded, sha256-verified against the release's own checksums,
and swapped in atomically. Everything specific to one CLI — its identity,
its managers, its naming conventions, its exit codes — is supplied by the
caller. Nothing here is hard-coded to any one consumer.

See `spec/features/self-update/README.md` for the full behavioral contract
this package implements, and `cmd/selfupdate/` for a complete, runnable
consumer (this module's own reference CLI, which updates itself from this
repository's GitHub releases using nothing but the public API below).

## Safety guarantees

- **A managed install is redirected, never overwritten.** `Classify`
  resolves symlinks first (a Homebrew cask shim usually is one) and checks
  the result against each configured `Manager`'s path markers. A match
  routes to `ActionRedirected` under every option combination — including an
  explicit version pin and a skipped confirmation — before any download or
  write is even considered.
- **An unrecognized install is never treated as safe to overwrite.** A path
  that matches neither a manager nor a plausible manual location (`go/bin`,
  or directly inside a `bin` directory) is `Ambiguous`, not `Manual`.
  Ambiguity fails closed.
- **The checksum is verified before a single byte is extracted.** The
  downloaded archive's sha256 is compared against that release's own
  checksums file first; extraction only happens on a match. A mismatch, or a
  missing checksum entry, aborts with nothing written.
- **The replace is atomic.** The verified binary is staged to a temp file in
  the same directory as the target (same filesystem) and moved into place
  with a single rename. On POSIX that's one atomic `rename(2)`; on Windows,
  where a running `.exe` can't be overwritten, the current target is renamed
  aside first and restored if the final move fails.
- **Every failure leaves a working binary.** Release lookup, download,
  checksum, staging, and permission failures all return before any write to
  the install location. There is no failure mode that ends with a partial or
  missing executable where the old one used to be.
- **A pin fetches that release's own assets, never "latest."** The download
  URL is built from the release's own tag
  (`.../releases/download/<tag>/<asset>`), not the `/releases/latest/`
  alias — an older pinned release can't accidentally resolve to whatever is
  currently newest.

## Install

```
go get github.com/strongo/selfupdate
```

## Wiring example

A minimal CLI wires one `Config` and builds a Cobra command from it:

```go
package cli

import (
	"github.com/spf13/cobra"
	"github.com/strongo/selfupdate"
	"github.com/strongo/selfupdate/cobracmd"
)

// version is stamped at link time, e.g. -ldflags "-X your/module.version=v1.2.3".
var version = "dev"

func newSelfUpdateCommand() *cobra.Command {
	cfg := selfupdate.Config{
		BinaryName:     "wb",
		Repository:     "sneat-dev/wb",
		CurrentVersion: version,
		// "dev" is the default undetermined placeholder; only set this when
		// a different one is needed, e.g. a Homebrew-formula build reports
		// "unknown" instead.
		UndeterminedVersions: []string{"unknown"},
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew("brew upgrade --cask wb"),
		},
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		},
		VersionProbeArgs: []string{"version", "--json"},
		// AssetName, ChecksumsName, ReleasesAPIURL, DownloadURL, and
		// HTTPClient all default to GoReleaser-shaped conventions against
		// the real GitHub API — set them only to deviate, or (in tests) to
		// point at an httptest.Server.
	}

	return cobracmd.New(cfg, cobracmd.CommandOptions{
		Aliases:    []string{"update"},
		Errors:     wbErrors{}, // maps *selfupdate.Failure onto wb's own exit codes
		JSONFormat: true,
	})
}

// wbErrors implements cobracmd.ErrorMapper for wb's own three-code exit
// contract (0/1/2).
type wbErrors struct{}

func (wbErrors) Failure(err error) error {
	code := 1
	if selfupdate.KindOf(err) == selfupdate.KindPermission {
		code = 2
	}
	return exitError{code: code, err: err}
}

func (wbErrors) UpdateAvailable(res selfupdate.CheckResult) error {
	return exitError{code: 1, err: nil} // folded into wb's general findings code
}
```

A CLI that doesn't use Cobra calls `cfg.Check(ctx)` and `cfg.Update(ctx,
opts)` directly — `cobracmd` is optional sugar over the same two calls; the
root package has no command-framework dependency at all.

### Why exit codes and output belong to the host, not this package

Two real consumers of this exact package disagree about what "an update is
available" should cost: one reserves a dedicated exit code for it, one folds
it into a general findings code alongside everything else. Neither is wrong
— it's a property of each CLI's own contract with its scripts and users, not
of the update logic. So `Config.Check` and `Config.Update` never decide a
process exit code and never touch a terminal; they return typed outcomes
(`Verdict`, `Action`, `FailureKind`) a caller switches on, and `cobracmd`'s
`ErrorMapper` is exactly the seam where each consumer's own convention
plugs in. The alternative — baking one CLI's exit-code opinions into the
shared package — is what made the pre-package version of this logic
unshippable as a library in the first place: it worked for exactly one CLI.

## Dry runs

`Options.DryRun` walks the entire decision path — detection, target
resolution (latest or a pin), the downgrade guard — and stops just before
the download would start, returning `ActionPlanned` with the exact asset URL
a real run would fetch (`Outcome.PlannedURL`). `cobracmd` exposes this as
`--dry-run`. It's the way to verify a CLI's own wiring — managers, asset
naming, platform list — without ever replacing a binary.

## Testing your own wiring

Nothing in this package touches the network or the filesystem beyond what a
real `Update` call requires, and every GitHub endpoint, filesystem
operation, and TTY check it makes is overridable — see `Config.ReleasesAPIURL`/
`DownloadURL`/`HTTPClient` for pointing at an `httptest.Server`, and
`cobracmd.CommandOptions.Interactive` for driving the confirmation prompt
without a real terminal. The package's own test suite (this repo) exercises
every `FailureKind`, every `Manager`, and both exit-code-contract shapes this
way — see `*_test.go` for the pattern.
