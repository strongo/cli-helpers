---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: CLI install command across the fleet

**Status:** Draft
**Source Feature:** cli-install
**Date:** 2026-09-17
**Owner:** ai
**Supersedes:** —

## Summary

Implements [Feature: cli-install](../../features/cli-install/README.md) across
thirteen repositories: the `cliinstall` library in `strongo/cli-helpers`, the
`version --json` contract in `strongo/buildinfo`, `install`, `upgrade` and
`version --json` wiring in all nine fleet CLIs — moving `ingitdb`, `ovdb` and `synchestra` onto
`cli-helpers/selfupdate` and giving `datatug` a self-update — self-update-only
migrations of `synchestra-channel` and `synchestra-vm-host`, and confirming
that the pre-rename `github.com/strongo/selfupdate` import path (this same
repository under its old name) has no remaining fleet consumer.

### Journey

In the user's own words, with the observable good result of each stage:

1. **"I installed DataTug and ran `datatug install`."** — Good result: a list of
   `ingitdb`, `ovdb` and `specscore`, each marked installed, unrecognized or not
   installed, with version, labelled build date and commit for installed ones, a
   one-line description, and one line on why it helps me as a DataTug user. No
   network access, and no waiting beyond a few seconds even if a binary hangs.
2. **"`ovdb` looked useful, so I ran `datatug install ovdb`."** — Good result: a
   longer description of OpenVaultDB, why it pairs with DataTug, and exactly what
   will happen (release version, asset URL, destination next to `datatug`, or the
   `brew install --cask` command on a Homebrew host), then one question.
3. **"I said yes."** — Good result: ovdb is downloaded, checksum-verified, placed
   without overwriting anything, probed, and reported as installed at that
   version; any PATH or shell-cache caveat is stated with a remedy.
4. **"I ran `ovdb install`."** — Good result: `datatug` is listed as installed
   with the same version, `built` date and commit that `datatug version --json`
   prints, and `ingitdb` is offered with an ovdb-specific reason.
5. **"My agent ran `ovdb install ingitdb nosuchcli --yes --format json`."** — Good
   result: one JSON document with one result per target, ingitdb installed,
   `nosuchcli` refused with the valid ids, and an exit code that follows ovdb's
   own contract.
6. **"Later I ran `ovdb upgrade --all --check`."** — Good result: `datatug` and
   `ovdb` (and `ingitdb`) are listed with path, install method, current and
   latest version and verdict; nothing changes; a build ahead of its latest
   release is shown as ahead and a source build as skipped, neither offered an
   upgrade or a downgrade; the exit code follows ovdb's mapping of "upgrades
   available", and ovdb's own row gives the same verdict as
   `ovdb self-update --check`.

| Journey step | Verified by |
|---|---|
| 1 | task-2 (catalog), task-3 (status), task-5 (listing output), task-6 (`version --json`), task-15 (datatug), task-19 (real releases) |
| 2 | task-4 (method and destination), task-5 (details output), task-15, task-19 |
| 3 | task-1 (no-replace placement), task-4 (install and verification), task-19 |
| 4 | task-3 (JSON probe), task-12 (ovdb), task-15 (datatug `version --json`), task-19 |
| 5 | task-4 (batch), task-5 (JSON, error mapper), task-12 (ovdb exit contract), task-19 |
| 6 | task-20 (`UpdateAt`, resolved tag, ahead verdict), task-21 (upgrade core), task-22 (`upgrade` command, check mapping), task-12, task-15, task-19 |

## Approach

Library in five small same-repository tasks, the buildinfo contract in
parallel, then one task per consumer repository, then confirmation that the
pre-rename `strongo/selfupdate` import path has no remaining consumer and one
whole-journey verification against real published releases.

- **Library split (cli-helpers).** task-1 (`selfupdate` placement primitive and
  failure kinds) and task-2 (catalog) touch disjoint packages and MAY run in
  parallel in separate worktrees; task-6 (`strongo/buildinfo`) is a different
  repository and is independent of both. task-3 (status) needs task-2 and
  task-6's exported JSON type; task-4 (planner and installer) needs task-1 and
  task-3; task-5 (output and Cobra adapter) needs task-4. The `upgrade`
  command adds three tasks numbered after the existing ones (the linter requires
  linear numbering): task-20 (the Self-Update Library amendment: `UpdateAt`,
  resolved-tag option, exposed latest-release lookup, `ahead` verdict), which
  touches only `selfupdate` and MAY run in parallel with task-5; task-21
  (`cliinstall` upgrade core), which needs task-4 and task-20; and task-22
  (`upgrade` writers and Cobra command), which needs task-5 and task-21.
  `cli-helpers`
  releases a minor tag on every `feat:` merge to `main`, so task-1's tag already
  unblocks the self-update-only migrations (task-16, task-17). Consumers pin the
  next minor tag produced after task-22 and task-6 land, read from
  `gh release list`, not a guessed number.
- **Catalog text is frozen in task-2.** Its relevance texts get their own
  adversarial check against each pair's cited basis before merge. Text fixes
  found later are batched and shipped in one propagation wave at the end
  (task-19 prerequisite), not by re-bumping already-released consumers one at a
  time.
- **One task per consumer repository** (task-7 to task-15) combines dependency
  bumps, self-update migration or addition, `install` and `upgrade` wiring,
  `version --json`,
  explicit exit mapping of the three new failure kinds, the GoReleaser-versus-
  catalog naming test, and thin Feature — one branch, one CI run, one release
  per repository. chatwright is the exception: its release runs only on a pushed
  tag (task-9).
