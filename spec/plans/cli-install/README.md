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
eleven repositories: the `cliinstall` library in `strongo/cli-helpers`, the
`version --json` flag in `strongo/buildinfo`, and `install` plus `version --json`
wiring in all nine fleet CLIs — moving `ingitdb`, `ovdb` and `synchestra` onto
`cli-helpers/selfupdate` and giving `datatug` a self-update command on the way.

### Journey

In the user's own words, with the observable good result of each stage:

1. **"I installed DataTug and ran `datatug install`."** — Good result: a list of
   `ingitdb`, `ovdb`, `specscore` and `wb`, each marked installed or not, with
   version, release date and commit for the installed ones, a one-line
   description, and one line on why it helps me as a DataTug user. No network,
   under a second.
2. **"`ovdb` looked useful, so I ran `datatug install ovdb`."** — Good result: a
   longer description of OpenVaultDB, why it pairs with DataTug, and exactly what
   will happen (release version, asset URL, destination next to `datatug`, or the
   `brew install --cask` command on a Homebrew Mac), then one question.
3. **"I said yes."** — Good result: ovdb is downloaded, checksum-verified,
   written atomically, probed, and reported as installed at that version; any
   PATH or shell-cache caveat is stated with a remedy.
4. **"I ran `ovdb install`."** — Good result: `datatug` is listed as installed
   with the same version, release date and commit that `datatug version --json`
   prints, and `ingitdb` and `wb` are offered with ovdb-specific reasons.
5. **"My agent ran `ovdb install ingitdb wb --yes --format json`."** — Good
   result: one JSON document with one result per target and an exit code that
   follows ovdb's own contract.

| Journey step | Verified by |
|---|---|
| 1 | task-1 (listing, status), task-2 (`version --json`), task-11 (datatug wiring), task-12 (real releases) |
| 2 | task-1 (details, method choice), task-11, task-12 |
| 3 | task-1 (direct and Homebrew install, verification), task-12 |
| 4 | task-8 (ovdb wiring), task-11 (datatug `version --json`), task-12 |
| 5 | task-1 (batch, JSON, error mapper), task-8 (ovdb exit contract), task-12 |

## Approach

Library first, then one task per consumer repository, then one whole-journey
verification against real published releases.

- **Two library tasks run first and in parallel** (they share no code):
  task-1 in `strongo/cli-helpers` (catalog, probe, planner, installer, output,
  Cobra adapter, release-lookup page size, new failure kinds) and task-2 in
  `strongo/buildinfo` (`version --json` on the shared `version` subcommand).
  Both repositories release automatically from conventional commits on merge to
  `main`; each consumer task pins the tags they produce. Putting the JSON flag in
  `buildinfo` rather than a new `cli-helpers` helper means seven CLIs get the
  contract from a dependency bump instead of new wiring — only `wb` and
  `chatwright`, which own their `version` commands, add keys by hand.
- **One task per consumer repository** (tasks 3–11) combines everything that
  repository needs — dependency bumps, self-update migration or addition,
  `install` wiring, `version --json`, exit-code mapping, thin Feature — so each
  repository gets one branch, one CI run and one release. Every per-repository
  task depends on tasks 1 and 2 only, so any two can run together.
- **Concurrency:** the VM allows at most two concurrent Go lanes. Run tasks 1
  and 2 together; then pair consumers, migrations first because they carry the
  most risk: (10 ingitdb, 9 synchestra), (8 ovdb, 11 datatug), (3 wb, 4
  specscore), (5 chatwright, 6 codegrapher), then 7 cover100. Task 12 runs after
  all nine have released.
- **Catalog changes after task 1** (for example a relevance text a consumer
  review improves) go back through `cli-helpers` and ride the next dependency
  bump; consumer tasks do not fork catalog text.
- **Exit codes:** each host maps install failures with the same mapping its
  self-update already uses (listed per task), so a host has one failure-to-exit
  table. A declined confirmation, an already-installed target and a print-only
  Homebrew redirect exit 0 everywhere.

