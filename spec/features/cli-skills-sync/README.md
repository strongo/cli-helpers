---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: CLI Skills Synchronization Library

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-skills-sync?op=explore) | [Edit](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-skills-sync?op=edit) | [Ask question](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-skills-sync?op=ask) | [Request change](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-skills-sync?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Reusable offline-first, provenance-tracked Agent Skills synchronization for Go CLIs.

## Problem

Every CLI that distributes Agent Skills needs the same durable installation
semantics, but each must retain its own embedded, CLI-matched bundle. Copying
an installer into every CLI makes provenance, conflict handling, interrupted
replacement, and harness selection inconsistent. In particular, an interrupted
write must never remove the only usable copy of a user's installed skill.

## Behavior

`github.com/strongo/cli-helpers/skillsync` accepts immutable embedded bundles
and installs them into a selected harness directory. Normal sync is offline and
uses the installed CLI's matched revision. A caller may explicitly supply a
resolver for a newer compatible bundle. The core has no command-framework
dependency; the optional Cobra adapter owns target selection and rendering.
The provider and adapter MUST reach 100% statement coverage before this Feature
is publishable; a local checkpoint below that gate remains work in progress.

### REQ: immutable-bundle-provenance

The library MUST validate plugin identity, source revision, compatibility, and
the digest of every supplied bundle before it classifies or writes a target. It
MUST record only skills whose installed content it verified, along with the
supplying CLI identity. A default sync MUST NOT contact the network.

### REQ: ownership-and-coexistence

A plugin identity owns its skill names. The library MUST reject a name owned by
another plugin, an unmanaged target, a modified owned target, and symlink or
path-traversal targets. A newer bundle from the sole recorded supplier MAY
replace that plugin's verified content. When more than one CLI supplies a
plugin, every supplier MUST agree on the complete `BundleDescriptor.Source`:
repository, path, immutable revision, digest, version, and compatibility
bounds. A source mismatch, including different content under the same revision,
MUST be reported as a conflict for the whole plugin and leave existing content
unchanged.

### REQ: crash-safe-transaction

The library MUST classify the whole requested plan before its first target
mutation. Every mutation MUST use a transaction-owned same-filesystem staging
area and a durable journal that records constrained skill names, expected old
and new digests, and recovery state before the original is moved. Recovery and
rollback MUST validate the journal, transaction ownership, and content before
removing anything. If those checks cannot prove an action preserves a known
copy, recovery MUST fail closed without deleting target content.

Each copied stage and proof file MUST be written, file-synchronized, and
closed before its containing transaction directories are synchronized. The
transaction directory entry MUST be synchronized before its journal is relied
upon. After a backup, publish, or restore rename across directory parents, the
library MUST synchronize both the source and destination parents before it
advances the journal phase or discards recovery evidence. A state marker and
journal use a write, file-sync, close, rename, and parent-directory-sync
publication protocol. A persistence failure after marker rename MUST retain the
matching marker, target, and journal for a later public `Sync` to verify and
finish safely.

A dry run MUST validate a present recovery journal and its transaction control
path without mutating either. If a valid journal is pending, it MUST return the
typed `ErrRecoveryPending` result rather than classify the interrupted target
against stale ownership. A corrupt journal remains a typed corruption error.

### REQ: target-confinement

The target directory, state marker, journal, and transaction-owned directories
MUST be non-symlinked and confined beneath the selected target. Journal parsing
MUST reject symlink journals, arbitrary paths, ancestor escape, duplicate names,
or inconsistent transaction metadata. A corrupt marker or journal MUST leave
the target untouched and return a typed corruption error.

### REQ: durable-finalization-and-reporting

The ownership state MUST be written only after successful target publication.
It MUST bind the transaction identifier before cleanup can discard backups.
Cleanup and journal-removal errors MUST remain errors. A failed apply or
rollback MUST report the actual outcome for each planned skill, never claim an
add, update, or removal that was restored or left incomplete.

### REQ: process-safe-locking

Concurrent syncs for one target MUST serialize through an operating-system
lock that the operating system releases when its owner exits. A new process
MUST be able to acquire the lock and recover an interrupted transaction without
manually deleting a stale lock artifact.

### REQ: publishable-source-closure

A bundle's digest and packaging metadata MUST cover every declared skill,
reference, executable mode, and auxiliary resource needed after installation.
The canonical source MUST NOT rely on a repository-relative symlink or a path
outside the published bundle unless that resource is explicitly modeled and
packaged. Path hashing MUST retain leading-dot paths, and executable paths MUST
be declared and reproduced by embedding or extraction.

`BundleDescriptor.Source` is the single authority for repository, path, full
immutable Git revision, semantic version, compatibility, and digest. Declared
executable paths are validated regular files and participate in both complete
bundle and per-skill digests, so a mode-only change is visible. A development
CLI may use its matched embedded snapshot with an undetermined build version
only when that snapshot has no compatibility bounds. The former WB marker's
path-and-bytes hash is accepted only to verify one migration; the new marker
records the mode-aware digest before any legacy-content replacement can begin.

### REQ: reproducible-bundle-artifact

The provider MUST produce one bounded archive format containing a complete
descriptor and the canonical content tree. Archive bytes MUST be reproducible:
entries are sorted, timestamps fixed, and file modes normalized to the
descriptor's executable metadata. The reader MUST reject duplicate,
traversing, absolute, backslash, symlink, hardlink, special, oversized, or
over-count archive entries before exposing a bundle. It MUST validate the
embedded descriptor and digest before a caller can sync it. The same producer
MUST also emit an embed-ready directory; local, embedded, archived, and
installed content reproduce the same mode-aware digest.

### REQ: explicit-newer-compatible-release

Only explicit newer-compatible selection MAY use the GitHub Releases API. It
MUST page stable, non-draft releases through a bounded HTTP client, select the
newest compatible version strictly newer than the matched source, and download
that release's published archive rather than a branch tarball. A candidate
MUST retain plugin, repository, path, immutable revision, and descriptor/digest
identity. No newer compatible release is a normal result that retains the
matched embedded bundle. Authenticity is limited to HTTPS transport to the
configured GitHub endpoint plus the descriptor digest's artifact-integrity
check; this Feature does not claim signatures it has not verified.

### REQ: reusable-cobra-sync-command

`skillsync/cobracmd.NewSync` MUST provide a reusable Cobra leaf and `New`
MUST provide a convenience parent without assuming a product's command tree or
exit codes. Hosts supply their CLI identity, matched bundles, target layout,
error mapper, and renderer. Argument, flag, format, target-selection, and
explicit-newer configuration errors MUST be mapped as typed usage/config
errors before a target is written; home and filesystem failures remain runtime
failures. `--dir` bypasses home discovery and cannot be combined with
`--harness`; named targets preserve caller order, support aliases, comma and
repeated values, `all`, and configured home overrides, then deduplicate only
equivalent directories after each requested alias is validated.

The adapter MUST resolve and validate the selected source set once before it
starts target writes, then verify it again at each write. It MUST attempt
independent targets after an ordinary target failure, stop before later targets
after cancellation, retain every completed target report with its
harness/directory identity and runtime error, render the complete result, and
then return an aggregate typed failure. JSON stdout is exclusively JSON. Hosts
may replace rendering to preserve a legacy payload shape.

### REQ: cli-version-marker-provenance

The marker and public `Sync` report MUST record the generic current CLI version
and each resolved bundle's prior supplying CLI version without duplicating
bundle `Source`. A marker-only public status query MUST expose these values for
host drift banners. Legacy WB marker metadata, including an available
`wb_version`, MUST survive import. A changed successful supplier CLI version
MAY update marker provenance alone; a conflict MUST NOT advance it.

### REQ: compact-terminal-reporting

The shared terminal renderer MUST group target results by harness, summarize
unchanged counts, name changed and conflicted skills, distinguish planned,
applied, restored, and incomplete changes, and call dry runs previews. It MUST
never present a restored or incomplete mutation as a successful sync.

## Acceptance Criteria

### AC: matched-offline-sync

Given a CLI with a digest-pinned embedded bundle, when it syncs an empty target,
then the bundle's skills and verified ownership marker exist without a network
request; a repeated sync leaves skill timestamps unchanged.

### AC: safe-plugin-update

Given a target owned only by the requesting CLI, when that CLI supplies a newer
verified revision of the same plugin, then the installed skill is updated. Given
another CLI records a different complete source for that plugin, including
different bytes under the same revision, sync reports a conflict for every skill
in that plugin and preserves the current content.

### AC: abrupt-process-recovery

Given a child process exits at each journal, backup, publish, and state
boundary, when a new public `Sync` process starts on that target, then it either
restores the complete prior verified content or retains the complete committed
content, removes only verified transaction leftovers, and never loses the only
copy. These are process-exit recovery checks; they do not claim to reproduce
power loss. The journey includes failed record, publish, persistence, and
rollback paths, preserving a valid backup or proof plus journal whenever the
operation cannot prove rollback or final persistence.
A dry run during this pending state returns `ErrRecoveryPending` without changing
the target or journal; a normal public `Sync` then performs the verified recovery.

### AC: hostile-recovery-input

Given a symlinked journal, an escaped path, a transaction-directory symlink, or
corrupt journal/state metadata, when a new process attempts recovery, then it
returns a typed corruption error and does not alter files outside the target or
delete target content.

### AC: truthful-target-result

Given a failed write or recovery, when sync returns a report and error, then
every reported change distinguishes applied, restored, and incomplete work; no
rolled-back change is reported as added, updated, or removed.

### AC: publishable-plugin-content

Given both a colocated native plugin and a CLI-installed bundle at one pinned
revision, when each declared skill and its references are resolved from an
isolated installed location, then all declared entrypoints, assets, and
executable permissions work without a checkout-relative path. Their content and
mode manifests reproduce the same digest.

### AC: published-newer-compatible-bundle

Given paginated GitHub Release responses containing draft, prerelease,
incompatible, corrupt, and compatible archive assets, when a user explicitly
selects newer-compatible sync, then the adapter reads bounded release and asset
responses, skips ineligible entries, validates the selected archive before
sync, and retains the matched bundle when no compatible newer release exists.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