- **Concurrency:** the VM allows at most two concurrent Go lanes. Suggested
  pairing: (task-1, task-2), (task-6, task-16), (task-3, task-17), task-4,
  (task-5, task-20), task-21, task-22, then consumers with migrations first: (task-14 ingitdb, task-13
  synchestra), (task-12 ovdb, task-15 datatug), (task-7 wb, task-8 specscore),
  (task-9 chatwright, task-10 codegrapher), task-11 cover100, then task-18 and
  task-19.
- **Runtimes and hosts:** implementation tasks run as local Claude Code
  subagents on the founder's Linux VM in `wb` worktrees; the coordinator is the
  single landing owner per repository and pushes chatwright's release tag. The
  macOS and Homebrew steps of task-19 run on the founder's Mac by the founder or
  a session the founder starts there.
- **Exit codes:** each host adds explicit cases for `KindUnknownTarget`
  (usage/invalid-argument code), `KindNoInstallDir` and `KindDestinationExists`
  (invalid-state or general failure code) with a command-neutral message, and
  keeps its self-update mapping for the shared kinds. `upgrade` uses the same
  mapper; its upgrades-available method maps exactly as that host's
  self-update `UpdateAvailable`. A declined confirmation, an already-installed
  or ahead-of-latest target and a print-only Homebrew redirect exit 0.
- **`update` alias:** kept only where a released CLI already ships it (checked:
  wb, specscore, chatwright, codegrapher, cover100, ovdb and synchestra); not added to
  `upgrade` or to any CLI that lacks it; datatug's planned alias is dropped;
  ingitdb never gets one because its `update` edits records.

Per-repository verification in every consumer task, in addition to the listed
commands: `go build ./...`, `go vet ./...`, `go test ./...` with the repository's
coverage gate and no network access in install tests (release endpoints
injected), `go mod tidy -diff`, `specscore spec lint` where the repository has
`spec/`, tests of `install nosuchcli` and `upgrade nosuchcli` asserting exit
code and message, a test that `self-update` and `upgrade <self>` reach the same
library call, the GoReleaser-naming test, and a local smoke run of
`<cli> version --json`, `<cli> install`, `<cli> install --all --format json`,
`<cli> install <target> --dry-run`, `<cli> upgrade`,
`<cli> upgrade --all --check --format json` and `<cli> upgrade --all --dry-run`
from a locally built binary. Each consumer's thin `install` Feature also covers
`upgrade` (exit mapping of the upgrades-available signal and alias status).

## Tasks

### Task 1: selfupdate placement primitive and failure kinds