Per-repository verification in every consumer task, in addition to the listed
commands: `go build ./...`, `go vet ./...`, `go test ./...` with the repository's
coverage gate, `go mod tidy -diff`, `specscore spec lint` where the repository
has `spec/`, and a local smoke run of `<cli> version --json`, `<cli> install`,
`<cli> install --all --format json` and `<cli> install <target> --dry-run` from a
locally built binary.

## Tasks

### Task 1: cli-helpers install library and catalog

**Id:** task-1
**Verifies:** cli-install#ac:listing-is-offline-and-read-only, cli-install#ac:older-builds-degrade-gracefully, cli-install#ac:homebrew-host-installs-by-cask, cli-install#ac:direct-install-writes-only-verified-new-files, cli-install#ac:batch-reports-every-target, cli-install#ac:catalog-matrix-is-valid
**Depends-On:** —
**Status:** planning

Repository `strongo/cli-helpers`. Add package `cliinstall` (catalog as typed Go
source with the Feature's matrix and full relevance texts; status locate and
probe with injected `PATH` lookup, process runner and 3 s deadlines; method
planner; batch installer; framework-neutral text/JSON writers reusing
`selfupdate/cliui` confirmation) and `cliinstall/cobracmd` (`install` with
`--all`, `--yes/-y`, `--dry-run`, `--dir`, `--format`, error mapper). In
`selfupdate`: an exported install-to-path primitive reusing the unexported
download/verify/stage/rename code with a refuse-if-exists check; new failure
kinds `KindUnknownTarget`, `KindNoInstallDir`, `KindDestinationExists`;
`per_page=100` on the default releases URL; `Config` constructor per catalog
entry. Record release asset snapshots for all nine CLIs under
`cliinstall/testdata/releases/` (fetched once by a documented `go generate`
script, never by tests). The reference CLI keeps its single command. Before committing the catalog, check every relevance text against both CLIs' READMEs and Features and drop any claimed integration that does not exist (REQ relevance-matrix).

Files likely touched: `cliinstall/*.go`, `cliinstall/cobracmd/*.go`,
`cliinstall/testdata/**`, `selfupdate/{config,failure,install,release}.go` and
tests, `README.md`, `spec/features/cli-install/README.md` status →
Implementing. Verification: `go test -count=1 -coverprofile=cover.out ./...`
at the repository's 100% floor, `go mod tidy -diff`, `specscore spec lint`.
Merge produces the minor release consumers pin (expected `v0.14.0`).

### Task 2: buildinfo version --json

**Id:** task-2
**Verifies:** cli-install#ac:older-builds-degrade-gracefully
**Depends-On:** —
**Status:** planning

Repository `strongo/buildinfo` (no `spec/`). Add `Info.JSON()` producing the
`name`/`version`/`commit`/`date` object and a `--json` flag on
`cobracmd.VersionCommand`, which `fangcmd.Wire` reuses, so plain `version`
output is unchanged. Files: `buildinfo.go`, `cobracmd/cobracmd.go`,
`fangcmd/fangcmd.go` (only if the shared command needs re-export), tests,
`README.md`. Verification: `go test -count=1 ./...` with full coverage of the
new code, a test proving stdout is exactly one JSON object and that plain
`version` is byte-identical to before. Merge produces the minor release
consumers pin (expected `v0.3.0`).

### Task 3: wb install wiring

**Id:** task-3
**Verifies:** cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `sneat-dev/wb`. Bump `cli-helpers`; add `name`, `commit`, `date` to
`wb version --json` beside the existing `revision`/`built`; build the
self-update `Config` from the catalog keeping `HomebrewCask` executable steps and
`{"version","--json"}` probe args; register `install` from
`cliinstall/cobracmd` mapping every failure to `exitFindings` (1) as
self-update does. Files: `cmd/wb/{version,selfupdate,install}.go` and tests,
root command registration, `spec/features/install/README.md` (thin Feature),
`README.md` command list. Verification: wb's sharded coverage gate
(`go run ./cmd/wb coverage .`), smoke commands above.

