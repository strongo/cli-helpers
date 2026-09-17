---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: CLI Install Command Library

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=explore) | [Edit](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=edit) | [Ask question](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=ask) | [Request change](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Lets any fleet CLI list, explain, and install the other fleet CLIs relevant to it, mirroring how the host itself was installed.

`github.com/strongo/cli-helpers/cliinstall` gives every fleet CLI an `install`
command. `<cli> install` lists the fleet CLIs relevant to the host, each with its
installed status (version, build or commit date, commit), a one-line description
and a one-line explanation of why it is useful to someone who uses the host.
`<cli> install <name>...` shows the long description and relevance of each named
CLI, confirms once, and installs them consistently with how the host was
installed: by `brew install --cask` when the host is Homebrew-managed and the
target publishes a cask for the host's OS, otherwise by downloading the target's
GitHub release asset, verifying its sha256 and placing it next to a manually
installed host or in the per-user bin directory. The download and verification
are the [Self-Update Library](../self-update/README.md)'s own machinery, reused
rather than re-specified.

The catalog of installable CLIs, their identities and the host → target
relevance texts are compiled into this module, so each CLI ships the catalog it
was built with. Installed status comes from a new fleet-wide `version --json`
contract that all nine CLIs implement, with a text fallback for older builds.

`<cli> upgrade` brings installed catalog CLIs — named ones, or all of them with
`--all` — to their latest release, each by the Self-Update Library's own policy
for how that copy was installed; `self-update` is the same operation for the
host alone.

This repository owns the behavior contract. Each consuming CLI carries a thin
Feature that points here and specifies only its own configuration and deviations
— see [Consumers](#consumers).

## Problem

The fleet's CLIs are used together — DataTug reads inGitDB databases, Synchestra
stores its state in inGitDB and orchestrates SpecScore specs, `wb` runs
`specscore spec lint` and can trigger `codegrapher sync` after checkouts — but
nothing inside any of them says so. A user of one tool learns that a sibling
exists only by reading a README, then has to find its install instructions, pick
a method consistent with how they installed the first tool, and check whether
they already have it and which build.

Every CLI already carries the hard part of installing a binary safely: its
self-update command resolves releases, verifies checksums and refuses to fight a
package manager. Installing a *different* CLI is the same problem with a
different identity and a destination that must be chosen. If each CLI adds its
own `install` verb, the fleet repeats the self-update story — nine copies, nine
exit-code philosophies, one reviewed.

Two further gaps block a shared implementation today:

- There is no machine-readable way to ask an installed CLI for its build
  identity. Only `wb version --json` exists, without a `name` key; everyone else
  prints `<name> <version> (<commit>) <date>` text, and stale builds print other
  shapes.
- Not every binary is on the shared self-update package. ingitdb has an internal
  copy whose per-OS checksum names no longer match its releases; `synchestra`,
  `ovdb`, `synchestra-channel` and `synchestra-vm-host` still import the older
  standalone `github.com/strongo/selfupdate` module; datatug has no self-update.
  A shared install that only some hosts can use would leave the fleet split.

## Behavior

### Catalog

#### REQ: catalog-compiled-in

The module MUST carry the catalog of installable CLIs as typed Go source in the
`cliinstall` package, compiled into every host. Listing, details and relevance
MUST NOT require network access or any file outside the host binary. A host sees
exactly the catalog of the `cli-helpers` version it was built with; catalog
changes reach users through the host's normal update.

#### REQ: catalog-entry-identity

Each catalog entry MUST carry: a stable id equal to the binary name; the release
identity in the Self-Update Library's `Config` shape (GitHub repository, tag
prefix, supported platforms, asset and checksums naming, version-probe
arguments, and the package managers that own it with their path markers); the
Homebrew cask token and the operating systems that cask supports, or none; a
homepage URL; a one-line description; a longer details text of at most a few
short paragraphs; and optionally legacy `--version` output signatures that
identify old builds of that CLI.

#### REQ: catalog-identity-single-source

The per-CLI identity constructors MUST live in `cliinstall` (for example
`Entry.Config(currentVersion)`), never in `selfupdate`, which keeps its Stable
[REQ: consumer-configured-identity](../self-update/README.md#req-consumer-configured-identity);
a test MUST prove the `selfupdate` package contains no catalog CLI name. A host's
`self-update` SHOULD build its `Config` from its own entry so its self-update and
every other host's `install <that cli>` resolve releases identically. A host MAY
add what only its own self-update needs (executable manager upgrade argv, Scoop
or WinGet managers, an after-update hook) but MUST NOT change repository, tag
prefix, asset naming or checksums naming away from its entry.

This couples a CLI's release naming to a `cli-helpers` release: a CLI that
changes its GoReleaser archive or checksum naming MUST first update its catalog
entry, release `cli-helpers`, and bump it in the same change. Each consumer MUST
carry an offline test asserting that its own `.goreleaser.y*ml` archive name
template, checksum name template, platforms, release repository and tag prefix
match its catalog entry, so drift fails that CLI's CI rather than a user's
install. The fleet keeps consumers on the latest `cli-helpers`, which bounds how
stale a shipped catalog can be.

#### REQ: relevance-matrix

The catalog MUST carry, for each host id, an ordered list of relevant target ids
each with a relevance text written for that pair: why a user of the host would
want the target and what they would do with the two together. Every text MUST be
unique to its pair and MUST be supported by a shipped behavior or statement in
the current README or a non-Draft Feature of either CLI; it MUST NOT describe
planned or Draft integrations as existing. A target MUST NOT be relevant to
itself. The initial matrix, with the basis for each pair:

| Host → target | Relevance (catalog carries the full text) | Basis |
|---|---|---|
| `wb` → `specscore` | `wb`'s `ci` profile runs `specscore spec lint` for every repository with `spec/` | wb README, CI profile |
| `wb` → `codegrapher` | a `wb` lifecycle hook can run `codegrapher sync --init` whenever a checkout updates, keeping code indexes fresh | wb README, lifecycle hooks |
| `wb` → `cover100` | `wb coverage` measures many repositories; `cover100` opens one repository's coverage as a zoomable treemap | wb and cover100 READMEs |
| `specscore` → `wb` | lint every synced repository's specs through `wb`'s CI profile instead of one clone at a time | wb README, CI profile |
| `specscore` → `ingitdb` | `specscore studio index` exports its facts as INGR recordsets, one of the record formats inGitDB stores natively; use `ingitdb` to keep your own structured project data in Git in the same format and check it with `ingitdb validate` | ingitdb `record-format` (Stable); specscore README Studio section |
| `specscore` → `synchestra` | Synchestra turns the SpecScore features and plans you write into task queues AI agents claim and work from, tracking status in a separate state repository | synchestra README |
| `specscore` → `chatwright` | verify conversational features with Chatwright scenarios alongside `specscore rehearse` acceptance scenarios | specscore and chatwright READMEs |
| `specscore` → `codegrapher` | CodeGrapher links source symbols to the SpecScore artifacts they implement | codegrapher `specscore-source-traceability` (Stable) |
| `chatwright` → `specscore` | specify the conversational behavior your Chatwright scenarios prove as SpecScore features, and lint them with `specscore spec lint` | chatwright README, specscore spec lint |
| `codegrapher` → `specscore` | lint and query the specs CodeGrapher's traceability edges point to | codegrapher `specscore-source-traceability` (Stable) |
| `codegrapher` → `wb` | let `wb` re-run `codegrapher sync` automatically after it updates a checkout | wb README, lifecycle hooks |
| `codegrapher` → `cover100` | pair CodeGrapher's call graph with cover100's coverage treemap to find heavily called code that lacks tests | codegrapher and cover100 READMEs |
| `cover100` → `codegrapher` | from an uncovered file in the treemap, run `codegrapher callers` to see what calls into it | codegrapher and cover100 READMEs |
| `cover100` → `wb` | measure coverage across a whole local fleet with `wb coverage --fleet`, then open single repositories in cover100 | wb README |
| `ovdb` → `ingitdb` | inGitDB is one of OpenVaultDB's pluggable storage engines; `ingitdb` validates and edits that data directly | ovdb README |
| `ovdb` → `datatug` | DataTug connects to a running `ovdb serve` database as an `openvaultdb` catalog with a scoped OpenVaultDB token, so you can query it and copy data out of it from DataTug's CLI and Web UI, under the server's own access policies | datatug `pkg/openvaultdb` + `docs/layered-acl-publication.md` (shipped) |
| `synchestra` → `ingitdb` | Synchestra uses inGitDB as its storage engine; `ingitdb` inspects and validates that state | synchestra README |
| `synchestra` → `specscore` | lint and scaffold the SpecScore features and plans Synchestra coordinates | synchestra README |
| `synchestra` → `datatug` | query and explore the inGitDB-stored project state in DataTug | synchestra README, datatug `go.mod` |
| `synchestra` → `ovdb` | OpenVaultDB's default engine is inGitDB, the same engine Synchestra stores state in; `ovdb serve` puts an inGitDB database behind an HTTP API with scoped, revocable tokens for tools that do not work in Git | ovdb README + `ovdb init --engine` default; synchestra README Storage |
| `synchestra` → `chatwright` | prove a delivered conversational agent with deterministic Chatwright scenario runs | chatwright README |
| `ingitdb` → `datatug` | explore and query inGitDB collections in DataTug's CLI and Web UI | datatug `go.mod`, READMEs |
| `ingitdb` → `ovdb` | serve an inGitDB repository as one storage engine behind OpenVaultDB | ovdb README |
| `ingitdb` → `synchestra` | Synchestra coordinates AI agents from a Git state repository kept in inGitDB (tasks, claims, status) and bundles `ingitdb` pull/setup/resolve, so your inGitDB repositories and skills carry straight into agent coordination | synchestra README Storage + Repository Types; `pkg/cli/main.go` commands |
| `ingitdb` → `specscore` | `specscore studio index` exports an ecosystem's spec, code-graph and manifest facts as INGR recordsets, a record format your inGitDB collections already support | specscore README Studio section; ingitdb `record-format` (Stable) |
| `datatug` → `ingitdb` | create, validate and edit the inGitDB databases DataTug reads | datatug `go.mod` |
| `datatug` → `ovdb` | run a user-owned OpenVaultDB server with `ovdb serve`, then register it in DataTug as an `openvaultdb` catalog to query it under that server's access policies | datatug `pkg/openvaultdb` + `docs/layered-acl-publication.md` (shipped) |
| `datatug` → `specscore` | if you're contributing to or extending DataTug, its own specifications are SpecScore artifacts — read and lint them with `specscore spec lint` | datatug README, `spec/` |

Listing order per host is the order of its rows above.

#### REQ: catalog-validated

The module's tests MUST prove, without network access, that every relevance
entry names a catalog id, no host lists itself or a target twice, every catalog
CLI appears as a host, no relevance text is duplicated, the matrix equals the
table above, each entry's asset and checksums names match a recorded snapshot of
that CLI's release asset list for every supported platform, and each cask's
declared operating systems match its recorded cask snapshot.

### Version JSON contract

#### REQ: version-json-contract

Every catalog CLI MUST implement `<cli> version --json`, printing exactly one
JSON object and nothing else to stdout and exiting 0. `--json` is the fleet-wide
probe flag regardless of a CLI's own output-flag convention (`--format`, `-o`),
which it MAY also offer. The object MUST contain these string keys:

| Key | Value |
|---|---|
| `name` | the binary name, equal to its catalog id |
| `version` | bare version without a leading `v`; `dev` when the build cannot determine it |
| `commit` | full commit SHA, with `+dirty` when built from a modified tree; `""` when unknown |
| `date` | RFC 3339 timestamp; `""` when unknown |
| `date_source` | `build` when `date` was stamped at link time (a released build's release build time), `commit` when it came from `debug.BuildInfo` `vcs.time`, `""` when unknown |

Additional keys are permitted and MUST be ignored by readers. Keys MUST NOT be
removed or change meaning; the contract evolves only additively. The writer and
the reader MUST share one exported Go type in `github.com/strongo/buildinfo`,
whose `version` subcommand is the implementation point for CLIs wired through
it; a CLI with its own `version` command (`wb`, and `chatwright`, whose `version`
also prints runtime and SDK versions) MUST emit the same keys.

#### REQ: version-json-side-effect-free

`version --json` MUST NOT perform network I/O, write or create files, start
daemons, run update checks, or emit telemetry — including start/exit telemetry
a CLI sends for other commands — so that probing an installed CLI is safe to
repeat and to run against every catalog entry at once.

### Status

#### REQ: status-locate

For each target the library MUST look for an executable file (not merely a file
of that name) named after the id with the platform executable suffix in every
absolute `PATH` entry, in the host executable's directory, and in `--dir` when
given. Relative `PATH` entries MUST be ignored. The first `PATH` match is the
reported copy; any other copies MUST be reported as additional paths (count in
text, full paths in JSON), and a copy found only outside `PATH` MUST carry a
not-on-`PATH` warning.

Classification MUST use the path as found on `PATH` and its symlink-resolved
path, each checked against the markers of every manager used anywhere in the
catalog (Homebrew, Scoop, WinGet, Snap, Chocolatey and any other), preferring
managed when either path matches; this is how `/snap/bin/ingitdb`, a symlink to
`/usr/bin/snap`, is recognized as Snap-managed.

#### REQ: status-probe-order

For a located copy the library MUST determine its identity by the first step
that yields this target's name: (1) `version --json` whose object has a `name`
equal to the id — an object without `name` falls through; (2) `version` whose
first line is `<name> <version> (<commit>) <date>`, tolerating `@` before the
date, or whose first line is `<name> <version>` followed by `revision:` and
`built:` lines (the `wb` form); (3) `--version` printing one version token, but
only when the entry declares a legacy signature matching that output. `none` and
`unknown` commit or date values MUST be read as empty. A step whose name differs
from the id MUST be discarded.

A copy for which some step succeeds is `installed` and reports version, commit,
date, date source and which step produced them (`version_json`, `version_text`,
`version_flag`). A copy for which no step yields this id is `unrecognized`: it is
reported with its path and the output that was seen, never under the target's
identity.

#### REQ: status-probe-bounded

Probing MUST execute the path as found on `PATH` (never the symlink-resolved
path, so argv0-dispatched binaries such as Snap shims run correctly), directly
without a shell, with empty stdin and `NO_COLOR=1`. Each target has a 3 second
budget covering all its probe steps; when it is exhausted the running process is
killed and the target is `unrecognized` with a timeout warning. At least four
targets MUST be probed concurrently. Probing MUST NOT write, move or delete any
file and MUST NOT make network requests of its own.

#### REQ: date-labelled-by-source

Text output MUST label a date by its source — `built` for `build`, `committed`
for `commit` — and show `date` for a text-probe date whose source cannot be
known. It MUST NOT call a date a release date.

### Listing and details

#### REQ: list-relevant

`<cli> install` with no names MUST list the host's relevant targets in matrix
order. Each row MUST show the id, the status (`installed`, `unrecognized`, or
`not installed`), and for a located copy its path, install method and additional
copies; for an installed copy its version, labelled date (date part only) and
short commit (7 characters); then the one-line description and the one-line
relevance. A final line MUST tell the user how to see details and install:
`<cli> install <name>`.

#### REQ: list-all

`--all` MUST list every catalog entry other than the host, relevant targets
first in matrix order, then the rest alphabetically; a row with no relevance
text for this host MUST say so instead of inventing one.

#### REQ: list-offline-read-only

Listing MUST make no network requests and modify nothing, so it is safe in
scripts, CI and agent sessions. Showing the latest available release per target
is out of scope for listing.

#### REQ: machine-readable-output

With `--format json`, listing, details, dry runs and install results MUST write
exactly one JSON document to stdout, with progress and warnings on stderr. Every
fact shown in text MUST be present in JSON with full values: `host`, and per
target `name`, `relevant`, `description`, `details` (details and install only),
`relevance`, `status`, `version`, `commit`, `date`, `date_source`, `path`,
`other_paths`, `install_method`, `manager`, `version_source` and `warnings`.

#### REQ: details-before-install

`<cli> install <name>...` MUST first print, for each named target, its
description, details, homepage, relevance to the host, current status, and the
planned action — the exact `brew` command, or the release version, asset URL and
destination path — before any confirmation prompt.

#### REQ: non-relevant-target-allowed

A catalog target that is not relevant to the host MUST still be installable. Its
details MUST state that it is not listed as relevant to the host instead of
showing a relevance text.

#### REQ: unknown-target-refused

A name that is not a catalog id MUST fail before any confirmation, network
request or write, with a typed failure naming the unknown name and listing the
valid ids.

#### REQ: already-installed-no-op

A target whose status is `installed` MUST NOT be downloaded, reinstalled or
replaced. The command MUST report the installed version, date and commit, name
`<target> self-update` as the way to update it, and count that target as a
success. Naming the host itself is reported the same way.

#### REQ: unrecognized-copy-not-trusted

An `unrecognized` copy MUST NOT be reported as installed, offered `self-update`,
or overwritten. An install whose destination is that copy's path MUST fail with
the destination-exists failure. An install into a different destination (the
planned one, or `--dir`) MUST proceed and warn when the unrecognized copy comes
earlier on `PATH` and will shadow the new install.

### Installing

#### REQ: install-method-mirrors-host

The host MUST be classified with its own catalog entry's managers. The method for
each target MUST be chosen without prompting, in this order:

1. `--dir` given: direct release install into that directory.
2. Host managed by Homebrew and the target's cask supports the host's operating
   system: Homebrew. Generated casks for all catalog taps declare both macOS and
   Linux binaries, and Homebrew supports `binary` casks on Linux since 4.5.0, so
   this applies to Linuxbrew hosts too.
3. Host classified manual and its directory allowed by
   [REQ: destination-denylist](#req-destination-denylist): direct release
   install into the host executable's directory.
4. Otherwise (a Homebrew host for a target without a suitable cask, a Scoop,
   WinGet, Snap or other managed host, an ambiguous host, or a manual host in a
   denied directory): direct release install into the per-user bin directory
   per [REQ: per-user-bin-dir](#req-per-user-bin-dir).

No other `PATH` directory is ever selected automatically.

#### REQ: per-user-bin-dir

The per-user bin directory is `$HOME/.local/bin` on Linux and macOS and
`%LOCALAPPDATA%\Programs\strongo\bin` on Windows. The Windows path sits under
`FOLDERID_UserProgramFiles`, Windows' documented per-user program location, and
uses one shared subdirectory so a single `PATH` entry serves every fleet CLI; it
is named after the library that defines the convention because the fleet spans
several organizations. It MUST be used only when it is present, compared as a
cleaned absolute path (case-insensitively on Windows), in `PATH`; it MUST be
created with mode `0755` when missing. When it is not on `PATH` the target MUST
fail with the no-install-directory failure, whose message names the directory,
how to add it to `PATH`, and `--dir`.

#### REQ: destination-denylist

Every direct-install destination, including `--dir` (resolved to an absolute,
symlink-resolved path), MUST be refused with the no-install-directory failure
when it is inside: `/usr`, `/bin`, `/sbin`, `/lib`, `/opt/homebrew`,
`/home/linuxbrew/.linuxbrew`, `/snap`, `/nix`, `$GOROOT`, `%ProgramData%`,
`%ProgramFiles%`, `%ProgramFiles(x86)%`, or `%SystemRoot%`; or when it matches
any catalog manager's path markers. There is no override: a user who really
wants a binary there can place the file themselves.

#### REQ: homebrew-cask-install

A Homebrew install MUST run `brew install --cask <token>` as structured argv
through the Self-Update Library's managed command runner, never through a shell,
streaming its output, after confirmation. Naming a target and confirming is the
explicit opt-in that
[REQ: managed-executable-command](../self-update/README.md#req-managed-executable-command)
requires; a host MAY configure print-only, in which case the command is reported
and the target counts as redirected, not failed. `brew` not resolvable on
`PATH`, or exiting non-zero, MUST fail that target with the managed-command
failure kind, naming `brew update` (Homebrew older than 4.5.0 cannot install
casks on Linux) and `--dir` as remedies.

#### REQ: direct-release-install

A direct install MUST resolve the target's latest stable release per
[REQ: latest-release-source](../self-update/README.md#req-latest-release-source)
and [REQ: multi-product-repository](../self-update/README.md#req-multi-product-repository),
and download and verify it by the same code and rules as a self-replace:
[REQ: download-matching-asset](../self-update/README.md#req-download-matching-asset),
[REQ: checksum-before-extract](../self-update/README.md#req-checksum-before-extract),
[REQ: unsupported-platform](../self-update/README.md#req-unsupported-platform),
and [REQ: permission-failure-identifiable](../self-update/README.md#req-permission-failure-identifiable).
Pinning a target version is not offered; the installed target's own
`self-update --version` covers that.

#### REQ: install-never-overwrites

The verified binary MUST be staged in the destination directory and placed with
a no-replace operation (hard link then unlink of the stage, or an atomic
no-replace rename where the platform has one), so an existing file at the
destination path is never overwritten, even one created after status was
probed. Where no no-replace primitive works on the destination filesystem the
library MAY fall back to check-then-rename and MUST document that race. An
existing file MUST fail with the destination-exists failure naming the path. A
failed install MUST leave nothing at the destination and no staging file behind.

#### REQ: post-install-verification

After a successful install the library MUST probe the installed target per
[REQ: status-probe-order](#req-status-probe-order) and confirm it reports the
installed release version; for Homebrew the first `PATH` copy is probed. A failed
confirmation, a destination not on `PATH`, a `PATH` lookup resolving to a
different copy, or a macOS quarantine rejection of an unsigned cask binary MUST
be reported as warnings with a remedy, not as a failed install. The result MUST
carry the
[REQ: shell-command-cache-refresh](../self-update/README.md#req-shell-command-cache-refresh)
remedy.

#### REQ: confirmation-gate

After all details are printed, the command MUST ask one confirmation covering
every target that would be installed; `--yes` skips it. Targets that are
already installed, unknown, or already failed planning are excluded from the
question. Without `--yes` and without an interactive terminal the command MUST
refuse per
[REQ: non-interactive-refusal](../self-update/README.md#req-non-interactive-refusal)
before any download or write. A declined confirmation installs nothing and is
not a failure. Selection is by name only; there is no interactive picker.

#### REQ: install-dry-run

`--dry-run` MUST walk the full decision path — status, method, destination
checks, release resolution, asset URL and destination, or the exact `brew`
command — and report it without invoking `brew`, downloading assets, creating
directories or writing anything, and without asking for confirmation.

#### REQ: multi-target-batch

`install a b ...` MUST process targets in the order given (for `upgrade`, except
the host, which [REQ: host-upgraded-last](#req-host-upgraded-last) moves last),
de-duplicated, and
MUST attempt every confirmed target even when an earlier one fails. The outcome
MUST carry one result per named target — installed, already installed,
redirected, dry run, declined, or failed with its typed failure — and the
command fails when at least one target failed.

### Upgrading

Founder decision (2026-09-17): the fleet-wide verb is `upgrade`; `self-update`
stays as the self-descriptive, searchable command; `upgrade --all` means every
*installed* catalog CLI, not the host's relevance list; `self-update` is the
shorthand for `upgrade <self>`, with no exception; the `update` alias is kept
where it already ships and not added anywhere else.

`<cli> upgrade [name...] [--all] [--check] [--yes] [--dry-run] [--format text|json]`
applies the [Self-Update Library](../self-update/README.md)'s update policy to
installed catalog CLIs. Nothing below re-specifies that policy; it only says
which copy is updated, with which resolved release, in which order, and how
results are batched.

#### REQ: upgrade-targets

`upgrade <name>...` MUST resolve each name against the catalog per
[REQ: unknown-target-refused](#req-unknown-target-refused). For a target other
than the host it MUST act on the copy [REQ: status-locate](#req-status-locate)
reports; the host is handled per [REQ: host-target-is-running-binary](#req-host-target-is-running-binary).
`upgrade --all` MUST target every catalog id whose status is `installed`, plus
the host, regardless of [REQ: relevance-matrix](#req-relevance-matrix); names
together with `--all` are a usage error.

#### REQ: upgrade-skips-non-release-builds

A version is a non-release build when it is in the target's
`UndeterminedVersions`, is `dev`, `(devel)` or `unknown`, is not strictly
`MAJOR.MINOR.PATCH` with an optional `-prerelease` (leading `v` allowed), carries
`+` build metadata (for example `0.20.3+dirty`), or is a Go pseudo-version.
Under `--all` and in the no-args report such a copy MUST be reported as
`skipped: non-release build` and MUST NOT be looked up, replaced, or counted as
upgradeable. Named explicitly, it MUST be offered like any other target and
included in the confirmation, whose text names it as a non-release build; with
`--yes` it proceeds.

#### REQ: upgrade-no-args-reports

`upgrade` with no names and no `--all` MUST NOT change anything: it MUST run the
read-only report of [REQ: upgrade-check](#req-upgrade-check) over the `--all`
target set and end with the next step, `<cli> upgrade --all` or
`<cli> upgrade <name>`. It MUST exit successfully when every lookup succeeded,
whether or not upgrades are available; when some lookups failed it MUST still
report every target and then fail through the host's error mapper with the
release-lookup failure. A bare verb that shows what would change, in one call,
serves agents and people better than a usage error. Unlike `install` listing it
needs network access, so it is not offline.

#### REQ: upgrade-per-target-policy

Each target MUST be handled by the Self-Update Library's policy for its
classified install, using that target's catalog managers. Classification of a
non-host copy MUST match `DetectSelf`: managed when the found path or its
symlink-resolved path matches a manager's markers, otherwise manual or
ambiguous judged on the symlink-resolved path only. The path passed for
replacement MUST be the symlink-resolved path, so a symlink is kept and a
symlink into a non-install location (for example a source checkout) is
ambiguous and refused.

| Status / classification | Behavior |
|---|---|
| `installed`, managed, redirect-only manager | report the manager's upgrade command per [REQ: managed-redirect-command](../self-update/README.md#req-managed-redirect-command) |
| `installed`, managed, executable manager | run its structured argv after the batch confirmation per [REQ: managed-executable-command](../self-update/README.md#req-managed-executable-command) (for example `brew upgrade --cask ovdb`) |
| `installed`, manual | verified atomic replacement of the resolved file per [REQ: download-matching-asset](../self-update/README.md#req-download-matching-asset), [REQ: checksum-before-extract](../self-update/README.md#req-checksum-before-extract), [REQ: atomic-replace](../self-update/README.md#req-atomic-replace), [REQ: post-swap-version-check](../self-update/README.md#req-post-swap-version-check) and [REQ: failure-leaves-working-binary](../self-update/README.md#req-failure-leaves-working-binary) |
| `installed`, ambiguous | refused per [REQ: ambiguous-safe-default](../self-update/README.md#req-ambiguous-safe-default) with manual-update guidance naming the path |
| `installed`, ahead of latest | reported with both versions, no action, per [REQ: ahead-of-latest](../self-update/README.md#req-ahead-of-latest) |
| `installed`, already latest | no-op per [REQ: no-op-when-current](../self-update/README.md#req-no-op-when-current) |
| `installed`, non-release build under `--all` | skipped per [REQ: upgrade-skips-non-release-builds](#req-upgrade-skips-non-release-builds) |
| `unrecognized` | never touched, reported per [REQ: unrecognized-copy-not-trusted](#req-unrecognized-copy-not-trusted) |
| `not installed` | reported with the hint `<cli> install <name>`; not a failure |

`upgrade` offers no version pin or downgrade flag; `self-update --version` and
`--allow-downgrade` remain the way to move one CLI to a specific release.

#### REQ: upgrade-resolves-release-once

For each target that is looked up, `upgrade` MUST resolve the latest stable
release exactly once, before details and confirmation, through the Self-Update
Library's exposed lookup, and MUST pass that tag to
[REQ: update-at-classified-copy](../self-update/README.md#req-update-at-classified-copy)
so the version shown and confirmed is the version installed; a newer release
published in between fails that target without changes. After the batch
confirmation, `UpdateAt` MUST be called with no per-target confirm callback,
because the batch gate already asked.

#### REQ: host-target-is-running-binary

The host target MUST always be the running executable, classified by
`DetectSelf` with the host's own running version, never the copy or version the
status probe found. When the first `PATH` copy of the host id is a different
file, the result MUST report it as another copy with a warning and MUST NOT
upgrade it. The host MUST use its own self-update `Config` and options, including
any after-update hook and manager overrides it adds to its catalog entry, so
`upgrade <self>` and `self-update` reach the same library call.

#### REQ: host-upgraded-last

As an explicit exception to the given-order rule of
[REQ: multi-target-batch](#req-multi-target-batch), the host MUST be processed
after every other target. The reason is the host's after-update hook, which may
restart a daemon or re-execute the new binary (wb does both); it must not run
while other targets are still pending. Windows needs no extra handling: the
library already renames a running executable aside before replacing it.

#### REQ: self-update-hook-hint

A catalog entry MUST declare whether its CLI's `self-update` performs
after-update work (`SelfUpdateHooks`; true today for `wb`, which restarts its
daemon and syncs skills, and `codegrapher`, which syncs skills). When such a
target other than the host is upgraded or has its manager command executed, the
result MUST carry a warning with the remedy `<target> self-update` to finish.
`upgrade` MUST NOT run another CLI's hooks itself.

#### REQ: self-update-equals-upgrade-self

For every outcome — redirect, executed manager command, replacement, no-op,
ahead of latest, refusal and each failure kind — `<cli> self-update --yes` MUST
behave as `<cli> upgrade <cli> --yes`, and `self-update --check`, `--dry-run` and
`--format json` as the matching `upgrade <cli>` flags; output shapes and exit
codes stay those `self-update` already documents. `self-update` keeps its name,
flags and aliases; the ahead-of-latest behavior both share comes from the
Self-Update Library's
[REQ: ahead-of-latest](../self-update/README.md#req-ahead-of-latest)
amendment, so there is no exception. Both commands MUST reach the same library
call so the equivalence holds by construction.

#### REQ: update-alias-policy

An `update` alias for `self-update` MUST remain where a released CLI already
ships it and MUST NOT be added to any other CLI; `upgrade` MUST NOT get an
`update` alias. datatug's planned `update` alias, never released, MUST NOT ship.
ingitdb MUST NOT gain one, because its `update` command edits records.

#### REQ: upgrade-release-lookups-bounded

`upgrade` MUST make at most one latest-release lookup per looked-up target, at
most four concurrently, each bounded by a 15 second timeout, and MUST NOT look
up releases for targets that are not installed, unrecognized, or skipped. When
`GH_TOKEN` or `GITHUB_TOKEN` is set, lookups to `api.github.com` MUST send it as
a bearer token through the library's `HTTPClient` seam, and the token MUST NOT
be sent to any other host or printed. A failed lookup MUST fail only that target
with the release-lookup failure kind; when GitHub answers 403 or 429 with
`X-RateLimit-Remaining: 0`, the message MUST say the API rate limit was reached
and name `GH_TOKEN` as the remedy. The rest of the batch continues, and the
command fails when any target failed.

#### REQ: upgrade-check

`--check` MUST be read-only: it runs status probing and release lookups and
reports, per target, the path, install method, manager and its upgrade command
when there is one, current version, latest version and verdict, carrying the
facts of [REQ: check-states-the-next-step](../self-update/README.md#req-check-states-the-next-step).
It MUST NOT download, write, confirm or invoke a manager. When at least one
looked-up target has an update available or an undetermined verdict, the Cobra
adapter MUST call the host's error mapper's upgrades-available method with
every such result, mirroring self-update's `UpdateAvailable` mapping. Targets
that are ahead of latest or skipped as non-release builds MUST NOT count, so a
machine with a source build does not signal upgrades forever. Failed lookups
fail the command as in
[REQ: upgrade-release-lookups-bounded](#req-upgrade-release-lookups-bounded),
taking precedence over the upgrades-available signal.

#### REQ: upgrade-batch-semantics

`upgrade` MUST follow `install`'s batch rules:
[REQ: details-before-install](#req-details-before-install) for the planned
action per target (command, or version transition, asset URL and path),
[REQ: confirmation-gate](#req-confirmation-gate) with one confirmation covering
every target that would be replaced or have a manager command executed,
[REQ: install-dry-run](#req-install-dry-run) for `--dry-run`,
[REQ: multi-target-batch](#req-multi-target-batch) for de-duplication,
continuing past failures and per-target results (upgraded, manager executed,
redirected, already current, ahead of latest, skipped non-release build, not
installed, unrecognized, refused, dry run, declined, or failed) and for order
except [REQ: host-upgraded-last](#req-host-upgraded-last), and
[REQ: machine-readable-output](#req-machine-readable-output) with added fields
`current`, `latest`, `verdict`, `action`, `command` and `resolved_path`.
Redirect-only targets need no confirmation.

### Consumer integration

#### REQ: host-owned-exit-codes

The library MUST NOT decide the process exit code. Install failures MUST be the
Self-Update Library's typed `*selfupdate.Failure`, extended with
`KindUnknownTarget`, `KindNoInstallDir` and `KindDestinationExists` appended
after the existing kinds so existing values do not change. A batch failure MUST
expose each target's typed failure. The Cobra adapter MUST accept an error
mapper and pass usage errors (bad flag value, `--all` with names) through a
distinguishable usage error type.

Every host MUST map the three new kinds explicitly in its install error mapper —
`KindUnknownTarget` to its usage or invalid-argument code, the other two to its
invalid-state or general failure code — and MUST NOT let them fall into a
self-update default branch or print a `self-update:` message prefix. Each host
MUST test `install nosuchcli` for its exit code and message.

The `upgrade` command MUST use the same error mapper, extended with an
upgrades-available method per [REQ: upgrade-check](#req-upgrade-check); a host
maps it as its self-update maps `UpdateAvailable`. Each host MUST test
`upgrade nosuchcli` the same way.

#### REQ: host-identity-from-catalog

A host MUST identify itself to the library by its catalog id and running version
only. A host id absent from the catalog MUST be a programming error caught by
the host's tests, not a runtime state users see.

#### REQ: core-framework-neutral

The catalog, status probe, planner and installer MUST NOT depend on a command
framework or write to the terminal. A framework-neutral layer MUST provide the
text and JSON writers and reuse the Self-Update Library's confirmation gate, and
an optional `cliinstall/cobracmd` adapter MUST build the `install` command with
`--all`, `--yes/-y`, `--dry-run`, `--dir` and `--format text|json`, and the
`upgrade` command with `--all`, `--check`, `--yes/-y`, `--dry-run` and
`--format text|json`.

#### REQ: no-network-in-tests

The library's and consumers' `install` and `upgrade` tests MUST NOT require network access,
invoke a real `brew`, or write outside temporary directories. Release endpoints,
`PATH` and environment, executable probing, the managed command runner, the
per-user bin directory and interactivity MUST be injectable.

#### REQ: fleet-cutover

All nine catalog CLIs MUST expose `install` and `upgrade` built from this library and implement
[REQ: version-json-contract](#req-version-json-contract). As part of this
feature every fleet binary MUST move its self-update onto
`github.com/strongo/cli-helpers/selfupdate`: `ingitdb` (dropping its
`internal/selfupdate`), `ovdb`, `synchestra`, and the non-catalog server binaries
`synchestra-channel` (`synchestra-io/synchestra-servers`) and
`synchestra-vm-host` (`synchestra-io/synchestra-vm`), which migrate self-update
only and do not get `install` because they are daemons deployed to hosts, not
tools a developer installs beside another CLI. `datatug` MUST gain a self-update
command built from the same package. Each migrated binary MUST keep its
documented self-update exit-code contract. When no fleet module imports
`github.com/strongo/selfupdate`, that module MUST be marked deprecated in its
README and `go.mod`; archiving it is the founder's decision.

## Consumers

| CLI | Repository | Cask token | Release source | Self-update today → after | Thin Feature (to add) |
|---|---|---|---|---|---|
| `wb` | `sneat-dev/wb` | `sneat-dev/tap/wb` | own releases | cli-helpers → cli-helpers | `spec/features/install` |
| `specscore` | `specscore/specscore-cli` | `specscore/tap/specscore` | own releases | cli-helpers → cli-helpers | `spec/features/cli/install` |
| `chatwright` | `chatwright/cli` | `chatwright/tap/chatwright` | own releases, tag-push only | cli-helpers → cli-helpers | `spec/features/install` |
| `codegrapher` | `code-grapher/codegrapher` | `code-grapher/tap/codegrapher` | own releases, flat `checksums.txt` | cli-helpers → cli-helpers | `spec/features/install` |
| `cover100` | `sneat-dev/cover100-cli` | none | own releases | cli-helpers → cli-helpers | `spec/features/install` |
| `ovdb` | `openvaultdb/ovdb` | `openvaultdb/tap/ovdb` | own releases, flat `checksums.txt` | `strongo/selfupdate` v0.6.0 → cli-helpers | none (repository has no `spec/`) |
| `synchestra` | `synchestra-io/synchestra` | none | `synchestra-io/synchestra-releases`, tag prefix `cli-` | `strongo/selfupdate` v0.4.0 → cli-helpers | `spec/features/cli/install` |
| `ingitdb` | `ingitdb/ingitdb-cli` | `ingitdb/cli/ingitdb` (unsigned) | own releases, flat `checksums.txt` | `internal/selfupdate` → cli-helpers | `spec/features/cli/install` |
| `datatug` | `datatug/datatug-cli` | `datatug/tap/datatug` | own releases | none → cli-helpers | `spec/features/cli/install`, `spec/features/cli/self-update` |

Self-update-only migrations (not catalog entries): `synchestra-channel`
(`synchestra-io/synchestra-servers`, tag prefix `servers-`) and
`synchestra-vm-host` (`synchestra-io/synchestra-vm`, tag prefix `vm-`), both
released to `synchestra-io/synchestra-releases` with flat `checksums.txt`.

A consumer's Feature specifies only its exit-code mapping, print-only choice for
Homebrew, and deviations; the behavior above is inherited, not restated.

## Acceptance Criteria

### AC: datatug-installs-ovdb-and-ovdb-sees-datatug

**Requirements:** cli-install#req:list-relevant, cli-install#req:details-before-install, cli-install#req:install-method-mirrors-host, cli-install#req:direct-release-install, cli-install#req:post-install-verification, cli-install#req:status-probe-order, cli-install#req:version-json-contract, cli-install#req:date-labelled-by-source, cli-install#req:fleet-cutover, cli-install#req:upgrade-targets, cli-install#req:upgrade-check

**Given** a user on Linux with no fleet CLIs installed who put the latest released `datatug` binary into `~/.local/bin`, which is on `PATH`
**When** they run `datatug install`, then `datatug install ovdb` and confirm, then `ovdb install`, then `ovdb upgrade --all --check`
**Then** the first command lists `ingitdb`, `ovdb` and `specscore` as not installed, each with its description and why it helps a DataTug user; the second shows ovdb's details, relevance and the planned release asset and destination `~/.local/bin/ovdb`, installs the latest ovdb release after one confirmation, and reports it verified; and `ovdb install` lists `datatug` as installed at `~/.local/bin/datatug` with the same version, `built` date and commit that `datatug version --json` prints, obtained through the JSON probe; and `ovdb upgrade --all --check` reports `datatug` and `ovdb`, each with its path, manual install method, current and latest version and verdict, changes nothing, and exits 0 when both are current or by ovdb's mapping of its upgrades-available signal otherwise — the same verdict `ovdb self-update --check` gives for ovdb.

### AC: listing-is-offline-and-read-only

**Requirements:** cli-install#req:catalog-compiled-in, cli-install#req:list-offline-read-only, cli-install#req:list-all, cli-install#req:status-locate, cli-install#req:status-probe-bounded, cli-install#req:machine-readable-output

**Given** a host with no network access, one target on `PATH` twice, one only in the host's directory, one whose `version --json` hangs, a relative `PATH` entry holding a fourth target, and a Snap-style symlink into a dispatcher binary
**When** `install`, `install --all` and `install --format json` run
**Then** each completes without a network request or file change; the first target shows one additional copy; the host-directory copy carries a not-on-`PATH` warning; the hanging target is `unrecognized` with a timeout warning after its budget; the relative entry is ignored; the Snap shim is probed through its `PATH` path and classified Snap-managed; and JSON carries full commit, timestamp, date source, other paths and version source.

### AC: older-builds-degrade-gracefully

**Requirements:** cli-install#req:status-probe-order, cli-install#req:version-json-contract, cli-install#req:date-labelled-by-source, cli-install#req:unrecognized-copy-not-trusted

**Given** installed copies that answer: `version --json` without `name` plus the `wb` multi-line text; only buildinfo `version` text with `(none)` and `unknown`; only `--version` matching a declared legacy signature; only `--version` with no signature; and a stale binary whose `version` names a different CLI
**When** status is probed
**Then** the first is `installed` from the text step, the second is `installed` with empty commit and date, the third is `installed` with a version only, and the last two are `unrecognized` with their output shown and no identity attributed.

### AC: install-destination-follows-policy

**Requirements:** cli-install#req:install-method-mirrors-host, cli-install#req:per-user-bin-dir, cli-install#req:destination-denylist, cli-install#req:unrecognized-copy-not-trusted

**Given** hosts at `/usr/bin` (AUR-installed, classified manual), `/usr/local/bin` on Intel macOS, `/opt/homebrew/Caskroom/...` for a target without a cask, and `~/go/bin`; a Windows host under `Program Files`; and a user whose per-user bin directory is on `PATH` in some cases and not in others
**When** each installs a target, with and without `--dir`, including `--dir /opt/homebrew/bin`, a relative `--dir`, and a `--dir` symlinked into `/usr/local`
**Then** `~/go/bin` installs beside the host; the denied host directories and the Caskroom host install into `~/.local/bin` (or `%LOCALAPPDATA%\Programs\strongo\bin`, created if missing) only when that directory is on `PATH` and otherwise fail naming the directory and `--dir`; denied `--dir` values fail even through a symlink; a relative `--dir` resolves against the working directory; and an unrecognized copy at the destination fails as destination-exists while a different `--dir` succeeds with a shadowing warning.

### AC: homebrew-host-installs-by-cask

**Requirements:** cli-install#req:install-method-mirrors-host, cli-install#req:homebrew-cask-install, cli-install#req:confirmation-gate, cli-install#req:install-dry-run, cli-install#req:post-install-verification

**Given** a host classified as managed by Homebrew on macOS and one on Linux
**When** each installs a target whose cask supports its OS, then the same as a dry run, then with a host configured print-only, then with a `brew` that exits non-zero
**Then** the install confirms once and runs `brew install --cask <token>` as argv without a shell; the dry run prints the command without running it; print-only reports the command as redirected with no failure; the failing `brew` yields the managed-command failure naming `brew update` and `--dir`; and a quarantine rejection of an unsigned cask binary is a warning with a remedy.

### AC: direct-install-writes-only-verified-new-files

**Requirements:** cli-install#req:direct-release-install, cli-install#req:install-never-overwrites, cli-install#req:post-install-verification, cli-install#req:unknown-target-refused, cli-install#req:already-installed-no-op, cli-install#req:non-relevant-target-allowed

**Given** a manual host and a fake release server
**When** it installs a target, a target whose checksum does not match, a target whose destination file is created between status and placement, a target already installed, a target not relevant to the host, and a name not in the catalog
**Then** the first is verified, staged and placed without replacement into the host directory and verified by probe; the checksum mismatch and the racing file fail with typed kinds, leaving the racing file intact and no stage behind; the installed target is reported with its identity and a `self-update` pointer and not touched; the non-relevant target installs with a note instead of a relevance text; and the unknown name fails before any request, listing valid ids.

### AC: batch-reports-every-target

**Requirements:** cli-install#req:multi-target-batch, cli-install#req:confirmation-gate, cli-install#req:host-owned-exit-codes

**Given** `install a b c` where `b` fails to download, run once without a terminal and without `--yes`, and once with `--yes`
**When** the command runs
**Then** the first refuses before any download or write; the second installs `a` and `c`, reports `b`'s typed failure, fails overall through the host's error mapper, and the JSON output has one result per target.

### AC: catalog-matrix-is-valid

**Requirements:** cli-install#req:catalog-entry-identity, cli-install#req:catalog-identity-single-source, cli-install#req:relevance-matrix, cli-install#req:catalog-validated, cli-install#req:host-identity-from-catalog

**Given** the compiled catalog, recorded release asset and cask snapshots for all nine CLIs, and each consumer's `.goreleaser.y*ml`
**When** the catalog tests run offline in cli-helpers and each consumer's naming test runs in its own repository
**Then** every structural rule of the matrix holds and it equals this Feature's table; every entry names assets and checksums files that exist in its snapshot for each supported platform, including `checksums.txt` for ingitdb on darwin and windows; `selfupdate` contains no catalog CLI name; and each consumer's GoReleaser naming, repository and tag prefix equal its catalog entry.

### AC: version-json-is-uniform-and-quiet

**Requirements:** cli-install#req:version-json-contract, cli-install#req:version-json-side-effect-free

**Given** each of the nine CLIs built from its consumer branch, once with release ldflags and once with plain `go build`
**When** `version --json` runs with telemetry configured and no network
**Then** stdout is exactly one object whose `name` is the catalog id, `date_source` is `build` for the stamped build and `commit` or empty for the plain build, plain `version` output is unchanged, and no network connection, telemetry event or file write occurs.

### AC: upgrade-all-covers-installed-not-relevant

**Requirements:** cli-install#req:upgrade-targets, cli-install#req:upgrade-skips-non-release-builds, cli-install#req:upgrade-no-args-reports, cli-install#req:upgrade-release-lookups-bounded, cli-install#req:upgrade-check, cli-install#req:upgrade-batch-semantics, cli-install#req:host-upgraded-last, cli-install#req:self-update-hook-hint

**Given** a current `cover100` host where `ovdb` (not relevant to cover100) and `wb` are installed and outdated, `ingitdb` is a `dev` build, `specscore` reports `0.52.0+dirty`, `chatwright` is a pseudo-version build, `codegrapher` is ahead of its latest release, `datatug` is an unrecognized binary, `synchestra` is not installed, `GH_TOKEN` is set, and the fake GitHub API rate-limits the `wb` lookup
**When** `cover100 upgrade`, then `cover100 upgrade --all --check`, then `cover100 upgrade --all --yes`, then `cover100 upgrade ingitdb synchestra datatug --yes`, then `cover100 upgrade --all wb` run
**Then** the bare command reports every target, looks up releases only for `ovdb`, `wb`, `codegrapher` and `cover100` with the token sent only to the API host, and fails through the mapper because of the `wb` rate limit whose message names `GH_TOKEN`; `--check` counts only `ovdb` as upgradeable, not the skipped, ahead, current or failed targets; `--all --yes` upgrades `ovdb`, processes `cover100` itself last as already current, reports `ingitdb`, `specscore` and `chatwright` as skipped non-release builds, `codegrapher` as ahead, `datatug` untouched, `wb` failed with the rest continuing, and the overall command failed; the named command upgrades the `dev` `ingitdb` after a confirmation naming it a non-release build, reports `synchestra` with an `install` hint and `datatug` as unrecognized; a successful upgrade of `wb` in a variant run carries the `wb self-update` hint; and `--all` with names is a usage error.

### AC: upgrade-respects-install-method

**Requirements:** cli-install#req:upgrade-per-target-policy, cli-install#req:upgrade-resolves-release-once, cli-install#req:upgrade-check, cli-install#req:host-owned-exit-codes

**Given** installed targets that are Homebrew-managed with executable argv, Homebrew-managed redirect-only, Snap-managed via a `/snap/bin` shim, manual behind a `~/.local/bin` symlink into `~/go/bin`, a `~/.local/bin` symlink into a source checkout, and already current; and a release server that publishes a newer release between confirmation and replacement for one target
**When** `upgrade` runs over them with confirmation, then with `--dry-run`, then with `--check`
**Then** the executable manager's argv runs once after the single batch confirmation without a shell, redirect-only and Snap targets print their manager commands without confirmation, the `~/go/bin` file is replaced atomically after checksum verification while the symlink is kept, the source-checkout symlink is refused as ambiguous with guidance naming the resolved path, the current target is a no-op, each target's release was looked up once, and the target whose release moved fails with the release-lookup kind and nothing changed; the dry run and check change nothing and invoke no manager; and the check maps through the host's upgrades-available method.

### AC: self-update-equals-upgrade-self

**Requirements:** cli-install#req:self-update-equals-upgrade-self, cli-install#req:host-target-is-running-binary, cli-install#req:host-upgraded-last, cli-install#req:update-alias-policy

**Given** each of a manual host, an executable-Homebrew host, a redirect-only host, an ambiguous host and a host build ahead of its latest release, against the same fake release server, and a manual host run from `./bin/ovdb` while a different `ovdb` is first on `PATH`
**When** `self-update --yes`, `self-update --check` and `self-update --dry-run` run, and separately `upgrade <self> --yes`, `upgrade <self> --check` and `upgrade <self> --dry-run`
**Then** each pair reaches the same library call on the running executable and produces the same action, target version, after-update hook invocation and failure kind, including `ahead` with no action and no update-available signal; the `PATH` copy is reported as another copy and left untouched; `self-update`'s existing flags, aliases, output and exit codes are unchanged; `upgrade` has no `update` alias; datatug has no `update` alias and ingitdb's `update` still edits records.

### AC: hosts-keep-their-exit-codes-and-cutover-completes

**Requirements:** cli-install#req:host-owned-exit-codes, cli-install#req:core-framework-neutral, cli-install#req:no-network-in-tests, cli-install#req:fleet-cutover

**Given** the nine hosts building `install` from the Cobra adapter, the migrated self-update commands of `ingitdb`, `ovdb`, `synchestra`, `synchestra-channel` and `synchestra-vm-host`, and datatug's new self-update
**When** each host runs `install nosuchcli` and `upgrade nosuchcli`, and each migrated binary runs its existing self-update tests
**Then** each host exits with its own usage code and a message without a `self-update:` prefix, no fleet module imports `github.com/strongo/selfupdate` or ingitdb's `internal/selfupdate`, each migrated binary's documented self-update exit codes are unchanged, `github.com/strongo/selfupdate` is marked deprecated, and the library and consumer install tests ran without network, `brew`, or writes outside temporary directories.

## Open Questions

- Should hosts installed by Scoop or WinGet install targets through the same
  manager (only `specscore` and `ingitdb` publish there today), instead of the
  per-user bin directory?
- Should `install --latest` add an opt-in, rate-limit-aware lookup of each
  target's newest release to the otherwise offline listing, and should release
  lookups honor `GITHUB_TOKEN` to avoid the unauthenticated GitHub API limit?
- Release lookup reads only GitHub's first page of 30 releases. That finds
  synchestra's newest `cli-` release today (6th of 71 in
  `synchestra-io/synchestra-releases`), but a burst of `servers-` or `vm-`
  releases could push it off the page. Should the Self-Update Library follow
  `Link: rel="next"` for prefixed repositories, amending its Stable spec?
- Should `upgrade` run another CLI's after-update work itself (for example
  `wb daemon restart --if-running` and `wb skills sync` after `datatug upgrade
  wb`) through a declared, timeout-bounded argv on the new binary, instead of
  the `<target> self-update` hint of
  [REQ: self-update-hook-hint](#req-self-update-hook-hint)?
- Should `upgrade` and `self-update` treat a manual copy inside a system
  prefix from [REQ: destination-denylist](#req-destination-denylist) (for
  example an AUR-installed `/usr/bin/ingitdb`) as ambiguous and refuse it? Today
  both replace it when writable, which keeps
  [REQ: self-update-equals-upgrade-self](#req-self-update-equals-upgrade-self)
  but lets a `sudo` run modify a system package manager's file; changing it
  means amending the Stable Self-Update Feature.
- Should a v2 offer interactive selection (a picker on a terminal) in addition
  to naming targets, given the fleet's preference for non-interactive,
  agent-friendly commands?

---
*This document follows the https://specscore.md/feature-specification*