**Id:** task-1
**Verifies:** cli-install#ac:direct-install-writes-only-verified-new-files
**Depends-On:** —
**Status:** complete
**Note:** Released as selfupdate InstallNew placement primitive, v0.14.0 (cli-helpers PR #26).

Repository `strongo/cli-helpers`, package `selfupdate` only; parallel-capable
with task-2. Export a verified-download-to-new-path primitive reusing the
unexported download, checksum and extract code, placing the staged binary with a
no-replace operation (hard link + unlink, `renameat2(RENAME_NOREPLACE)` on
Linux, `MoveFileEx` without replace on Windows; documented check-then-rename
fallback). Append `KindUnknownTarget`, `KindNoInstallDir`,
`KindDestinationExists` after `KindManagedCommand` with a test pinning existing
values. No change to self-update behavior or its Stable spec. Files:
`selfupdate/{install,failure}.go` and tests. Verification:
`go test -count=1 -coverprofile=cover.out ./selfupdate/...` at 100%, existing
self-update tests unchanged, Windows CI job green.

### Task 2: catalog, snapshots and matrix validation

**Id:** task-2
**Verifies:** cli-install#ac:catalog-matrix-is-valid
**Depends-On:** —
**Status:** complete
**Note:** Catalog, snapshots and matrix landed in cli-helpers PR #27.

Repository `strongo/cli-helpers`, new package `cliinstall` catalog files only;
parallel-capable with task-1. Nine entries with identity constructors
(`Entry.Config(version)`), cask tokens and supported OSes, legacy `--version`
signatures, descriptions, details and the Feature's relevance table with full
texts. Record release asset lists and cask files under
`cliinstall/testdata/snapshots/` with a documented `go generate` script that is
never run by tests. Tests: every rule of REQ catalog-validated, and that
`selfupdate` source contains no catalog id. Before merge, an adversarial pass
checks each relevance text against its cited basis; texts are then frozen.
Files: `cliinstall/catalog*.go`, `cliinstall/testdata/**`,
`cliinstall/gen/`. Verification: `go test ./cliinstall/...` at 100%.

### Task 3: status locate and probe

**Id:** task-3
**Verifies:** cli-install#ac:listing-is-offline-and-read-only, cli-install#ac:older-builds-degrade-gracefully
**Depends-On:** 2, 6
**Status:** complete
**Note:** Released as status locate/probe, v0.16.0 (cli-helpers PR #28).

Repository `strongo/cli-helpers`, `cliinstall` status files. Locate across
absolute `PATH` entries, host directory and `--dir`; classify unresolved and
resolved paths against all catalog manager markers; probe the unresolved path
with the three-step order, 3 s per-target budget and concurrency of four;
decode `version --json` into buildinfo's exported type; normalize `none` and
`unknown`; produce `installed`/`unrecognized`/`not installed` with other paths
and date source. All process, filesystem and environment access injected.
Files: `cliinstall/status*.go` and tests (including a hanging fake binary and a
Snap-style dispatcher). Verification: `go test -race ./cliinstall/...` at 100%,
bump `github.com/strongo/buildinfo` to task-6's tag, `go mod tidy -diff`.

### Task 4: planner, destination policy and batch installer

**Id:** task-4
**Verifies:** cli-install#ac:install-destination-follows-policy, cli-install#ac:homebrew-host-installs-by-cask, cli-install#ac:direct-install-writes-only-verified-new-files, cli-install#ac:batch-reports-every-target
**Depends-On:** 1, 3
**Status:** complete
**Note:** Released as planner/destination-policy/batch installer, v0.17.0 (cli-helpers PR #29).

Repository `strongo/cli-helpers`, `cliinstall` planning and install files.
Method order, per-user bin directory (`~/.local/bin`,
`%LOCALAPPDATA%\Programs\strongo\bin`, created only for a real install when on
`PATH`), the destination denylist with symlink resolution, Homebrew cask argv
through the managed runner with print-only option, direct install through
task-1's primitive, unrecognized-copy handling, post-install verification and
warnings, confirmation-gate semantics over a batch, dry run, per-target results.
Files: `cliinstall/{plan,destination,install,batch}*.go` and tests with
per-OS table cases (Windows paths tested by injected OS). Verification:
`go test ./cliinstall/...` at 100%.

### Task 5: output writers, Cobra adapter and library release

**Id:** task-5
**Verifies:** cli-install#ac:listing-is-offline-and-read-only, cli-install#ac:batch-reports-every-target, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 4
**Status:** complete
**Note:** Released as output writers and Cobra install adapter, v0.19.0 (cli-helpers PR #32).

Repository `strongo/cli-helpers`. Framework-neutral text and JSON writers
(labelled dates, additional copies, warnings on stderr), and
`cliinstall/cobracmd` building `install` with `--all`, `--yes/-y`, `--dry-run`,
`--dir`, `--format`, error mapper and usage error type; a test proving two
mappers give different exit codes for the same failure and that tests run
offline. Update `README.md` and move the Feature to Implementing. Files:
`cliinstall/cliui/*.go`, `cliinstall/cobracmd/*.go`, tests, `README.md`,
`spec/features/cli-install/README.md`. Verification: full-repository
`go test -count=1 -coverprofile=cover.out ./...` at 100%, `go mod tidy -diff`,
`specscore spec lint`; the merge's minor tag is what consumers pin.

### Task 6: buildinfo version --json

**Id:** task-6
**Verifies:** cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** —
**Status:** complete
**Note:** Released as buildinfo v0.3.0 (version --json contract).

Repository `strongo/buildinfo` (no `spec/`); independent of tasks 1–2. Track
whether `date` came from link-time stamping or `vcs.time`; export the JSON type
(`name`, `version`, `commit`, `date`, `date_source`) and `Info.JSON()`; add
`--json` to `cobracmd.VersionCommand`, which `fangcmd.Wire` reuses; plain
`version` output stays byte-identical. Files: `buildinfo.go`,
`cobracmd/cobracmd.go`, tests, `README.md`. Verification: `go test -count=1
./...` with full coverage of new code; stdout is exactly one object. Merge
produces the next minor tag.

### Task 7: wb install and upgrade wiring

**Id:** task-7
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** wb PR #565 merged; cli-helpers v0.136.0 tagged, release in progress.

Repository `sneat-dev/wb`. Bump `cli-helpers` and `buildinfo`; `wb version
--json` adds `name`, `commit`, `date`, `date_source` beside `revision`/`built`
using buildinfo's exported type; self-update `Config` from the catalog keeping
`HomebrewCask` steps and `{"version","--json"}` probe args; `install` mapping all
failures, including the three new kinds explicitly, to `exitFindings` (1).
Files: `cmd/wb/{version,selfupdate,install}.go` and tests, root registration,
`spec/features/install/README.md`, `spec/features/self-update/README.md` pointer
to catalog identity, `README.md`. Verification: `go run ./cmd/wb coverage .`
gate plus the common checks.

### Task 8: specscore install and upgrade wiring

**Id:** task-8
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** specscore-cli PR #212 merged; release in progress.

Repository `specscore/specscore-cli`. Bump `cli-helpers` (from v0.9.4) and
`buildinfo`; self-update `Config` from the catalog keeping Homebrew/Scoop/WinGet
executable managers; `install` mapper: `KindUnknownTarget` → invalid-arguments
code, `KindNoInstallDir`/`KindDestinationExists` → `InvalidState` 4, shared kinds
as self-update, no `self-update:` prefix. Exempt `version --json` from
telemetry. Files: `internal/cli/{self_update,install,root}.go`, telemetry hook,
tests, `spec/features/cli/install/README.md`, `spec/features/cli/version/README.md`
amendment for `--json`. Verification: `scripts/coverage-gate.sh` (100%) plus
common checks.

### Task 9: chatwright install and upgrade wiring and release

**Id:** task-9
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** chatwright/cli PR #28 merged; release NOT shipped — needs a manually pushed tag and founder confirmation of macOS notarization secrets (MACOS_SIGN_*); blocks task-19's chatwright rows.

Repository `chatwright/cli`. Bump `cli-helpers` (from v0.9.4) and `buildinfo`;
add `--json` to chatwright's own `version` command (buildinfo type plus optional
`runtime`/`sdk`); self-update `Config` from the catalog; `install` mapper:
`KindUnknownTarget` → 2, other new kinds → 1. Files:
`cmd/chatwright/{main,selfupdate,install}.go` and tests,
`spec/features/install/README.md`, `README.md`. Release: the workflow runs only
on a pushed `v*` tag and has not run since v0.8.0 (2026-08-09), and it requires
macOS notarization (`require_notarized_macos: true`) whose `MACOS_SIGN_*`
secrets are not visible at repository level. The landing owner confirms the
notarization secrets with the founder, pushes the next tag, and watches the
release; a notarization failure blocks task-19's chatwright rows and is
reported, not worked around.

### Task 10: codegrapher install and upgrade wiring

**Id:** task-10
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** code-grapher/codegrapher PR #46 merged; released v0.13.0.

Repository `code-grapher/codegrapher`. Bump `cli-helpers` and `buildinfo`;
self-update `Config` from the catalog (flat `checksums.txt`, keep `AfterUpdate`
skills re-exec); `install` with a small mapper that makes `KindUnknownTarget` a
usage error and passes other failures through, matching its self-update
passthrough otherwise. Files: `internal/cli/{self_update,install,root}.go` and
tests, `spec/features/install/README.md`. Verification: `CGO_ENABLED=0 go test
-count=1 ./...` including command-tree goldens (rebaselined by the repository's
scripts) plus common checks.

### Task 11: cover100 install and upgrade wiring

**Id:** task-11
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** cover100-cli PR #1 merged; release in progress.

Repository `sneat-dev/cover100-cli`. Bump `cli-helpers` and `buildinfo`;
self-update `Config` from the catalog (no managers); `install` mapper treating
`KindUnknownTarget` as usage and others as failure. The root command takes an
optional `[path]`, so test that `cover100 install` resolves to the subcommand
and `cover100 ./install` still means a path. Files:
`internal/cli/{self_update,install,root}.go` and tests,
`spec/features/install/README.md`, `spec/features/cli-command-surface/README.md`
amendment. Verification: coverage floor 100% plus common checks.

### Task 12: ovdb self-update migration and install and upgrade wiring

**Id:** task-12
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** ovdb merged to main at 5e7b44f; released v0.15.0.

Repository `openvaultdb/ovdb` (no `spec/`; record configuration in `README.md`).
Replace `github.com/strongo/selfupdate` v0.6.0 with `cli-helpers/selfupdate` and
drop it from `go.mod`; `Config` from the catalog keeping the executable
`brew upgrade --cask ovdb` manager; keep the 0/1 contract for both commands with
the new kinds mapped explicitly to 1 and a neutral message. Files:
`selfupdate.go`, `install.go`, tests, `main.go`, `go.mod`, `README.md`.
Verification: common checks, no `strongo/selfupdate` import, `ovdb self-update
--dry-run` names the same asset URL as before.

### Task 13: synchestra self-update migration and install and upgrade wiring

**Id:** task-13
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** synchestra PR #30 merged (squash — repo disallows merge commits); released cli-v0.21.0.

Repository `synchestra-io/synchestra`. Replace `strongo/selfupdate` v0.4.0 with
`cli-helpers/selfupdate`; `Config` from the catalog
(`synchestra-io/synchestra-releases`, tag prefix `cli-`, no managers); keep the
`exitcode` mapping (`InvalidArgs` 2, `NotFound` 3, `InvalidState` 4,
`Unexpected` 10, update available → `Conflict` 1) and add
`KindUnknownTarget` → `InvalidArgs` 2, the other new kinds → `InvalidState` 4.
The naming test reads the mirror workflow's tag prefix. Files:
`pkg/cli/selfupdate/selfupdate.go`, new `pkg/cli/install`, `pkg/cli/main.go`,
tests, `go.mod`, `spec/features/cli/install/README.md`,
`spec/features/cli/self-update/README.md` pointer update. Verification: common
checks, no `strongo/selfupdate` import, `synchestra self-update --dry-run`
resolves the newest `cli-v*` release.

### Task 14: ingitdb self-update migration and install and upgrade wiring

**Id:** task-14
**Verifies:** cli-install#ac:catalog-matrix-is-valid, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** ingitdb-cli PR #159 merged; released v0.67.0.

Repository `ingitdb/ingitdb-cli`. Delete `internal/selfupdate`; rebuild
`cmd/ingitdb/commands/self_update.go` on `cli-helpers/selfupdate/cobracmd`.
Fixes the known bug: the internal package fetches `checksums-darwin.txt` and
`checksums-windows.txt`, but releases publish only `checksums.txt` (v0.65.16),
so darwin and windows self-update fail today. Model Homebrew
(`ingitdb/cli/ingitdb`) and Snap (`snap refresh ingitdb`, `/snap/` marker) as
redirect-only; verify the Snap layout still classifies managed given the shared
package classifies the resolved path. Preserve exit 10 for `--check` with an
update available, and map `upgrade --check`'s upgrades-available signal to 10
as well; wire `upgrade` with no `update` alias (ingitdb's `update` edits
records); map `KindUnknownTarget` to its usage code and other new kinds
explicitly. Rewrite `spec/features/cli/self-update/README.md` as a thin Feature
pointing at the library (removing REQ checksum-verification's per-OS names and
the hand-specified flag surface, adding `--dry-run` and `--format`); amend
`spec/features/cli/version/README.md` to answer its `--format=json` Open
Question with the fleet `--json` flag; add `spec/features/cli/install/README.md`.
Files: `cmd/ingitdb/commands/{self_update,install}.go` and tests,
`cmd/ingitdb/main.go`, `internal/selfupdate/**` (deleted), `go.mod`, specs above.
Verification: 80% floor, `golangci-lint run` with the repository config, no
`internal/selfupdate` import, `ingitdb self-update --check` plus common checks.

### Task 15: datatug self-update addition and install and upgrade wiring

**Id:** task-15
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet, cli-install#ac:self-update-equals-upgrade-self
**Depends-On:** 22, 6
**Status:** complete
**Note:** datatug-cli PR #257 merged; released v0.31.0.

Repository `datatug/datatug-cli`. Releases already carry the default GoReleaser
identity (`datatug_<v>_<os>_<arch>` tarballs, windows zip,
`datatug_<v>_checksums.txt`, cask `datatug/tap/datatug`), so no pipeline change.
Add `self-update` (no `update` alias — the earlier plan's alias is dropped
before it ships) from the catalog `Config` with `HomebrewCask("datatug")` steps,
`install` and `upgrade`, in
`apps/datatugapp/commands/{cmd_self_update,cmd_install,cmd_upgrade}.go` following that
package's layout, registered where `main.go` builds the root. Exit mapping
follows datatug's own CLI spec, not a single failures-to-1 rule: invalid
arguments and `KindUnknownTarget` → 2, not-found failures → 3, I/O, permission
and destination failures (including `KindNoInstallDir`/`KindDestinationExists`)
→ 4, other failures → 1; `--check` with an update available → 0 for both
`self-update --check` and `upgrade --check`. `main.go` enqueues PostHog "CLI started"/"CLI exited"
events on every run: skip them for `version --json`. Files: those commands and
tests, `main.go`, `go.mod`, `spec/features/cli/self-update/README.md`,
`spec/features/cli/install/README.md`, `spec/features/cli/version/README.md`
amendment for `--json`, `README.md`. Verification: 49% floor with new files
fully covered, a test proving no telemetry event for `version --json`,
`datatug self-update --dry-run`, common checks.

### Task 16: synchestra-channel self-update migration

**Id:** task-16
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 1
**Status:** complete
**Note:** synchestra-channel self-update migration merged to main (synchestra-io/synchestra-servers).

Repository `synchestra-io/synchestra-servers`. Replace `strongo/selfupdate`
v0.4.0 in `cmd/synchestra-channel/selfupdate.go` with `cli-helpers/selfupdate`
(repository `synchestra-io/synchestra-releases`, tag prefix `servers-`, flat
`checksums.txt`), keeping its error mapping. No `install`: it is a daemon, not a
catalog CLI. Files: `cmd/synchestra-channel/selfupdate{,_test}.go`, `go.mod`,
`spec/features/channel-self-update/README.md` pointer to the library.
Verification: `go test ./...`, `go mod tidy -diff`, no `strongo/selfupdate`
import, `synchestra-channel self-update --dry-run` resolves the newest
`servers-v*` release.

### Task 17: synchestra-vm-host self-update migration

**Id:** task-17
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 1
**Status:** complete
**Note:** synchestra-vm-host self-update migration merged to main (synchestra-io/synchestra-vm).

Repository `synchestra-io/synchestra-vm` (no `spec/`). Same migration for
`cmd/synchestra-vm-host/selfupdate.go` (tag prefix `vm-`, flat `checksums.txt`,
nil managers), keeping its error mapping; no `install`. Files: that file and its
test, `go.mod`. Verification: `go test ./...`, `go mod tidy -diff`, no
`strongo/selfupdate` import, `synchestra-vm-host self-update --dry-run`.

### Task 18: confirm no fleet consumer of strongo/selfupdate — closed not applicable

**Id:** task-18
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 12, 13, 16, 17
**Status:** aborted
**Note:** Not applicable: github.com/strongo/selfupdate is the pre-rename module path of this same repository (GitHub redirects it to strongo/cli-helpers), so there is no separate repository to mark deprecated or archive. Verified no go.mod on main imports the old path in any of the 11 fleet repositories.

Repository `strongo/cli-helpers` — no separate repository exists to act on.
`github.com/strongo/selfupdate` is the pre-rename module path of this same
repository; GitHub redirects requests for it to `strongo/cli-helpers`, so there
is no other `go.mod` to mark `// Deprecated:` and no other `README.md` to carry
a deprecation notice, and no separate patch tag to release. Verified instead,
for all eleven fleet repositories (the nine catalog CLIs plus
`synchestra-servers` and `synchestra-vm`), that no `go.mod` on `main` imports
the old `github.com/strongo/selfupdate` path any more:
`gh search code "github.com/strongo/selfupdate" --owner <org>` per fleet org
plus a grep of local clones' `go.mod` files, excluding this module itself.
Closed as not applicable with that reason; archiving is moot because it is not
a separate repository.

### Task 19: whole-journey verification on published releases

**Id:** task-19
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 7, 8, 9, 10, 11, 12, 13, 14, 15, 18
**Status:** blocked
**Note:** Linux direct-install path PASSED against published releases in a sandbox HOME/PATH (see task body for evidence). Outstanding: founder's Mac Homebrew cask install/upgrade dry-run, and chatwright's rows (release not yet shipped, task-9).

Prerequisites: every consumer CLI has a published release carrying `install`
`upgrade` and `version --json` (chatwright via task-9's tag push), and any batched catalog
text fixes have shipped in one final propagation wave. Owner: the coordinator on
the Linux VM, in a scratch `HOME` with `PATH` reduced to system directories plus
`$HOME/.local/bin`: download the latest `datatug` archive by hand, verify its
checksum, run Journey steps 1–6 verbatim and capture output; then for each of the
nine released CLIs run `<cli> install --all --format json`,
`<cli> upgrade --all --check --format json` and `<cli> version --json` and
compare matrix rows, probe sources and upgrade verdicts; finally install an
older ovdb release by hand into the scratch bin and confirm `datatug upgrade
--all --yes` upgrades it and leaves datatug itself current. Owner: the founder on the Mac
with a Homebrew-installed host: `<host> install <cask target> --dry-run` and one
real cask install of a target not yet installed, including the unsigned ingitdb
cask, then `<host> upgrade --all --dry-run` showing `brew upgrade --cask` for
cask-managed targets. Record evidence in this task's notes; the coordinator then commits the
Feature's move to Stable in `cli-helpers`.

**Evidence (coordinator, Linux VM, 2026-09-17):** run against the published
releases in a sandbox `HOME`/`PATH`: `datatug install` listed `ingitdb`, `ovdb`
and `specscore`, each with description and Why; `datatug install ovdb --yes`
showed details, plan and asset URL and installed ovdb v0.15.0; `ovdb install`
showed `datatug: installed v0.31.0, built 2026-09-17, 947d77f`; `ovdb upgrade
--all --check` reported `datatug` and `ovdb` up to date (exit 0), matching
`ovdb self-update --check`; a real upgrade from ovdb 0.14.1 via
`datatug upgrade ovdb --yes` reached 0.15.0, verified via `ovdb version
--json`. Journey steps 1–6 PASSED for the Linux direct-install path. Not yet
run: the founder's Mac Homebrew cask install/upgrade dry-run, and chatwright's
rows, since its release has not shipped (task-9). Status is `blocked` on those
two, not `complete`.

### Task 20: Self-Update Library amendment for upgrade

**Id:** task-20
**Verifies:** cli-install#ac:self-update-equals-upgrade-self, cli-install#ac:upgrade-respects-install-method
**Depends-On:** —
**Status:** complete
**Note:** Released as UpdateAt/ahead verdict, v0.18.0 (cli-helpers PR #31).

Repository `strongo/cli-helpers`, package `selfupdate` only; MAY run in parallel
with task-5. Implements the self-update Feature amendment (status Amending):
REQ ahead-of-latest (an `Ahead` verdict from `Check`, a no-action outcome from
unpinned `Update`, `cobracmd` not calling `UpdateAvailable` for it, JSON verdict
`ahead`; pinned downgrade path unchanged) and REQ update-at-classified-copy
(`func (c Config) UpdateAt(ctx context.Context, detection Detection, opts
Options) (Outcome, error)`, `Update` = `DetectSelf` + `UpdateAt`, an exported
`LatestRelease(ctx) (tag string, err error)`, and `Options.ResolvedTag` that
skips both internal lookups and fails with `KindReleaseLookup` if the tag is no
longer latest). Existing tests stay green; new tests cover ahead (manual,
managed, check), `UpdateAt` on a non-running symlinked copy, and a moved
release. Files: `selfupdate/{update,version,release}.go`,
`selfupdate/cobracmd/cobracmd.go`, `selfupdate/cliui/output.go`, tests,
`spec/features/self-update/README.md` back to Stable on merge. Verification:
`go test -count=1 -coverprofile=cover.out ./selfupdate/...` at 100%,
`specscore spec lint`. Its merge releases a minor tag; migrated CLIs inherit
the ahead verdict for `self-update`.

### Task 21: cliinstall upgrade core

**Id:** task-21
**Verifies:** cli-install#ac:upgrade-all-covers-installed-not-relevant, cli-install#ac:upgrade-respects-install-method
**Depends-On:** 4, 20
**Status:** complete
**Note:** Released as cliinstall upgrade core, v0.20.0 (cli-helpers PR #33).

Repository `strongo/cli-helpers`, `cliinstall` upgrade files only. `Status`
gains `ResolvedPath`; classification of non-host copies follows `DetectSelf`
(managed on either path, manual/ambiguous on the resolved path). `Upgrade(ctx,
names, UpgradeOptions)` and `CheckUpgrades`: target selection (`--all` =
installed ids plus the host), non-release-build skipping, one `LatestRelease`
per looked-up target with concurrency 4 and a 15 s timeout, `GH_TOKEN` /
`GITHUB_TOKEN` bearer auth for `api.github.com` only through `HTTPClient`,
rate-limit message, host target from `DetectSelf` with the host's own `Config`
and hook (other PATH copy reported only), host last, `SelfUpdateHooks` catalog
flag (true for wb and codegrapher) producing the finish hint, batch gate then
`UpdateAt` with `ResolvedTag` and nil `Confirm`, dry run, per-target results,
partial-failure result. Files: `cliinstall/upgrade*.go`, `cliinstall/status.go`,
`cliinstall/catalog.go`, `cliinstall/catalog_{wb,codegrapher}.go`, tests.
Verification: `go test -race -count=1 ./cliinstall/...` at 100%.

### Task 22: upgrade output and Cobra command

**Id:** task-22
**Verifies:** cli-install#ac:self-update-equals-upgrade-self, cli-install#ac:upgrade-all-covers-installed-not-relevant
**Depends-On:** 5, 21
**Status:** complete
**Note:** Released as upgrade output writers and Cobra command, v0.21.0 (cli-helpers PR #34); this is the tag consumers pin.

Repository `strongo/cli-helpers`. Text and JSON writers for upgrade and check
results (current, latest, verdict, action, command, resolved path, ahead,
skipped, not-installed hint, finish hint, rate-limit message) in
`cliinstall/cliui`; `cliinstall/cobracmd` builds `upgrade` with `--all`,
`--check`, `--yes/-y`, `--dry-run`, `--format`, no aliases, and extends the
error mapper with an upgrades-available method; no-args runs the report and
exits 0 unless lookups failed. A test builds `self-update` (from
`selfupdate/cobracmd`) and `upgrade <self>` over the same fake host and asserts
the same library call, action and failure kind for manual,
executable-managed, redirect-only, ambiguous and ahead hosts, including a host
run from a path that is not first on `PATH`. Files:
`cliinstall/cliui/upgrade*.go`, `cliinstall/cobracmd/upgrade*.go`, tests,
`README.md`; a coordinator-ruled follow-up round (2026-09-17, see Review
Disposition) additionally touched `cliinstall/upgrade.go` to delegate every
per-target decision to `selfupdate.Config.UpdateAt`/`Config.Check` instead of
re-implementing it, and `spec/features/cli-install/README.md`. Verification:
full-repository coverage at 100%, `go mod tidy -diff`, `specscore spec lint`;
this merge's minor tag is what consumers pin.

## Review Disposition

Adversarial review of the first draft (2 blocking, 11 serious, 12 minor):

- B1 destination from PATH classification — fixed: PATH scanning dropped; host dir only when manual and not denylisted, else per-user bin dir on PATH, else fail naming `--dir`; denylist applies to `--dir` with no override (REQ install-method-mirrors-host, per-user-bin-dir, destination-denylist; AC install-destination-follows-policy).
- B2 incomplete cutover — fixed: task-16, task-17 migrate `synchestra-channel` and `synchestra-vm-host`; task-18 deprecates the module; "retired" wording removed; REQ fleet-cutover widened.
- S1 new kinds in default exit buckets — fixed: kinds appended after `KindManagedCommand`; every host maps them explicitly and tests `install nosuchcli` (REQ host-owned-exit-codes, tasks 7–15).
- S2 Snap and resolved-path probing — fixed: probe the `PATH` path as found; classify both paths against all catalog manager markers, preferring managed (REQ status-locate, status-probe-bounded).
- S3 foreign binaries block install — fixed: `unrecognized` status; never trusted or overwritten; install elsewhere allowed with shadowing warning; name-less JSON falls through (REQ status-probe-order, unrecognized-copy-not-trusted).
- S4 chatwright tag-gated release — fixed: task-9 has a landing-owner tag push with notarization check; task-19 prerequisite lists it.
- S5 task 1 too big — fixed: split into task-1 to task-5 with dependencies; task-1 and task-2 parallel-capable.
- S6 constructors in selfupdate — fixed: constructors in `cliinstall`; test that `selfupdate` names no catalog CLI (REQ catalog-identity-single-source).
- S7 Stable spec behavior changed silently — fixed: `per_page` change and REQ release-lookup-depth dropped; pagination is an Open Question in the Feature.
- S8 catalog coupling and drift — fixed in part: per-consumer GoReleaser naming test and stated trade-off; declined the scheduled snapshot-refresh job (coordinator ruling: per-consumer tests catch drift where it is introduced).
- S9 unsupported relevance texts — fixed: matrix rewritten with a cited basis per pair; codegrapher overlay, datatug project-spec and ovdb-git claims removed; ingitdb→synchestra and specscore↔ingitdb added; wb-everywhere invariant dropped.
- S10 Windows fallback — fixed: `%LOCALAPPDATA%\Programs\strongo\bin` when on `PATH`; no writability pre-probe (staging failure is the permission failure).
- S11 consumer spec work understated — fixed: ingitdb self-update spec rewrite, ingitdb and datatug version spec amendments, datatug path `apps/datatugapp/commands`.
- M1 Linuxbrew and unsigned casks — fixed: verified generated casks declare `on_linux` and Homebrew supports `binary` casks on Linux since 4.5.0; step applies when the cask supports the host OS; old-brew remedy and quarantine warning specified. Not executed on a Linuxbrew host (the VM has no brew).
- M2 release-date label and picker — fixed: REQ date-labelled-by-source and `date_source`; interactive selection is a v2 Open Question.
- M3 "under a second" — fixed: removed; 3 s per-target budget across all steps, concurrency of at least four.
- M4 exists-then-rename race — fixed: no-replace placement with documented fallback (REQ install-never-overwrites, task-1).
- M5 PATH shadowing in listing — fixed: additional copies reported (REQ status-locate, list-relevant).
- M6 `none`/`unknown` and multi-word `--version` — fixed: normalized; `--version` step requires a declared legacy signature.
- M7 JSON type defined twice — fixed: one exported type in buildinfo shared by writer and reader.
- M8 flag conventions differ — fixed: `--json` declared the fleet-wide probe flag independent of `--format`/`-o`.
- M9 loose Verifies — fixed: side-effect-free AC verified by consumer tasks; offline-tests part verified by task-5.
- M10 guessed tags — fixed: consumers pin the next minor tag read from `gh release list`.
- M11 catalog text churn — fixed: text frozen after an adversarial pass in task-2; later fixes batched into one final wave.
- M12 task-19 ownership — fixed: coordinator on the VM, founder on the Mac; Stable move is an explicit coordinator commit.
- (task-2 catalog review) No entry declares `LegacyVersionSignatures` — the two real old-build outputs found on this VM (a stale synchestra binary's bare `--version` printing `synchestra version 0.9.0 (92c5a01)`, and a from-source ingitdb's `--version` printing `ingitdb version unknown (built from source)`) are both multi-token lines, not the single version token REQ: status-probe-order's step 3 requires, so a declared signature could never match either one. Left empty rather than widening step 3's shape to accommodate them; that REQ is unchanged.

Adversarial review of the `upgrade` amendment (2 blocking, 5 serious, 6 minor):

- B1 double lookup and unconfirmed newer release — fixed: `LatestRelease` + `Options.ResolvedTag`, one lookup per target, moved release fails; batch gate then `UpdateAt` with nil `Confirm` (self-update REQ update-at-classified-copy; cli-install REQ upgrade-resolves-release-once; task-20).
- B2 which host copy — fixed: host target is always `DetectSelf` + running version; other PATH copies reported only (REQ host-target-is-running-binary).
- S1 symlinks and developer builds — fixed: replace the resolved path; manual/ambiguous judged on the resolved path, managed on either (REQ upgrade-per-target-policy; `Status.ResolvedPath` in task-21).
- S2 undetermined test too narrow — fixed: non-release build = catalog `UndeterminedVersions` ∪ `dev`/`(devel)`/`unknown` ∪ non-semver ∪ `+` metadata ∪ pseudo-versions; skipped under `--all` and the report, allowed when named with confirmation (REQ upgrade-skips-non-release-builds).
- S3 dropped hooks — fixed for v1: `SelfUpdateHooks` catalog flag (wb, codegrapher, verified as the only CLIs with `AfterUpdate`) adds a `<target> self-update` finish hint; running another CLI's hooks is a Feature Open Question (REQ self-update-hook-hint).
- S4 ahead exception — fixed: Self-Update Library amended with REQ ahead-of-latest (Feature moved to Amending); `self-update` inherits it; exception removed; journey step 6 reworded.
- S5 task too big — fixed: split into task-20 (selfupdate), task-21 (cliinstall core), task-22 (writers and command).
- M1 check signals forever — fixed: skipped and ahead targets do not count (REQ upgrade-check).
- M2 rate limits — fixed: `GH_TOKEN`/`GITHUB_TOKEN` through the `HTTPClient` seam, API host only; rate-limit message on the existing release-lookup kind (no new kind, so no new host mapping); partial results then failure exit (REQ upgrade-release-lookups-bounded, upgrade-no-args-reports).
- M3 order conflict — fixed: host-last is an explicit exception written into REQ multi-target-batch and REQ host-upgraded-last.
- M4 task-12 Verifies — fixed.
- M5 ingitdb version line owner — fixed: task-14 owns bringing the founder's decision before task-19 (Open Questions).
- M6 Windows rationale — fixed: host-last justified by the host's after-update hook; Windows needs nothing extra.

Adversarial review of the landed task-21/task-22 branch (3 blocking, 6 serious, 8 minor; coordinator ruling 2026-09-17): root cause was `PlanUpgrade` re-implementing `UpdateAt`'s own ambiguous/managed/current/ahead decisions instead of calling it, so `self-update` and `upgrade <self>` diverged on an ambiguous host (B1), an already-current host's after-update hook (B2), and a managed host's failed lookup or already-current version (B3) — fixed by making every non-skipped target's outcome come from one real `Config.UpdateAt`/`Config.Check` call, mapped for display only, and adding `UpgradeOptions.DetectHost`/`VerifyManaged` seams (S1, S2); confirmation now names non-release builds and shows the asset URL (S3); the self-update-equals-upgrade-self equivalence test became a real matrix (manual, executable-managed, redirect-only, ahead, ambiguous, a real `--yes` replacement, and hook-invocation-count parity) built hermetically, network-isolated and PATH-isolated (S4, S5); `spec/features/cli-install/README.md` amended for REQ upgrade-per-target-policy, upgrade-release-lookups-bounded, upgrade-check, upgrade-batch-semantics and upgrade-skips-non-release-builds (S6, M1-M8, task-22 own file scope).

## Decisions

- **Founder, 2026-09-17: add `upgrade` in this round.** `upgrade` is the
  fleet-wide verb; `self-update` stays as the self-descriptive, searchable
  command and is equivalent to `upgrade <self>`; `upgrade --all` means every
  installed catalog CLI, not the host's relevance list; the `update` alias stays
  where it already ships and is added nowhere else (Feature section
  "Upgrading").
- **`upgrade` with no arguments reports instead of failing.** It runs the
  read-only check over the `--all` set and exits 0 with the next step unless a
  lookup failed, because one call that shows what would change serves agents
  and people better than a usage error (REQ upgrade-no-args-reports).
- **Library primitives:** `selfupdate.Config.UpdateAt(ctx, detection, opts)`
  extracted from `Update`, `Config.LatestRelease(ctx)`, and
  `Options.ResolvedTag` (self-update REQ update-at-classified-copy).
- **Ahead-of-latest builds:** the Self-Update Library gains an `ahead` verdict
  with no action (self-update REQ ahead-of-latest), which `self-update` and
  `upgrade` share, so `self-update` is `upgrade <self>` with no exception.
- **Other CLIs' after-update work:** v1 reports a `<target> self-update` finish
  hint instead of running another binary's hooks.
- **Founder, 2026-09-17: ingitdb and specscore version lines.** Where a build
  can be stamped from either a module tag or a GitHub release tag, the GitHub
  release line (`v0.x`) is canonical, not any stray module-tag line. Stray
  `v1.x` tags were already absent from GitHub for both repositories; stale
  local `v1.x` tags were deleted. This resolves the Open Question task-14
  carried (ingitdb's module tags vs. its GitHub releases), applied before
  task-19 ran.

## Open Questions

Rollout follow-ups found during task-19's Linux verification (2026-09-17), not
yet decided or fixed:

- (a) Unauthenticated GitHub API calls hit the 60/hour rate limit during smoke
  tests; fall back to `gh auth token` when `GH_TOKEN`/`GITHUB_TOKEN` are both
  unset, instead of failing or waiting.
- (b) The `upgrade` success line mixes a bare and a `v`-prefixed version in the
  same sentence (for example "0.14.1 → v0.15.0"); pick one convention.
- (c) A manual copy inside a system prefix (for example an AUR-installed
  `/usr/bin/ingitdb`) is still replaced when the prefix is writable, the same
  question already open on the Feature (see
  [Feature Open Questions](../../features/cli-install/README.md#open-questions));
  founder decision pending.
- (d) Running another CLI's `AfterUpdate` hooks on a non-host `upgrade` target
  is still an open design question (see the Feature's Open Question on this);
  current behavior ships only the `<target> self-update` finish hint.
- (e) Two defects hit during rollout and filed rather than worked around: a
  `wb` landing issue, sneat-dev/wb#561, and a `specscore` test git-env
  pollution issue, specscore/specscore-cli#211.

---
*This document follows the https://specscore.md/plan-specification*
