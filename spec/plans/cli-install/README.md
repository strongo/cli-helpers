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
`version --json` contract in `strongo/buildinfo`, `install` plus `version --json`
wiring in all nine fleet CLIs — moving `ingitdb`, `ovdb` and `synchestra` onto
`cli-helpers/selfupdate` and giving `datatug` a self-update — self-update-only
migrations of `synchestra-channel` and `synchestra-vm-host`, and deprecation of
the standalone `strongo/selfupdate` module once nothing imports it.

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

| Journey step | Verified by |
|---|---|
| 1 | task-2 (catalog), task-3 (status), task-5 (listing output), task-6 (`version --json`), task-15 (datatug), task-19 (real releases) |
| 2 | task-4 (method and destination), task-5 (details output), task-15, task-19 |
| 3 | task-1 (no-replace placement), task-4 (install and verification), task-19 |
| 4 | task-3 (JSON probe), task-12 (ovdb), task-15 (datatug `version --json`), task-19 |
| 5 | task-4 (batch), task-5 (JSON, error mapper), task-12 (ovdb exit contract), task-19 |

## Approach

Library in five small same-repository tasks, the buildinfo contract in
parallel, then one task per consumer repository, then deprecation of the old
module and one whole-journey verification against real published releases.

- **Library split (cli-helpers).** task-1 (`selfupdate` placement primitive and
  failure kinds) and task-2 (catalog) touch disjoint packages and MAY run in
  parallel in separate worktrees; task-6 (`strongo/buildinfo`) is a different
  repository and is independent of both. task-3 (status) needs task-2 and
  task-6's exported JSON type; task-4 (planner and installer) needs task-1 and
  task-3; task-5 (output and Cobra adapter) needs task-4. `cli-helpers`
  releases a minor tag on every `feat:` merge to `main`, so task-1's tag already
  unblocks the self-update-only migrations (task-16, task-17). Consumers pin the
  next minor tag produced after task-5 and task-6 land, read from
  `gh release list`, not a guessed number.
- **Catalog text is frozen in task-2.** Its relevance texts get their own
  adversarial check against each pair's cited basis before merge. Text fixes
  found later are batched and shipped in one propagation wave at the end
  (task-19 prerequisite), not by re-bumping already-released consumers one at a
  time.
- **One task per consumer repository** (task-7 to task-15) combines dependency
  bumps, self-update migration or addition, `install` wiring, `version --json`,
  explicit exit mapping of the three new failure kinds, the GoReleaser-versus-
  catalog naming test, and thin Feature — one branch, one CI run, one release
  per repository. chatwright is the exception: its release runs only on a pushed
  tag (task-9).
