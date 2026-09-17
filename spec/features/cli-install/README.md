---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: CLI Install Command Library

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=explore) | [Edit](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=edit) | [Ask question](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=ask) | [Request change](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Lets any fleet CLI list, explain, and install the other fleet CLIs relevant to it, mirroring how the host itself was installed.

`github.com/strongo/cli-helpers/cliinstall` gives every fleet CLI an `install`
command. `<cli> install` lists the fleet CLIs relevant to the host, each with its
installed status (version, release date, commit), a one-line description and a
one-line explanation of why it is useful to someone who uses the host.
`<cli> install <name>...` shows the long description and relevance of each named
CLI, confirms once, and installs them the same way the host was installed: by
`brew install --cask` when the host is Homebrew-managed and the target publishes
a cask, otherwise by downloading the target's GitHub release asset, verifying its
sha256 and writing it atomically next to the host. The download, verification and
atomic write are the [Self-Update Library](../self-update/README.md)'s own
machinery, reused rather than re-specified.

The catalog of installable CLIs, their identities and the host → target
relevance texts are compiled into this module, so each CLI ships the catalog it
was built with. Installed status comes from a new fleet-wide `version --json`
contract that all nine CLIs implement, with a text fallback for older builds.

This repository owns the behavior contract. Each consuming CLI carries a thin
Feature that points here and specifies only its own configuration and deviations
— see [Consumers](#consumers).

## Problem

The fleet's CLIs are designed to be used together — DataTug explores inGitDB and
OpenVaultDB data, Synchestra coordinates work specified with SpecScore, CodeGrapher
integrates with `wb` worktrees — but nothing inside any of them says so. A user
of one tool learns that a sibling exists only by reading a README, then has to
find its install instructions, pick a method consistent with how they installed
the first tool, and check whether they already have it and which build.

Every CLI already carries the hard part of installing a binary safely: its
self-update command resolves releases, verifies checksums, swaps atomically and
refuses to fight a package manager. Installing a *different* CLI is the same
problem with a different identity and a destination that does not exist yet. If
each CLI adds its own `install` verb, the fleet repeats the self-update story —
nine copies, nine exit-code philosophies, one reviewed.

Two further gaps block a shared implementation today:

- There is no machine-readable way to ask an installed CLI for its build
  identity. Only `wb version --json` exists; everyone else prints
  `<name> <version> (<commit>) <date>` text, and stale builds print other shapes.
- Three CLIs are not on the shared self-update package at all (ingitdb has an
  internal copy whose per-OS checksum names no longer match its releases; ovdb
  and synchestra pin the retired `github.com/strongo/selfupdate` module) and
  datatug has no self-update. A shared install that only some hosts can use
  would leave the fleet split.

## Behavior

### Catalog

#### REQ: catalog-compiled-in

The module MUST carry the catalog of installable CLIs as typed Go source in the
`cliinstall` package, compiled into every host. Listing, details and relevance
MUST NOT require network access or any file outside the host binary. A host sees
exactly the catalog of the `cli-helpers` version it was built with; catalog
changes reach users through the host's normal update.

#### REQ: catalog-entry-identity

Each catalog entry MUST carry: a stable id equal to the binary name; the
release identity in the Self-Update Library's `Config` shape (GitHub repository,
tag prefix, supported platforms, asset and checksums naming, version-probe
arguments, and the package managers that own it with their path markers); the
Homebrew cask token to install it with, or none; a homepage URL; a one-line
description; and a longer details text of at most a few short paragraphs.

#### REQ: catalog-identity-single-source

An entry MUST be able to produce the `selfupdate.Config` for its CLI given only
the running version, so a host's `self-update` and every other host's
`install <that cli>` resolve releases identically. A host MAY override fields
its own self-update needs beyond the catalog (for example executable manager
upgrade argv or a Scoop/WinGet manager) but MUST NOT change the repository, tag
prefix, asset naming or checksums naming away from its catalog entry.

#### REQ: relevance-matrix

The catalog MUST carry, for each host id, an ordered list of relevant target ids
each with a relevance text written for that pair: why a user of the host would
want the target and what they would do with the two together. Relevance texts
MUST be specific to the pair — the same sentence MUST NOT be reused for two
pairs — and MUST NOT claim an integration that does not exist. The initial
matrix is:

| Host | Relevant targets, in listing order | Why (one line per pair; the catalog carries the full text) |
|---|---|---|
| `wb` | `specscore`, `codegrapher`, `cover100` | specscore: lint and query the specs of every repository `wb` syncs · codegrapher: fleet-aware code graph that understands `wb` canonical clones and worktrees · cover100: turn the coverage `wb` gates on into a navigable per-repository treemap |
| `specscore` | `wb`, `ingitdb`, `synchestra`, `chatwright`, `codegrapher` | wb: run `specscore spec lint` across every synced clone with one `wb` recipe · ingitdb: keep structured spec-adjacent data as schema-validated records in Git · synchestra: turn SpecScore features and plans into coordinated agent tasks · chatwright: specify conversational behavior and prove it with deterministic scenario runs · codegrapher: trace source symbols back to the SpecScore artifacts that require them |
| `chatwright` | `wb`, `specscore` | wb: keep scenario repositories and the CLIs under test synced across a fleet · specscore: specify the conversational features that scenarios verify |
| `codegrapher` | `wb`, `specscore` | wb: create the worktrees whose deltas codegrapher overlays on a canonical graph · specscore: author the specs codegrapher's traceability links point to |
| `cover100` | `wb`, `codegrapher` | wb: collect coverage across many repositories and gate it in CI · codegrapher: see which files an uncovered area is most depended on by |
| `ovdb` | `ingitdb`, `datatug`, `wb` | ingitdb: inspect and validate an OpenVaultDB database stored on the inGitDB engine directly · datatug: explore and query the underlying data in a UI · wb: keep the vault and engine repositories synced |
| `synchestra` | `ingitdb`, `datatug`, `ovdb`, `chatwright`, `specscore`, `cover100`, `wb` | ingitdb: keep the structured data your specified work needs as schema-validated records in Git, next to Git-held coordination state · datatug: explore and query that data · ovdb: give data that agents produce a user-owned, portable database · chatwright: verify conversational agents a task delivered · specscore: lint the specs Synchestra dispatches from · cover100: check a delivered task reached full coverage · wb: land and clean up the branches agents produce |
| `ingitdb` | `datatug`, `ovdb`, `wb` | datatug: explore and query inGitDB collections in a UI · ovdb: serve the same Git-stored data through OpenVaultDB's portable database API · wb: sync the repositories that hold your databases |
| `datatug` | `ingitdb`, `ovdb`, `specscore`, `wb` | ingitdb: create, validate and edit the Git databases DataTug reads · ovdb: keep user-owned, portable OpenVaultDB databases alongside the data DataTug explores · specscore: keep DataTug project and query definitions specified · wb: sync many DataTug project repositories at once |

A target MUST NOT be relevant to itself. `wb` MUST be relevant to every other
host.

#### REQ: catalog-validated

The module's tests MUST prove, without network access, that every relevance
entry names a catalog id, no host lists itself or lists a target twice, every
catalog CLI appears as a host and as someone's target, `wb` is relevant from
every other host, no relevance text is duplicated, and each entry's asset and
checksums names match a recorded snapshot of that CLI's most recent real release
asset list for every supported platform. A naming rule that would produce a
file the release does not publish MUST fail the tests, not a user's install.

### Version JSON contract

#### REQ: version-json-contract

Every catalog CLI MUST implement `<cli> version --json`, printing exactly one
JSON object and nothing else to stdout and exiting 0. The object MUST contain
these string keys:

| Key | Value |
|---|---|
| `name` | the binary name, equal to its catalog id |
| `version` | bare version without a leading `v`; `dev` when the build cannot determine it |
| `commit` | full commit SHA, with `+dirty` when built from a modified tree; `""` when unknown |
| `date` | RFC 3339 build timestamp — for a released build, the release build time; `""` when unknown |

Additional keys are permitted and MUST be ignored by readers. Keys MUST NOT be
removed or change meaning; the contract evolves only additively. For CLIs wired
through `github.com/strongo/buildinfo`, the `version` subcommand that module
builds is the implementation point; a CLI with its own `version` command (today
`wb`, and `chatwright`, whose `version` also prints runtime and SDK versions)
MUST add these keys to its own output.

#### REQ: version-json-side-effect-free

`version --json` MUST NOT perform network I/O, write or create files, start
daemons, run update checks, or emit telemetry, so that probing an installed CLI
is safe to repeat and to run against every catalog entry at once.

### Status

#### REQ: status-locate

For each target the library MUST locate an installed copy — an executable
file, not merely a file with the right name — by resolving the binary name (with
the platform executable suffix) on `PATH`, then in the host executable's
resolved directory, then in an explicitly supplied `--dir`. The
copy `PATH` resolves is the reported one; a different copy found only in the
host directory or `--dir` MUST be reported as installed with a warning that it
is not on `PATH`. Status MUST classify the located path with the target's own
managers using the Self-Update Library's classification
([REQ: detect-managed](../self-update/README.md#req-detect-managed),
[REQ: ambiguous-safe-default](../self-update/README.md#req-ambiguous-safe-default)),
reporting the install method and manager.

#### REQ: status-probe-order

For a located copy the library MUST determine its build identity by the first
step that succeeds: (1) `version --json` yielding an object per
[REQ: version-json-contract](#req-version-json-contract); (2) `version` whose
first line is `<name> <version> (<commit>) <date>`, tolerating an `@` before the
date and the `wb`-style multi-line `revision:`/`built:` form; (3) `--version`
printing a single version token, which yields a version with no commit or date.
A step whose reported name differs from the target id MUST be discarded, so a
stale or mis-stamped binary is not reported under another CLI's identity. When
every step fails the target is reported installed with version `unknown` and a
probe warning. Status output MUST record which step produced the identity.

#### REQ: status-probe-bounded

Probing MUST run the located executable directly without a shell, with empty
stdin, `NO_COLOR=1`, and a deadline of 3 seconds per invocation after which the
process is killed and the step counts as failed. Targets MAY be probed
concurrently, at most four at a time. Probing MUST NOT write, move or delete any
file and MUST NOT make network requests of its own.

### Listing and details

#### REQ: list-relevant

`<cli> install` with no names MUST list the host's relevant targets in matrix
order. Each row MUST show the id, the status (`installed` or `not installed`),
and for an installed copy its version, release date (the build date, date part
only in text), short commit (7 characters in text), install method and path;
then the one-line description and the one-line relevance. A final line MUST
tell the user how to see details and install: `<cli> install <name>`.

#### REQ: list-all

`--all` MUST list every catalog entry other than the host, relevant targets
first in matrix order, then the rest alphabetically; a row with no relevance
text for this host MUST say so instead of inventing one.

#### REQ: list-offline-read-only

Listing MUST make no network requests and modify nothing, so it is safe in
scripts, CI and agent sessions and fast enough to run as a first command.
Showing the latest available release per target is out of scope for listing.

#### REQ: machine-readable-output

With `--format json`, listing, details, dry runs and install results MUST write
exactly one JSON document to stdout, with progress and warnings on stderr. Every
fact shown in text MUST be present in JSON with full values (full commit, full
timestamp): `host`, and per target `name`, `relevant`, `description`,
`details` (details and install only), `relevance`, `status`, `version`,
`commit`, `date`, `path`, `install_method`, `manager`, `version_source`
(`version_json`, `version_text`, `version_flag`, or `unknown`) and `warnings`.

#### REQ: details-before-install

`<cli> install <name>...` MUST first print, for each named target, its
description, details, homepage, relevance to the host, current status, and the
planned action — the exact `brew` command, or the release version, asset URL and
destination path. Details MUST be printed before any confirmation prompt.

#### REQ: non-relevant-target-allowed

A catalog target that is not relevant to the host MUST still be installable. Its
details MUST state that it is not listed as relevant to the host instead of
showing a relevance text.

#### REQ: unknown-target-refused

A name that is not a catalog id MUST fail before any confirmation, network
request or write, with a typed failure naming the unknown name and listing the
valid ids.

#### REQ: already-installed-no-op

A target whose status is installed MUST NOT be downloaded, reinstalled or
replaced. The command MUST report the installed version, date and commit and
name `<target> self-update` as the way to update it, and MUST count that target
as a success. Naming the host itself is reported the same way, pointing at the
host's own `self-update`.

### Installing

#### REQ: install-method-mirrors-host

The install method for each target MUST be chosen without prompting, in this
order: (1) when `--dir` is given, direct release install into that directory;
(2) when the host classifies as managed by Homebrew and the target has a
Homebrew cask token, Homebrew; (3) when the host classifies as manual, direct
release install into the host executable's resolved directory; (4) otherwise —
a host managed by Homebrew for a target without a cask, a Scoop or WinGet host,
or an ambiguous host — direct release install into the first `PATH` directory
that classifies as manual and is writable by the current user, determined
without creating files. When step 4 finds no directory, the target MUST fail
with a typed failure whose message names `--dir`. A manager-owned directory
MUST never be chosen as a direct-install destination.

#### REQ: homebrew-cask-install

A Homebrew install MUST run `brew install --cask <token>` as structured argv
through the Self-Update Library's managed command runner, never through a shell,
streaming its output, after confirmation. The user naming a target and
confirming is the explicit opt-in that
[REQ: managed-executable-command](../self-update/README.md#req-managed-executable-command)
requires; a host MAY instead configure print-only, in which case the command is
reported and the target counts as redirected, not failed. `brew` not resolvable
on `PATH`, or exiting non-zero, MUST fail that target with the managed-command
failure kind.

#### REQ: direct-release-install

A direct install MUST resolve the target's latest stable release per
[REQ: latest-release-source](../self-update/README.md#req-latest-release-source)
and [REQ: multi-product-repository](../self-update/README.md#req-multi-product-repository),
and download, verify and write it by the same code and rules as a self-replace:
[REQ: download-matching-asset](../self-update/README.md#req-download-matching-asset),
[REQ: checksum-before-extract](../self-update/README.md#req-checksum-before-extract),
[REQ: atomic-replace](../self-update/README.md#req-atomic-replace) (staged in the
destination directory, then renamed into place),
[REQ: unsupported-platform](../self-update/README.md#req-unsupported-platform),
and [REQ: permission-failure-identifiable](../self-update/README.md#req-permission-failure-identifiable).
Pinning a target version is not offered; the installed target's own
`self-update --version` covers that.

#### REQ: release-lookup-depth

Release resolution MUST request GitHub's maximum page size of 100 releases, so
a target whose tag prefix shares a repository with frequently released products
(synchestra's `cli-` releases beside `servers-` releases) still resolves while
its newest stable release is among the repository's newest 100. This applies to
self-update as well, because both use one resolver.

#### REQ: install-never-overwrites

A direct install MUST refuse, with a typed failure naming the path, when any
file already exists at the destination path, including one the status probe
could not identify. A failed install MUST leave nothing at the destination and
no staging file behind.

#### REQ: post-install-verification

After a successful install the library MUST probe the installed target per
[REQ: status-probe-order](#req-status-probe-order) and confirm it reports the
installed release version; for Homebrew the resolved `PATH` copy is probed. A
failed confirmation, a destination directory not on `PATH`, or a `PATH` lookup
that resolves to a different copy MUST be reported as warnings with a remedy,
not as a failed install. The result MUST carry the
[REQ: shell-command-cache-refresh](../self-update/README.md#req-shell-command-cache-refresh)
remedy.

#### REQ: confirmation-gate

After all details are printed, the command MUST ask one confirmation covering
every target that would be installed; `--yes` skips it. Targets that are
already installed or unknown are excluded from the question. Without `--yes`
and without an interactive terminal the command MUST refuse per
[REQ: non-interactive-refusal](../self-update/README.md#req-non-interactive-refusal)
before any network request beyond what details needed, or any write. A declined
confirmation installs nothing and is not a failure.

#### REQ: install-dry-run

`--dry-run` MUST walk the full decision path — status, method, release
resolution, asset URL and destination, or the exact `brew` command — and report
it without invoking `brew`, downloading assets or writing anything, and without
asking for confirmation.

#### REQ: multi-target-batch

`install a b ...` MUST process targets in the order given, de-duplicated, and
MUST attempt every confirmed target even when an earlier one fails. The outcome
MUST carry one result per named target — installed, already installed,
redirected, dry run, declined, or failed with its typed failure — and the
command fails when at least one target failed.

### Consumer integration

#### REQ: host-owned-exit-codes

The library MUST NOT decide the process exit code. Install failures MUST be the
Self-Update Library's typed `*selfupdate.Failure`, extended with kinds for an
unknown target, no install directory, and an existing destination, so a host's
existing failure mapping covers both commands. A batch failure MUST expose each
target's typed failure. The Cobra adapter MUST accept an error mapper, as
[REQ: host-owned-exit-codes](../self-update/README.md#req-host-owned-exit-codes)
does for self-update, and pass usage errors (unknown flag value, `--all` with
names) through a distinguishable usage error type.

#### REQ: host-identity-from-catalog

A host MUST identify itself to the library by its catalog id and running
version only. A host id absent from the catalog MUST be a programming error
caught by the host's tests, not a runtime state users see.

#### REQ: core-framework-neutral

The catalog, status probe, planner and installer MUST NOT depend on a command
framework or write to the terminal. A framework-neutral `cliui`-style layer MUST
provide the text and JSON writers and reuse the Self-Update Library's
confirmation gate, and an optional `cliinstall/cobracmd` adapter MUST build the
`install` command with `--all`, `--yes/-y`, `--dry-run`, `--dir` and
`--format text|json`.

#### REQ: no-network-in-tests

The library's tests MUST NOT require network access, invoke a real `brew`, or
write outside temporary directories. Release endpoints, `PATH` resolution,
executable probing, the managed command runner, writability checks and
interactivity MUST be injectable.

#### REQ: fleet-cutover

All nine catalog CLIs MUST expose `install` built from this library and
implement [REQ: version-json-contract](#req-version-json-contract). As part of
this feature `ingitdb`, `ovdb` and `synchestra` MUST move their self-update onto
`github.com/strongo/cli-helpers/selfupdate` with no remaining import of
`github.com/strongo/selfupdate` or of ingitdb's `internal/selfupdate`, and
`datatug` MUST gain a self-update command built from the same package. Each
migrated CLI MUST keep its documented self-update exit-code contract.

## Consumers

| CLI | Repository | Cask token | Release source | Self-update today → after | Thin Feature (to add) |
|---|---|---|---|---|---|
| `wb` | `sneat-dev/wb` | `sneat-dev/tap/wb` | own releases | cli-helpers → cli-helpers | `spec/features/install` |
| `specscore` | `specscore/specscore-cli` | `specscore/tap/specscore` | own releases | cli-helpers → cli-helpers | `spec/features/cli/install` |
| `chatwright` | `chatwright/cli` | `chatwright/tap/chatwright` | own releases | cli-helpers → cli-helpers | `spec/features/install` |
| `codegrapher` | `code-grapher/codegrapher` | `code-grapher/tap/codegrapher` | own releases, flat `checksums.txt` | cli-helpers → cli-helpers | `spec/features/install` |
| `cover100` | `sneat-dev/cover100-cli` | none | own releases | cli-helpers → cli-helpers | `spec/features/install` |
| `ovdb` | `openvaultdb/ovdb` | `openvaultdb/tap/ovdb` | own releases, flat `checksums.txt` | `strongo/selfupdate` v0.6.0 → cli-helpers | none (repository has no `spec/`) |
| `synchestra` | `synchestra-io/synchestra` | none | `synchestra-io/synchestra-releases`, tag prefix `cli-` | `strongo/selfupdate` v0.4.0 → cli-helpers | `spec/features/cli/install` |
| `ingitdb` | `ingitdb/ingitdb-cli` | `ingitdb/cli/ingitdb` | own releases, flat `checksums.txt` | `internal/selfupdate` → cli-helpers | `spec/features/cli/install` |
| `datatug` | `datatug/datatug-cli` | `datatug/tap/datatug` | own releases | none → cli-helpers | `spec/features/cli/install`, `spec/features/cli/self-update` |

A consumer's Feature specifies only its exit-code mapping, print-only choice for
Homebrew, and deviations; the behavior above is inherited, not restated.

## Acceptance Criteria

### AC: datatug-installs-ovdb-and-ovdb-sees-datatug

**Requirements:** cli-install#req:list-relevant, cli-install#req:details-before-install, cli-install#req:install-method-mirrors-host, cli-install#req:direct-release-install, cli-install#req:post-install-verification, cli-install#req:status-probe-order, cli-install#req:version-json-contract, cli-install#req:fleet-cutover

**Given** a user on Linux with no fleet CLIs installed who put the latest released `datatug` binary into `~/.local/bin`, which is on `PATH`
**When** they run `datatug install`, then `datatug install ovdb` and confirm, then `ovdb install`
**Then** the first command lists `ingitdb`, `ovdb`, `specscore` and `wb` as not installed, each with its description and why it helps a DataTug user; the second shows ovdb's details, relevance and the planned release asset and destination `~/.local/bin/ovdb`, installs the latest ovdb release after one confirmation, and reports it verified; and `ovdb install` lists `datatug` as installed at `~/.local/bin/datatug` with the same version, release date and commit that `datatug version --json` prints, obtained through the JSON probe.

### AC: listing-is-offline-and-read-only

**Requirements:** cli-install#req:catalog-compiled-in, cli-install#req:list-offline-read-only, cli-install#req:list-all, cli-install#req:status-locate, cli-install#req:status-probe-bounded, cli-install#req:version-json-side-effect-free, cli-install#req:machine-readable-output

**Given** a host with no network access, one target on `PATH`, one only in the host's directory, and one whose `version --json` hangs
**When** `install`, `install --all` and `install --format json` run
**Then** each completes without a network request or file change, reports the `PATH` copy, reports the host-directory copy with a not-on-`PATH` warning, reports the hanging target as installed with version `unknown` after its deadline, and the JSON output carries full commit and timestamp values and the version source for every target.

### AC: older-builds-degrade-gracefully

**Requirements:** cli-install#req:status-probe-order, cli-install#req:version-json-contract

**Given** installed targets that answer only `version` text in the buildinfo form, only the `wb` multi-line form, only `--version`, nothing parseable, and a stale binary whose `version` names a different CLI
**When** status is probed
**Then** the first two report version, date and commit, the third reports a version with no commit or date, the fourth reports `unknown` with a warning, and the stale binary's foreign name is discarded rather than shown as the target's identity.

### AC: homebrew-host-installs-by-cask

**Requirements:** cli-install#req:install-method-mirrors-host, cli-install#req:homebrew-cask-install, cli-install#req:confirmation-gate, cli-install#req:install-dry-run

**Given** a host classified as managed by Homebrew
**When** it installs a target with a cask, then a target without a cask, then the first again as a dry run, then with a host configured print-only
**Then** the first confirms once and runs `brew install --cask <token>` as argv without a shell; the second installs the release into the first writable manual `PATH` directory or fails naming `--dir`, never into the Caskroom; the dry run prints the command without running it; and print-only reports the command as redirected with no failure.

### AC: direct-install-writes-only-verified-new-files

**Requirements:** cli-install#req:direct-release-install, cli-install#req:release-lookup-depth, cli-install#req:install-never-overwrites, cli-install#req:post-install-verification, cli-install#req:unknown-target-refused, cli-install#req:already-installed-no-op, cli-install#req:non-relevant-target-allowed

**Given** a manual host and a fake release server whose target release sits 40 releases deep behind another product's tag prefix
**When** it installs that target, a target whose checksum does not match, a target whose destination already holds a non-executable file of that name, a target already installed, a target not relevant to the host, and a name not in the catalog
**Then** the first is resolved, verified, staged and renamed into the host directory and verified by probe; the checksum mismatch and the existing file fail with typed kinds leaving nothing new on disk; the installed target is reported with its identity and a `self-update` pointer and not touched; the non-relevant target installs with a note instead of a relevance text; and the unknown name fails before any request, listing valid ids.

### AC: batch-reports-every-target

**Requirements:** cli-install#req:multi-target-batch, cli-install#req:confirmation-gate, cli-install#req:host-owned-exit-codes

**Given** `install a b c` where `b` fails to download, run once without a terminal and without `--yes`, and once with `--yes`
**When** the command runs
**Then** the first refuses before any write; the second installs `a` and `c`, reports `b`'s typed failure, fails overall through the host's error mapper, and the JSON output has one result per target.

### AC: catalog-matrix-is-valid

**Requirements:** cli-install#req:catalog-entry-identity, cli-install#req:catalog-identity-single-source, cli-install#req:relevance-matrix, cli-install#req:catalog-validated, cli-install#req:host-identity-from-catalog

**Given** the compiled catalog and recorded release asset snapshots for all nine CLIs
**When** the catalog tests run offline
**Then** every structural rule of the matrix holds, the initial matrix matches the table in this Feature, and every entry's `selfupdate.Config` names assets and checksums files that exist in its snapshot for each supported platform — including `checksums.txt` for ingitdb on darwin and windows.

### AC: two-hosts-keep-their-exit-codes

**Requirements:** cli-install#req:host-owned-exit-codes, cli-install#req:core-framework-neutral, cli-install#req:no-network-in-tests, cli-install#req:fleet-cutover

**Given** two hosts whose self-update exit-code contracts disagree, each building `install` from the Cobra adapter, and the migrated ingitdb, ovdb, synchestra and new datatug self-update commands
**When** the same install failure occurs in both hosts and each migrated self-update runs its existing test suite
**Then** each host exits with its own code for that failure kind, no catalog CLI imports `github.com/strongo/selfupdate` or ingitdb's `internal/selfupdate`, each migrated CLI's documented self-update exit codes are unchanged, and the library tests ran without network, `brew`, or writes outside temporary directories.

## Open Questions

- Should hosts installed by Scoop or WinGet install targets through the same
  manager (only `specscore` and `ingitdb` publish there today), instead of
  falling back to a direct release install?
- Should `install --latest` add an opt-in, rate-limit-aware lookup of each
  target's newest release to the otherwise offline listing, and should release
  lookups honor `GITHUB_TOKEN` to avoid the unauthenticated GitHub API limit?

---
*This document follows the https://specscore.md/feature-specification*
