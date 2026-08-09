---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: Self-Update Library

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/strongo/selfupdate/spec/features/self-update?op=explore) | [Edit](https://specscore.studio/app/github.com/strongo/selfupdate/spec/features/self-update?op=edit) | [Ask question](https://specscore.studio/app/github.com/strongo/selfupdate/spec/features/self-update?op=ask) | [Request change](https://specscore.studio/app/github.com/strongo/selfupdate/spec/features/self-update?op=request-change) |
**Status:** Stable
**Source Ideas:** —

## Summary

`github.com/strongo/selfupdate` lets any Go CLI update its own binary in place.
It decides how the running binary was installed: a package-manager-owned install
is never overwritten — the caller is told the exact upgrade command instead —
while a manual install (release archive, `go install`) is replaced by
downloading the release asset for the host platform, verifying its sha256
against the release checksums, and atomically swapping the executable. A
check-only mode reports availability without touching anything, and an explicit
version pin installs an exact release.

This repository owns the behavior contract. Each consuming CLI carries a thin
Feature that points here and specifies only its own configuration and deviations
— see [Consumers](#consumers).

## Problem

Every CLI that ships binaries eventually needs to update itself, and every one of
them gets the same two things wrong.

The first is deciding whether a swap is allowed at all. Overwriting a
Homebrew-managed binary desynchronizes the manager's bookkeeping: the Caskroom
still claims the old version and the next `brew upgrade` fights the file the CLI
wrote. Overwriting a Scoop or WinGet install is the same mistake on another
platform. Install-method detection is therefore part of the contract, not an
implementation detail, and when detection is uncertain the safe outcome — do not
replace, explain — must be the default.

The second is that the update path is the one code path where a bug leaves the
user with no working tool. A partially written executable cannot be re-run to
fix itself.

Neither problem is CLI-specific, but the surrounding decisions are: exit-code
conventions, output formats, which managers publish the tool, and how the binary
reports its own version all differ per CLI. A shared implementation is only
reusable if those stay with the consumer. Copying the logic into each CLI's
`internal/` tree — the status quo before this package — means every consumer
re-derives the same safety rules and only one of them gets reviewed.

## Behavior

### Portable contract

#### REQ: detect-managed

The package MUST classify a resolved executable path as package-managed when it
lies inside a configured manager's layout, following symlinks first so a
symlinked shim resolves to its real location. A managed classification MUST route
to the redirect outcome and MUST NOT self-replace.

#### REQ: detect-manual

The package MUST classify the running binary as manual when it is not recognized
as managed and its path is a plausible user or Go install location — a
`go install` target under `GOBIN` or `GOPATH/bin`, or a binary directly inside a
`bin` directory such as `~/bin` or `/usr/local/bin`. A manual classification is
eligible for self-replace.

#### REQ: ambiguous-safe-default

When the install method cannot be confidently classified, the package MUST NOT
self-replace. It MUST report the ambiguity so the consumer can print manual
guidance and fail. Ambiguity MUST NOT resolve to "manual".

#### REQ: managed-no-overwrite

For a managed classification the package MUST NOT download, write, or replace
the executable under any option combination, including an explicit version pin
and a skipped confirmation.

#### REQ: managed-redirect-command

For a managed classification the package MUST report the detected manager's
display name and the exact upgrade command configured for it, and MUST treat the
run as a success rather than a failure.

#### REQ: latest-release-source

The package MUST determine the latest version from the configured GitHub
repository's published releases, considering only the newest release that is
neither a draft nor a prerelease. This stable-only rule governs the unpinned
path; an explicit pin bypasses it per
[REQ: pinned-exact-tag](#req-pinned-exact-tag).

#### REQ: undetermined-version

The consumer MUST be able to declare which version strings mean "this build
cannot say" (for example `dev` or `unknown`). Such a version MUST be reported as
undetermined rather than as up to date, and MUST NOT be treated as either newer
or older than a release. A Go pseudo-version is a known version, not an
undetermined one: it orders below its release per semver.

#### REQ: no-op-when-current

When the running version already equals the latest stable release, the package
MUST report that it is up to date and MUST NOT download or replace anything.

#### REQ: version-pin

The package MUST accept an exact release to install instead of the latest
stable, with the leading `v` optional so `v1.2.3` and `1.2.3` resolve to the
same release. A pinned install reuses the confirmation, verification, and
replace machinery of the unpinned path; only the target differs.

#### REQ: pinned-exact-tag

A version pin MUST resolve to exactly the named release whatever its prerelease
or draft status, and MUST fetch that release's own assets. Requesting an older
release MUST NOT download the assets of whatever release is currently latest.

#### REQ: pinned-downgrade-guard

When the pinned target is strictly lower than the running version, the package
MUST refuse unless downgrades were explicitly allowed, reporting both versions
so the consumer can name the flag that permits it, and MUST NOT modify the
binary. When downgrades are allowed the operation proceeds and the reported
transition MUST identify itself as a downgrade. When the running version is
undetermined the guard MUST NOT trigger, because direction cannot be
established.

#### REQ: pinned-unknown-tag

When the pinned tag has no published release, or that release carries no asset
for the host platform, the package MUST fail with an error naming the requested
tag and MUST leave the existing binary untouched.

#### REQ: download-matching-asset

For an eligible self-replace the package MUST download the release asset
matching the host operating system and architecture, named by the consumer's
asset-naming rule, defaulting to GoReleaser's
`<binary>_<version>_<os>_<arch>` convention.

#### REQ: checksum-before-extract

The package MUST verify the downloaded asset's sha256 against that release's
checksums file before extracting anything from it. On a mismatch, or a missing
or unfetchable checksum entry, it MUST abort and MUST NOT modify the existing
binary. Verification MUST precede extraction, not follow it.

#### REQ: atomic-replace

The executable MUST be replaced atomically: the verified binary is staged beside
the target, on the same filesystem, then renamed over it, so an interrupted or
failed operation leaves the original intact and runnable and never a partial or
truncated file.

#### REQ: post-swap-version-check

After a successful swap the package MUST confirm the installed binary reports
the expected version, using the version-probe arguments the consumer configured.
Because the swap has already succeeded, a failed confirmation is reported, not
treated as a failed update.

#### REQ: unsupported-platform

When the host platform is one the consumer publishes no asset for, the package
MUST refuse with a clear error before attempting any download or swap.

#### REQ: failure-leaves-working-binary

Every failure path — release lookup, download, checksum, staging, permission —
MUST leave the previously installed executable in place and runnable. No failure
mode may end with no working binary at the install location.

#### REQ: permission-failure-identifiable

When the replacement fails for lack of permission to write the install
location, the failure MUST be distinguishable from other failures by the caller,
and MUST carry the executable's path, so the consumer can print a remedy naming
the file.

### Consumer integration

The package is only reusable if everything a CLI already decided for itself
stays with that CLI.

#### REQ: consumer-configured-identity

All CLI identity MUST be supplied by the consumer: binary name, GitHub
repository, current version, the version strings meaning undetermined, the
package managers that own it with their display names, path markers and upgrade
commands, the asset and checksums naming rules, the version-probe arguments, and
the supported platforms. The package MUST NOT hard-code any one CLI's values.

#### REQ: host-owned-exit-codes

The package MUST NOT decide the process exit code. It MUST report outcomes and
typed failure kinds that let each consumer map them onto its own documented exit
codes — including consumers whose contracts disagree, such as one reserving a
dedicated code for "update available" and one folding it into a general findings
code.

#### REQ: no-io-side-effects-in-core

The core logic MUST NOT write to the terminal or read from it. Prompting,
formatting, and output format selection belong to the consumer or to the
optional command adapter, so the core can be used by a CLI with any output
convention.

#### REQ: non-interactive-refusal

When a self-replace would need confirmation, the consumer has not skipped it,
and no interactive terminal is attached, the operation MUST refuse rather than
block on input. A CLI driven by scripts and agents must never silently wait for
a keystroke.

#### REQ: check-states-the-next-step

A check-only report MUST state what to do about an available update, not only
that one exists: the manager's upgrade command for a managed install, the
self-update command itself for a manual one, and the manual-update guidance for
an ambiguous one. Machine-readable check output MUST carry the same facts —
the install method, and the manager and its upgrade command when there is one —
so a caller need not parse prose to reach the same conclusion. Classifying the
install reads no network and writes nothing, so this costs the read-only
guarantee nothing; a classification failure MUST NOT fail the check, which
still reports the version comparison. An up-to-date result MUST NOT print a
next step, because there is nothing to do.

#### REQ: cobra-adapter-optional

The package MUST provide an optional adapter that builds a ready-made
`self-update` command, exposing check, confirmation-skip, version-pin, and
allow-downgrade options and accepting the consumer's aliases and error mapping.
The core package MUST NOT depend on any command framework, so a CLI that does
not use that framework can still use it.

#### REQ: no-network-in-tests

The package's own tests MUST NOT require network access and MUST NOT replace a
real installed binary. The GitHub endpoints, filesystem operations, executable
resolution, and interactivity check MUST be injectable for that purpose.

#### REQ: dry-run

The package MUST support a dry run that walks the full decision path — detect,
resolve the target release, compare versions, evaluate the downgrade guard, and
determine the exact asset URL it would fetch — then stops before downloading or
writing anything, reporting what it would have done. Every consumer needs a way
to verify its own wiring without replacing a binary.

### Reference CLI

The module ships a small CLI whose only job is to be a real consumer of the
package. It exists because a library that updates binaries cannot be fully
proven by unit tests: something must actually download, verify and swap a real
executable, and it should be this repository's own binary rather than a
downstream CLI's users.

#### REQ: reference-cli-single-command

The module MUST provide a CLI exposing exactly one command, `self-update` (with
the `update` alias), built through the same public API a downstream consumer
uses. It MUST NOT reach into unexported internals, so anything the reference CLI
needs is by construction available to every consumer.

#### REQ: reference-cli-self-hosting

The reference CLI MUST be configured to update itself from this module's own
GitHub releases, and MUST report its own version through the same probe
arguments it configures. Updating it is therefore a genuine end-to-end exercise
of detection, download, verification, and replacement.

#### REQ: reference-cli-inspection

The reference CLI MUST be able to demonstrate and verify behavior without
modifying anything: a check mode, a dry run per [REQ: dry-run](#req-dry-run)
that prints the decided action and asset URL, machine-readable output, and a
mode that classifies an arbitrary supplied path so detection can be exercised
against layouts the host machine does not have.

#### REQ: reference-cli-released

The module MUST publish the reference CLI as release artifacts through its CI —
archives per supported platform plus a checksums file, following the same
GoReleaser naming the package defaults to. Without published artifacts the
reference CLI cannot update itself, and the release path itself would go
untested.

## Consumers

| CLI | Feature |
|---|---|
| `wb` | [sneat-dev/wb spec/features/self-update](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-update?op=explore) — three-code exit contract, Homebrew cask, `unknown` placeholder |
| `specscore` | [specscore/specscore-cli spec/features/cli/self-update](https://specscore.studio/app/github.com/specscore/specscore-cli/spec/features/cli/self-update?op=explore) — dedicated exit code 10, Homebrew/Scoop/WinGet, `dev` placeholder |

A consumer's Feature specifies only its configuration and its deviations; the
behavior above is inherited, not restated.

## Acceptance Criteria

### AC: managed-installs-are-redirected

**Requirements:** self-update#req:detect-managed, self-update#req:managed-no-overwrite, self-update#req:managed-redirect-command

**Given** a binary whose resolved path lies inside a configured manager's layout, reached through a symlink
**When** an update is requested, including with a version pin and with confirmation skipped
**Then** the package reports the manager and its upgrade command as a successful outcome, and performs no download, no write, and no replacement.

### AC: ambiguity-never-becomes-manual

**Requirements:** self-update#req:detect-manual, self-update#req:ambiguous-safe-default

**Given** a binary at a path matching neither a configured manager layout nor a plausible manual install location
**When** an update is requested
**Then** the package reports the install method as ambiguous and performs no download or replacement, while a `go install` or `bin` path is classified manual and is eligible.

### AC: only-verified-bytes-are-installed

**Requirements:** self-update#req:latest-release-source, self-update#req:download-matching-asset, self-update#req:checksum-before-extract, self-update#req:atomic-replace, self-update#req:post-swap-version-check, self-update#req:no-op-when-current

**Given** a manual install older than the latest stable release, where drafts and prereleases exist alongside it
**When** the update runs
**Then** the package selects the newest stable release, downloads the asset for the host platform, compares its sha256 against that release's checksums before extracting, swaps the binary atomically, and confirms the installed version — and when already current it reports so having downloaded nothing.

### AC: pins-resolve-exactly-and-guard-direction

**Requirements:** self-update#req:version-pin, self-update#req:pinned-exact-tag, self-update#req:pinned-downgrade-guard, self-update#req:pinned-unknown-tag, self-update#req:undetermined-version

**Given** a manual install and a pinned target release
**When** the pin is older than the running version, then the same pin with downgrades allowed, then a tag that was never published
**Then** the first refuses naming both versions, the second installs that exact release from its own asset URL and reports a downgrade, the third fails naming the requested tag — and an undetermined running version disables the direction guard rather than guessing.

### AC: no-failure-leaves-a-broken-install

**Requirements:** self-update#req:failure-leaves-working-binary, self-update#req:permission-failure-identifiable, self-update#req:unsupported-platform, self-update#req:non-interactive-refusal

**Given** a self-replace that fails at the release lookup, the download, the checksum comparison, the staging step, or the final write for lack of permission — or that is attempted on an unsupported platform, or without a terminal and without an explicit skip
**When** any of those occurs
**Then** the operation fails with a typed kind the caller can branch on, a permission failure carries the executable path, and the previously installed binary remains in place and runnable.

### AC: reference-cli-proves-it-end-to-end

**Requirements:** self-update#req:reference-cli-single-command, self-update#req:reference-cli-self-hosting, self-update#req:reference-cli-inspection, self-update#req:reference-cli-released, self-update#req:dry-run

**Given** the reference CLI, published as release artifacts by this module's CI
**When** a released copy of it is run with the dry run, with the check mode, with a supplied path to classify, and finally with the update applied
**Then** the dry run names the action and the exact asset URL without downloading, the check reports availability without writing, the supplied path is classified without the host having that layout, and the applied update really downloads, verifies and swaps the running binary, which then reports the new version — all through the same public API a downstream CLI uses.

### AC: two-cli-contracts-coexist

**Requirements:** self-update#req:consumer-configured-identity, self-update#req:host-owned-exit-codes, self-update#req:no-io-side-effects-in-core, self-update#req:check-states-the-next-step, self-update#req:cobra-adapter-optional, self-update#req:no-network-in-tests

**Given** two consumers whose exit-code contracts disagree — one reserving a dedicated code for "update available", one folding it into a general findings code
**When** both build their command from this package
**Then** each keeps its own exit codes, output format, managers, and version placeholder with no CLI-specific value hard-coded in the package, and the package's own test suite exercises all of it without network access or replacing a real binary.

## Open Questions

- Should the package offer a cached background availability check, so a CLI can
  mention a stale binary during unrelated commands without adding a network call
  to every run?
- Should signature verification (minisign or cosign) sit alongside the sha256
  check for consumers that publish signatures, or does that belong to a separate
  package?

---
*This document follows the https://specscore.md/feature-specification*
