---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: Self-Update Library

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/self-update?op=explore) | [Edit](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/self-update?op=edit) | [Ask question](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/self-update?op=ask) | [Request change](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/self-update?op=request-change) |
**Status:** Stable
**Source Ideas:** —

## Summary

`github.com/strongo/cli-helpers/selfupdate` lets any Go CLI update its own binary in place.
It decides how the running binary was installed: a package-manager-owned install
is never overwritten directly — the caller is told the exact upgrade command by
default, or may explicitly opt in to run that manager command as structured argv
— while a manual install (release archive, `go install`) is replaced by
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
symlinked shim resolves to its real location. A managed classification MUST
route to that manager's configured policy and MUST NOT self-replace.

#### REQ: system-package-dirs-are-managed

The package MUST classify a resolved executable path as package-managed, using a
built-in redirect-only "system package manager", when it lies inside the host
OS's own package-manager directories — `/usr/bin`, `/usr/sbin`, `/usr/lib`,
`/usr/lib64`, `/usr/libexec`, `/usr/share`, `/bin`, `/sbin`, `/lib`, `/lib64`,
`/nix/store`, `/run/current-system` on Linux and other Unix; `/usr/bin`,
`/usr/sbin`, `/usr/libexec`, `/bin`, `/sbin`, `/System`, `/nix/store` (nix-darwin),
`/run/current-system` (nix-darwin) on macOS, which has no bare `/lib` the way
Linux does — its C libraries live under `/usr/lib` and `/System` instead, so
there is no separate `/lib` entry to list for it; and the environment-derived
`%SystemRoot%`, `%ProgramFiles%`, `%ProgramFiles(x86)%` on Windows — regardless
of the consumer's configured `Managers`, and boundary-aware so a sibling
directory whose name merely starts with the same characters (for example
`/usr/binx`) never matches. Each Windows value MUST be normalized before it
anchors that boundary match: a trailing separator MUST be stripped, and a
value that is nothing but a bare drive root (`C:` or `C:\`, once trimmed) MUST
be skipped entirely — an unstripped trailing separator would silently break
the match, and treating a whole drive as a system directory would be a
catastrophic false positive from a misconfigured or unusual environment.

`/usr/local/**` and any path under the user's home directory (including
`~/go/bin`) are explicitly excluded: both are the conventional locations for
software placed by hand, not package-manager-owned ones, and a Homebrew
Intel-Mac prefix under `/usr/local` is already recognized through
`Manager.PathMarkers`. `/opt/**` is excluded too, but as a deliberate
trade-off rather than a certainty: the Filesystem Hierarchy Standard defines
`/opt` for add-on application software, so a vendor's own `.deb`/`.rpm` MAY
legitimately install there and a path under it is not reliably a hand-placed
manual install the way one under `/usr/local` or `$HOME` is. It is excluded
anyway because manual/tarball installs commonly live under `/opt` too, and
there is no way to tell the two apart from the path alone; a consumer whose
catalog target really is `/opt`-installed by a package manager should declare
that manager's own `Manager.PathMarkers` instead of relying on this built-in,
path-only check.

Files under these directories are tracked by the OS package manager's own
database (dpkg/apt, rpm/dnf, pacman including the AUR, apk, or the Nix store).
Overwriting one does not typically make the manager refuse anything: the next
ordinary upgrade of that package (`apt upgrade`, `dnf upgrade`, `pacman -Syu`)
silently overwrites it again with the package's own bytes — undoing the
self-update without any error — while `dpkg --verify`, `rpm -V`, and
`pacman -Qkk` report the file as modified against what the manager's database
recorded in the meantime. The Nix store (`/nix/store`) is the same rule under
a different mechanism: it is content-addressed and nothing but Nix is meant to
write into it. Only NixOS mounts it read-only; elsewhere root (or the owning
user of a single-user install) can overwrite a file, which breaks the store
path's content hash and makes `nix-store --verify --check-contents` report it
as corrupted. This is the same principle the package
already applies to Homebrew, Scoop, WinGet, and Snap: never overwrite a
manager-owned install in place, redirect to that manager's own upgrade command
instead. On a typical multi-user install these directories are writable only
by root (Administrator/TrustedInstaller on Windows), so a process able to
replace a file there is normally running elevated, and doing so means
replacing a file from the distribution's signed package channel with one
downloaded outside it, as the machine's most privileged user — except a
single-user Nix install, where the store belongs to the user and the reason to
refuse is the store's content-addressing, not file ownership. This
package's own installers never write into these directories in the first
place, so any copy found inside one was placed by something else — almost
always the OS's own package manager.

The package's list of these directories, `SystemPackageDirs(goos, getenv)`, MUST
be the single exported source of truth a consumer's own install/destination
denylist reuses rather than duplicates.

Because the built-in manager cannot know WHICH package manager actually owns a
given file, it MUST carry no single `UpgradeCommand`; the redirect prose MUST
instead be carried in a separate `Manager.UpgradeHint` field, naming the host
OS's own real tooling family (POSIX package managers on Linux/other Unix,
`macOS`/System Integrity Protection on macOS, Windows Update/the original
installer on Windows) rather than one fixed string for every OS. A renderer
MUST NOT print an empty `UpgradeCommand` after a "Run:"-style prefix; when
`UpgradeCommand` is empty and `UpgradeHint` is set, it MUST render the hint as
a natural sentence instead (`"<binary> is managed by <name>. Update it with
<hint>."`), never as `"Run: <hint>"`. This applies to every manager, not only
the built-in one: a WinGet machine-scope install (`%ProgramFiles%\WinGet\...`,
which a machine-scope `winget install --scope machine` produces) would
otherwise fall through to this built-in check and its generic Windows Update
hint — `WinGet`'s own `PathMarkers` MUST also match that machine-scope layout
(directly under `%ProgramFiles%`, without the per-user location's
`Microsoft\` segment) so it is instead recognized as WinGet-managed and
redirects to `winget upgrade`.

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

For a managed classification the package MUST NOT download, write, or directly
replace the executable under any option combination. It MAY invoke the owning
package manager only when the consumer explicitly configured executable argv;
otherwise it MUST redirect. A skipped confirmation MUST NOT turn a redirect-only
manager into an executable one.

#### REQ: managed-redirect-command

For a managed classification without executable argv, the package MUST report
the detected manager's display name and the exact upgrade command configured for
it, and MUST treat the run as a success rather than a failure.

#### REQ: managed-executable-command

A consumer MAY opt a manager into execution by configuring one executable and
argument vector, or an ordered sequence of executable/argument-vector steps,
separately from its human-readable upgrade command. The package MUST pass every
step directly to a consumer-supplied command runner and MUST NOT parse or invoke
the display command through a shell. The normal path MUST confirm once before
execution unless confirmation was explicitly skipped, MUST stream every step's
output through the command adapter, MUST stop at the first failed step, and MUST
fail with a typed manager-command failure identifying that step when the process
cannot start or exits unsuccessfully.

A dry run MUST report the exact display command without invoking the runner. An
explicit version pin MUST be refused with a typed failure because a generic
package-manager upgrade cannot promise an arbitrary historical release. After a
successful manager command sequence, the adapter MUST probe the CLI found on
`PATH` with the configured version arguments. When the latest stable release is
known, the probe output MUST contain that exact normalized version; non-empty
output from an older installed build is a failed probe. A failed probe is a
warning because the manager command already completed.

#### REQ: managed-availability-report

For an unpinned managed update, the package MUST make one bounded, advisory
lookup of the latest stable release and retain the normalized current/latest
comparison in every redirect, dry-run, declined, and completed manager outcome.
The adapter MUST report those values before confirmation or manager execution.
If that lookup fails, it MUST report the normalized current version and that the
latest release is unavailable, retain a distinct warning in the outcome, and
still redirect or execute the configured manager command. The lookup MUST NOT
make the manager-owned install eligible for direct replacement, infer that the
manager installed the GitHub release, or skip a manager upgrade merely because
the GitHub version equals the running version.

The framework-neutral adapter MUST render the same compact ASCII preview for
manual and managed updates, including Current/Latest (or pinned Target), install
method, and a manager command when applicable. Colors are terminal-aware and
MUST be absent for non-terminal, `TERM=dumb`, or `NO_COLOR` output; JSON stdout
remains exactly one unstyled document, with preview and warning progress on
stderr.

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

#### REQ: ahead-of-latest

When the running version is known and orders strictly above the latest stable
release — a Go pseudo-version after the newest tag, or a build from a newer
version line than the releases — the package MUST report a distinct `ahead`
verdict carrying both versions. An unpinned update MUST then act as for
[REQ: no-op-when-current](#req-no-op-when-current): no download, replacement,
confirmation or manager command. A check-only report MUST NOT treat `ahead` as
an available update, so a consumer's "update available" mapping is not invoked.
This governs only the unpinned path: an explicit pin below the running version
is still decided by [REQ: pinned-downgrade-guard](#req-pinned-downgrade-guard)
and the allow-downgrade option, unchanged.

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

#### REQ: multi-product-repository

The consumer MUST be able to declare a tag prefix identifying which releases
belong to its binary, so one repository can publish several products. When a
prefix is declared, latest-stable resolution MUST ignore every release whose
tag does not carry it, a pin MUST resolve within that product's releases only,
and the version compared against the running build MUST be what remains after
the prefix and an optional leading `v` — the running binary reports a bare
version and cannot be expected to know the publisher's tag scheme.

The release path and the asset name come from different halves of that tag: the
download URL MUST use the full published tag, while the asset name MUST use the
bare version. Conflating them yields a URL that resolves to a real release and
a filename that exists nowhere, which is a 404 at download time rather than an
error at resolution time.

An empty prefix MUST behave exactly as a single-product repository does.

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

The same exact-version rule applies after a package-manager update whenever the
managed availability lookup resolved a stable target. A manager that exits zero
while stale metadata retains an older binary MUST produce a post-update warning,
not an unqualified verified-version claim. Verification MUST inspect executable
candidates owned by the detected manager rather than accepting an unrelated
development binary that appears earlier on `PATH`. A successfully verified
manager update MUST retain the exact absolute invocation path and resolved path
that passed the probe.

#### REQ: shell-command-cache-refresh

After a completed manual replacement or executable package-manager update, the
framework-neutral text and JSON adapters MUST explain that a parent shell may
still cache the previously resolved command and MUST provide an explicit
`hash -r` or new-shell remedy. The updater runs as a child process and MUST NOT
claim that it refreshed its parent shell's command cache.

#### REQ: after-update-integration

`Options` and the optional Cobra command adapter MUST accept the same typed,
optional after-update callback. The callback MUST run only for a completed
manual replacement, an already-current result, or a completed executable
package-manager update; it MUST NOT run for a check, dry run, declined update,
redirected manager, or failed update. It MUST receive the completed outcome and
an absolute executable identity suitable for reexecution. For a
package-manager update, the identity MUST be the same manager-owned executable
that passed the exact-version probe. The callback MUST NOT perform or depend on
a second `PATH` lookup, and MUST NOT run when no manager-owned executable passed
verification, so a manager-owned version-directory or shim change cannot select
a stale or shadowing binary.

#### REQ: after-update-integration-nonfatal

Failure to resolve the installed executable or an error returned by the
after-update callback MUST be retained as a distinct outcome warning. It MUST
NOT turn a completed binary update into a failed update. The framework-neutral
and Cobra output adapters MUST keep machine-readable stdout valid and expose the
warning separately in text stderr and JSON.

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
formatting, output format selection, and process I/O for executable manager
commands belong to the consumer or to the optional command adapter, so the core
can be used by a CLI with any output convention.

#### REQ: non-interactive-refusal

When a self-replace or executable manager command would need confirmation, the
consumer has not skipped it, and no interactive terminal is attached, the
operation MUST refuse rather than block on input. A CLI driven by scripts and
agents must never silently wait for a keystroke.

#### REQ: check-states-the-next-step

A check-only report MUST state what to do about an available update, not only
that one exists: the self-update command for an executable managed install, the
manager's upgrade command for a redirect-only managed install, the self-update
command itself for a manual one, and the manual-update guidance for an ambiguous
one. Machine-readable check output MUST carry the same facts — the install
method, the manager and its upgrade command or upgrade hint (whichever is set
— see self-update#req:system-package-dirs-are-managed) when there is one, and
whether that manager is executable through self-update — so a caller need not
parse prose to reach the same conclusion. Classifying the
install reads no network and writes nothing, so this costs the read-only
guarantee nothing; a classification failure MUST NOT fail the check, which
still reports the version comparison. An up-to-date result MUST NOT print a
next step, because there is nothing to do.

#### REQ: framework-neutral-cli-helpers

The prompting, refusal, and output-formatting a consuming CLI needs — the
confirmation gate including the non-interactive refusal, the terminal check,
the text and machine-readable writers, and the next-step and ambiguous-install
guidance — MUST be reusable without any command framework. A CLI that uses no
framework at all MUST be able to reach the same behavior a framework adapter
gives, rather than reimplementing it; that reimplementation is precisely the
duplication this package exists to remove, and it is where the subtle rules
live (a character device is not a terminal; an empty read is a refusal, not a
decline).

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

#### REQ: update-at-classified-copy

`Config` MUST expose the update path for a copy the caller has already
classified and whose latest release it has already resolved:
`UpdateAt(ctx, detection, opts)`, where `detection.Path` is the symlink-resolved
file to replace and `CurrentVersion` is that copy's version. `Update` MUST be
exactly `DetectSelf` followed by `UpdateAt`. `Options` MUST accept a resolved
release tag; when it is set, neither the manual nor the managed path performs
its own latest-release lookup, and the update MUST fail with the release-lookup
failure kind, changing nothing, if that tag is no longer the latest stable
release, so a caller that confirmed one version never installs another.
`Config` MUST expose the latest-stable-release lookup the unpinned path uses, so
a caller can resolve the tag once and pass it on.

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
| `ovdb` | `openvaultdb/ovdb` — Homebrew cask execution through structured argv; still on the standalone `github.com/strongo/selfupdate` module until the [cli-install plan](../../plans/cli-install/README.md) migrates it |
| `wb` | [sneat-dev/wb spec/features/self-update](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-update?op=explore) — three-code exit contract, Homebrew cask, `unknown` placeholder |
| `specscore` | [specscore/specscore-cli spec/features/cli/self-update](https://specscore.studio/app/github.com/specscore/specscore-cli/spec/features/cli/self-update?op=explore) — dedicated exit code 10, Homebrew/Scoop/WinGet, `dev` placeholder |
| `chatwright` | [chatwright/cli spec/features/self-update](https://specscore.studio/app/github.com/chatwright/cli/spec/features/self-update?op=explore) — redirect-only Homebrew, `--check` informational |
| `codegrapher` | `code-grapher/codegrapher` — Homebrew cask execution, flat `checksums.txt`, skills re-exec after update |
| `cover100` | `sneat-dev/cover100-cli` — no package managers, plain release archives |
| `synchestra` | `synchestra-io/synchestra` — planned: migrates from `github.com/strongo/selfupdate` per the [cli-install plan](../../plans/cli-install/README.md); `cli-` tag prefix on a mirror releases repository |
| `ingitdb` | `ingitdb/ingitdb-cli` — planned: replaces its internal copy per the [cli-install plan](../../plans/cli-install/README.md) |
| `datatug` | `datatug/datatug-cli` — planned: gains self-update per the [cli-install plan](../../plans/cli-install/README.md) |
| `synchestra-channel` | `synchestra-io/synchestra-servers` — planned: migrates from `github.com/strongo/selfupdate` per the [cli-install plan](../../plans/cli-install/README.md); `servers-` tag prefix, flat `checksums.txt` |
| `synchestra-vm-host` | `synchestra-io/synchestra-vm` — planned: migrates from `github.com/strongo/selfupdate` per the [cli-install plan](../../plans/cli-install/README.md); `vm-` tag prefix, flat `checksums.txt` |

A consumer's Feature specifies only its configuration and its deviations; the
behavior above is inherited, not restated.

## Acceptance Criteria

### AC: managed-installs-use-only-the-configured-manager-policy

**Requirements:** self-update#req:detect-managed, self-update#req:managed-no-overwrite, self-update#req:managed-redirect-command, self-update#req:managed-executable-command

**Given** a binary whose resolved path lies inside a configured manager's layout, reached through a symlink
**When** an update is requested first with a redirect-only manager, then with executable argv, then as a dry run and with a version pin
**Then** redirect-only reports the manager command without executing it; executable mode confirms and invokes every configured program and argv in order without a shell, stops on the first failure, streams its output, and proves the installed CLI reports the exact known target version; dry-run reports but does not execute; the pin is refused; and no branch downloads, writes, or directly replaces the manager-owned executable.

### AC: system-directories-redirect-without-a-configured-manager

**Requirements:** self-update#req:system-package-dirs-are-managed, self-update#req:managed-no-overwrite, self-update#req:managed-redirect-command

**Given** a binary resolved inside an OS package-manager directory such as `/usr/bin`, with no `Managers` configured, and sibling paths that merely resemble one, such as `/usr/binx`, `/usr/local/bin`, or `/opt/tool/bin`
**When** an update is requested
**Then** the system-directory copy is classified managed by the built-in system package manager and redirected — its `UpgradeCommand` empty and `UpgradeHint` naming that OS's own real tooling, never a wrong-OS example, rendered as a natural sentence and never as `"Run: "` followed by prose — with no download, write, or replacement, while the sibling paths remain classified manual or ambiguous exactly as they were before this check existed, and a WinGet machine-scope install under `%ProgramFiles%` is recognized as WinGet-managed rather than falling through to this built-in check.

### AC: upgrade-classifies-a-symlink-identically-to-self-update

**Requirements:** self-update#req:system-package-dirs-are-managed, cli-install#req:self-update-equals-upgrade-self

**Given** a symlink whose unresolved PATH entry sits in a system directory but whose real file resolves to a manual `/opt` install, and the mirror case (a manual-looking unresolved entry resolving into a system directory)
**When** `self-update` (via `DetectSelf`, which classifies only the resolved path) and `upgrade <self>`/a non-host target's classification (which checks the unresolved PATH entry against manager markers only, then classifies the resolved path in full) both run
**Then** both reach the identical install-method verdict for the identical binary in either direction — the file's actual resolved location decides, never which symlink happened to point at it.

### AC: ambiguity-never-becomes-manual

**Requirements:** self-update#req:detect-manual, self-update#req:ambiguous-safe-default

**Given** a binary at a path matching neither a configured manager layout nor a plausible manual install location
**When** an update is requested
**Then** the package reports the install method as ambiguous and performs no download or replacement, while a `go install` or `bin` path is classified manual and is eligible.

### AC: only-verified-bytes-are-installed

**Requirements:** self-update#req:latest-release-source, self-update#req:ahead-of-latest, self-update#req:update-at-classified-copy, self-update#req:multi-product-repository, self-update#req:download-matching-asset, self-update#req:checksum-before-extract, self-update#req:atomic-replace, self-update#req:post-swap-version-check, self-update#req:shell-command-cache-refresh, self-update#req:after-update-integration, self-update#req:after-update-integration-nonfatal, self-update#req:no-op-when-current

**Given** a manual install older than the latest stable release, where drafts and prereleases exist alongside it
**When** the update runs
**Then** the package selects the newest stable release, downloads the asset for the host platform, compares its sha256 against that release's checksums before extracting, swaps the binary atomically, and confirms the installed version — and when already current, or when the running build is ahead of the latest stable release, it reports so having downloaded nothing and a check does not signal an update; the same outcome is reached through `UpdateAt` on a classified copy with a pre-resolved tag, and a tag that is no longer latest fails without changes.

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

**Requirements:** self-update#req:consumer-configured-identity, self-update#req:host-owned-exit-codes, self-update#req:no-io-side-effects-in-core, self-update#req:check-states-the-next-step, self-update#req:framework-neutral-cli-helpers, self-update#req:cobra-adapter-optional, self-update#req:no-network-in-tests

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