### Task 4: specscore install wiring

**Id:** task-4
**Verifies:** cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `specscore/specscore-cli`. Bump `cli-helpers` (from v0.9.4) and
`buildinfo`; build self-update `Config` from the catalog keeping its
Homebrew/Scoop/WinGet executable managers; register `install` mapping failures
through the existing self-update table (`InvalidState` 4, `NotFound` 3,
`UpdateFailed` 9). Confirm `version --json` does not emit telemetry
(REQ version-json-side-effect-free) and exempt it if the telemetry hook runs for
every command. Files: `internal/cli/{self_update,install,root}.go` and tests,
`spec/features/cli/install/README.md`, `spec/features/cli/version/README.md`
amendment. Verification: `scripts/coverage-gate.sh` (100%), smoke commands.

### Task 5: chatwright install wiring

**Id:** task-5
**Verifies:** cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `chatwright/cli`. Bump `cli-helpers` (from v0.9.4) and `buildinfo`;
add `--json` to chatwright's own `version` command (canonical keys plus optional
`runtime`/`sdk`); build self-update `Config` from the catalog; register
`install` with the existing mapping (usage-class kinds → 2, else 1). Files:
`cmd/chatwright/{main,selfupdate,install}.go` and tests,
`spec/features/install/README.md`, `README.md`. Verification: `go test ./...`,
smoke commands.

### Task 6: codegrapher install wiring