- **Concurrency:** the VM allows at most two concurrent Go lanes. Suggested
  pairing: (task-1, task-2), (task-6, task-16), (task-3, task-17), task-4,
  task-5, then consumers with migrations first: (task-14 ingitdb, task-13
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
  keeps its self-update mapping for the shared kinds. A declined confirmation,
  an already-installed target and a print-only Homebrew redirect exit 0.

Per-repository verification in every consumer task, in addition to the listed
commands: `go build ./...`, `go vet ./...`, `go test ./...` with the repository's
coverage gate and no network access in install tests (release endpoints
injected), `go mod tidy -diff`, `specscore spec lint` where the repository has
`spec/`, a test of `install nosuchcli` asserting exit code and message, the
GoReleaser-naming test, and a local smoke run of `<cli> version --json`,
`<cli> install`, `<cli> install --all --format json` and
`<cli> install <target> --dry-run` from a locally built binary.

## Tasks

### Task 1: selfupdate placement primitive and failure kinds

**Id:** task-1
**Verifies:** cli-install#ac:direct-install-writes-only-verified-new-files
**Depends-On:** —
**Status:** planning

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
**Status:** planning

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
**Status:** planning

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
**Status:** planning

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
**Status:** planning

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
**Status:** planning

Repository `strongo/buildinfo` (no `spec/`); independent of tasks 1–2. Track
whether `date` came from link-time stamping or `vcs.time`; export the JSON type
(`name`, `version`, `commit`, `date`, `date_source`) and `Info.JSON()`; add
`--json` to `cobracmd.VersionCommand`, which `fangcmd.Wire` reuses; plain
`version` output stays byte-identical. Files: `buildinfo.go`,
`cobracmd/cobracmd.go`, tests, `README.md`. Verification: `go test -count=1
./...` with full coverage of new code; stdout is exactly one object. Merge
produces the next minor tag.

### Task 7: wb install wiring

**Id:** task-7
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 5, 6
**Status:** planning

Repository `sneat-dev/wb`. Bump `cli-helpers` and `buildinfo`; `wb version
--json` adds `name`, `commit`, `date`, `date_source` beside `revision`/`built`
using buildinfo's exported type; self-update `Config` from the catalog keeping
`HomebrewCask` steps and `{"version","--json"}` probe args; `install` mapping all
failures, including the three new kinds explicitly, to `exitFindings` (1).
Files: `cmd/wb/{version,selfupdate,install}.go` and tests, root registration,
`spec/features/install/README.md`, `spec/features/self-update/README.md` pointer
to catalog identity, `README.md`. Verification: `go run ./cmd/wb coverage .`
gate plus the common checks.

### Task 8: specscore install wiring

**Id:** task-8
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 5, 6
**Status:** planning

Repository `specscore/specscore-cli`. Bump `cli-helpers` (from v0.9.4) and
`buildinfo`; self-update `Config` from the catalog keeping Homebrew/Scoop/WinGet
executable managers; `install` mapper: `KindUnknownTarget` → invalid-arguments
code, `KindNoInstallDir`/`KindDestinationExists` → `InvalidState` 4, shared kinds
as self-update, no `self-update:` prefix. Exempt `version --json` from
telemetry. Files: `internal/cli/{self_update,install,root}.go`, telemetry hook,
tests, `spec/features/cli/install/README.md`, `spec/features/cli/version/README.md`
amendment for `--json`. Verification: `scripts/coverage-gate.sh` (100%) plus
common checks.

### Task 9: chatwright install wiring and release

**Id:** task-9
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 5, 6
**Status:** planning

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

### Task 10: codegrapher install wiring

**Id:** task-10
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 5, 6
**Status:** planning

Repository `code-grapher/codegrapher`. Bump `cli-helpers` and `buildinfo`;
self-update `Config` from the catalog (flat `checksums.txt`, keep `AfterUpdate`
skills re-exec); `install` with a small mapper that makes `KindUnknownTarget` a
usage error and passes other failures through, matching its self-update
passthrough otherwise. Files: `internal/cli/{self_update,install,root}.go` and
tests, `spec/features/install/README.md`. Verification: `CGO_ENABLED=0 go test
-count=1 ./...` including command-tree goldens (rebaselined by the repository's
scripts) plus common checks.

### Task 11: cover100 install wiring

**Id:** task-11
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 5, 6
**Status:** planning

Repository `sneat-dev/cover100-cli`. Bump `cli-helpers` and `buildinfo`;
self-update `Config` from the catalog (no managers); `install` mapper treating
`KindUnknownTarget` as usage and others as failure. The root command takes an
optional `[path]`, so test that `cover100 install` resolves to the subcommand
and `cover100 ./install` still means a path. Files:
`internal/cli/{self_update,install,root}.go` and tests,
`spec/features/install/README.md`, `spec/features/cli-command-surface/README.md`
amendment. Verification: coverage floor 100% plus common checks.

### Task 12: ovdb self-update migration and install wiring

**Id:** task-12
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 5, 6
**Status:** planning

Repository `openvaultdb/ovdb` (no `spec/`; record configuration in `README.md`).
Replace `github.com/strongo/selfupdate` v0.6.0 with `cli-helpers/selfupdate` and
drop it from `go.mod`; `Config` from the catalog keeping the executable
`brew upgrade --cask ovdb` manager; keep the 0/1 contract for both commands with
the new kinds mapped explicitly to 1 and a neutral message. Files:
`selfupdate.go`, `install.go`, tests, `main.go`, `go.mod`, `README.md`.
Verification: common checks, no `strongo/selfupdate` import, `ovdb self-update
--dry-run` names the same asset URL as before.

### Task 13: synchestra self-update migration and install wiring

**Id:** task-13
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 5, 6
**Status:** planning

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

### Task 14: ingitdb self-update migration and install wiring

**Id:** task-14
**Verifies:** cli-install#ac:catalog-matrix-is-valid, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 5, 6
**Status:** planning

Repository `ingitdb/ingitdb-cli`. Delete `internal/selfupdate`; rebuild
`cmd/ingitdb/commands/self_update.go` on `cli-helpers/selfupdate/cobracmd`.
Fixes the known bug: the internal package fetches `checksums-darwin.txt` and
`checksums-windows.txt`, but releases publish only `checksums.txt` (v0.65.16),
so darwin and windows self-update fail today. Model Homebrew
(`ingitdb/cli/ingitdb`) and Snap (`snap refresh ingitdb`, `/snap/` marker) as
redirect-only; verify the Snap layout still classifies managed given the shared
package classifies the resolved path. Preserve exit 10 for `--check` with an
update available; map `KindUnknownTarget` to its usage code and other new kinds
explicitly. Rewrite `spec/features/cli/self-update/README.md` as a thin Feature
pointing at the library (removing REQ checksum-verification's per-OS names and
the hand-specified flag surface, adding `--dry-run` and `--format`); amend
`spec/features/cli/version/README.md` to answer its `--format=json` Open
Question with the fleet `--json` flag; add `spec/features/cli/install/README.md`.
Files: `cmd/ingitdb/commands/{self_update,install}.go` and tests,
`cmd/ingitdb/main.go`, `internal/selfupdate/**` (deleted), `go.mod`, specs above.
Verification: 80% floor, `golangci-lint run` with the repository config, no
`internal/selfupdate` import, `ingitdb self-update --check` plus common checks.

### Task 15: datatug self-update addition and install wiring

**Id:** task-15
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 5, 6
**Status:** planning

Repository `datatug/datatug-cli`. Releases already carry the default GoReleaser
identity (`datatug_<v>_<os>_<arch>` tarballs, windows zip,
`datatug_<v>_checksums.txt`, cask `datatug/tap/datatug`), so no pipeline change.
Add `self-update` (alias `update`) from the catalog `Config` with
`HomebrewCask("datatug")` steps and `install`, in
`apps/datatugapp/commands/{cmd_self_update,cmd_install}.go` following that
package's layout, registered where `main.go` builds the root. Exit mapping:
failures → 1, `KindUnknownTarget` → 1 with a usage message, `--check` with an
update available → 0. `main.go` enqueues PostHog "CLI started"/"CLI exited"
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
**Status:** planning

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
**Status:** planning

Repository `synchestra-io/synchestra-vm` (no `spec/`). Same migration for
`cmd/synchestra-vm-host/selfupdate.go` (tag prefix `vm-`, flat `checksums.txt`,
nil managers), keeping its error mapping; no `install`. Files: that file and its
test, `go.mod`. Verification: `go test ./...`, `go mod tidy -diff`, no
`strongo/selfupdate` import, `synchestra-vm-host self-update --dry-run`.

### Task 18: deprecate strongo/selfupdate

**Id:** task-18
**Verifies:** cli-install#ac:hosts-keep-their-exit-codes-and-cutover-completes
**Depends-On:** 12, 13, 16, 17
**Status:** planning

Repository `strongo/selfupdate`. First prove no importer remains: `gh search code
"github.com/strongo/selfupdate" --owner` for each fleet org plus a grep of local
clones' `go.mod` files, excluding the module itself. Then add a `// Deprecated:
use github.com/strongo/cli-helpers/selfupdate` comment on the `module` line of
`go.mod` and a deprecation notice at the top of `README.md`, and release a patch
tag so the Go toolchain surfaces it. Archiving the repository is left to the
founder and recorded as a recommendation in the report.

### Task 19: whole-journey verification on published releases

**Id:** task-19
**Verifies:** cli-install#ac:datatug-installs-ovdb-and-ovdb-sees-datatug, cli-install#ac:version-json-is-uniform-and-quiet
**Depends-On:** 7, 8, 9, 10, 11, 12, 13, 14, 15, 18
**Status:** planning

Prerequisites: every consumer CLI has a published release carrying `install`
and `version --json` (chatwright via task-9's tag push), and any batched catalog
text fixes have shipped in one final propagation wave. Owner: the coordinator on
the Linux VM, in a scratch `HOME` with `PATH` reduced to system directories plus
`$HOME/.local/bin`: download the latest `datatug` archive by hand, verify its
checksum, run Journey steps 1–5 verbatim and capture output; then for each of the
nine released CLIs run `<cli> install --all --format json` and `<cli> version
--json` and compare matrix rows and probe sources. Owner: the founder on the Mac
with a Homebrew-installed host: `<host> install <cask target> --dry-run` and one
real cask install of a target not yet installed, including the unsigned ingitdb
cask. Record evidence in this task's notes; the coordinator then commits the
Feature's move to Stable in `cli-helpers`.

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

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