**Id:** task-6
**Verifies:** cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `code-grapher/codegrapher`. Bump `cli-helpers` and `buildinfo`;
build self-update `Config` from the catalog (flat `checksums.txt`, keep
`AfterUpdate` skills re-exec); register `install` with no error mapper, matching
its self-update passthrough. Files: `internal/cli/{self_update,install,root}.go`
and tests, `spec/features/install/README.md`. Verification: `CGO_ENABLED=0 go
test -count=1 ./...` including goldens that enumerate the command tree (rebaseline
via the repository's scripts if the new command appears), smoke commands.

### Task 7: cover100 install wiring

**Id:** task-7
**Verifies:** cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `sneat-dev/cover100-cli`. Bump `cli-helpers` and `buildinfo`; build
self-update `Config` from the catalog (no managers); register `install` with
the passthrough mapper. The root command takes an optional `[path]`, so add a
test that `cover100 install` resolves to the subcommand and `cover100 ./install`
still reports that path. Files: `internal/cli/{self_update,install,root}.go` and
tests, `spec/features/install/README.md`, `spec/features/cli-command-surface`
amendment. Verification: CI "Coverage floor" at 100% locally, smoke commands.

### Task 8: ovdb self-update migration and install wiring

**Id:** task-8
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `openvaultdb/ovdb` (no `spec/`; record the configuration in
`README.md`). Replace `github.com/strongo/selfupdate` v0.6.0 imports with
`cli-helpers/selfupdate` and drop the module from `go.mod`; build `Config` from
the catalog keeping the executable `brew upgrade --cask ovdb` manager; keep the
0/1 exit contract for self-update and install. Files: `selfupdate.go`,
`install.go`, tests, `main.go`, `go.mod`, `README.md`. Verification:
`go test ./...`, `grep -rn "strongo/selfupdate\"" .` returns nothing, smoke
commands, `ovdb self-update --dry-run` names the same asset URL as before.

### Task 9: synchestra self-update migration and install wiring

**Id:** task-9
**Verifies:** cli-install#ac:direct-install-writes-only-verified-new-files, cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `synchestra-io/synchestra`. Replace `strongo/selfupdate` v0.4.0 with
`cli-helpers/selfupdate`; `Config` from the catalog (repository
`synchestra-io/synchestra-releases`, tag prefix `cli-`, no managers); keep the
`exitcode` mapping (`InvalidArgs` 2, `NotFound` 3, `InvalidState` 4,
`Unexpected` 10, update available → `Conflict` 1) for both commands. Files:
`pkg/cli/selfupdate/selfupdate.go`, new `pkg/cli/install`, `pkg/cli/main.go`,
tests, `go.mod`, `spec/features/cli/install/README.md`,
`spec/features/cli/self-update/README.md` pointer update. Verification:
`go test ./...`, no `strongo/selfupdate` import, `synchestra self-update
--dry-run` resolves the newest `cli-v*` release, smoke commands.

### Task 10: ingitdb self-update migration and install wiring

**Id:** task-10
**Verifies:** cli-install#ac:catalog-matrix-is-valid, cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `ingitdb/ingitdb-cli`. Delete `internal/selfupdate` and rebuild
`cmd/ingitdb/commands/self_update.go` on `cli-helpers/selfupdate/cobracmd`.
Known risk fixed here: the internal package fetches `checksums-darwin.txt` and
`checksums-windows.txt`, but releases publish only `checksums.txt` (checked
against v0.65.16), so darwin and windows self-update fail today; the catalog
entry names `checksums.txt` for every platform. Model Homebrew
(`ingitdb/cli/ingitdb`) and Snap (`snap refresh ingitdb`, `/snap/` marker) as
redirect-only managers; confirm the resolved-path-only classification does not
reclassify a real Snap or Homebrew layout (the old code also classified the
unresolved path). Preserve exit code 10 for `--check` with an update available
through `ErrorMapper.UpdateAvailable`, and map install failures the same way as
self-update failures. Files: `cmd/ingitdb/commands/{self_update,install}.go`
and tests, `cmd/ingitdb/main.go`, `internal/selfupdate/**` (deleted), `go.mod`,
`spec/features/cli/install/README.md`, `spec/features/cli/self-update/README.md`
pointer update. Verification: `go test ./...` at the 80% floor,
`golangci-lint run` with the repository config, no `internal/selfupdate`
import, smoke commands including `ingitdb self-update --check`.

### Task 11: datatug self-update addition and install wiring

**Id:** task-11
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:two-hosts-keep-their-exit-codes
**Depends-On:** 1, 2
**Status:** planning

Repository `datatug/datatug-cli`. Checked: releases already carry the
GoReleaser identity the library defaults to (`datatug_<v>_<os>_<arch>` tarballs,
windows zip, `datatug_<v>_checksums.txt`, cask `datatug/tap/datatug`), so no
release pipeline change is needed. Add `self-update` (alias `update`) from the
catalog `Config` with `HomebrewCask("datatug")` executable steps, and
`install`; define datatug's exit mapping (failure → 1, `--check` with update
available → 0, matching cover100's informational convention) in the thin
Features. Files: `main.go`, new `cmd/selfupdate` and `cmd/install` packages
(following the repository's command layout), tests, `go.mod`,
`spec/features/cli/self-update/README.md`, `spec/features/cli/install/README.md`,
`README.md`. Verification: `go test ./...` at the 49% floor with the new
packages fully covered, smoke commands, `datatug self-update --dry-run`.

### Task 12: whole-journey verification on published releases

**Id:** task-12
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug
**Depends-On:** 3, 4, 5, 6, 7, 8, 9, 10, 11
**Status:** planning

No code. On the Linux VM, in a scratch `HOME` with `PATH` reduced to system
directories plus `$HOME/.local/bin`, download the latest released `datatug`
archive by hand, verify its checksum, and run the Journey steps 1–5 verbatim,
capturing output. Then for each of the nine released CLIs run
`<cli> install --all --format json` and `<cli> version --json` and check the
matrix rows and probe sources. On the founder's Mac with a Homebrew-installed
host, run `<host> install <cask target> --dry-run` and one real cask install
of a target not yet installed. Record the evidence in this plan's task notes and
move the Feature to Stable only when every step's good result was observed.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
