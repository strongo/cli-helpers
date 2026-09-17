---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Daemon Lifecycle Primitives

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle?op=explore) | [Edit](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle?op=edit) | [Ask question](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle?op=ask) | [Request change](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Purpose

CLI daemons repeatedly need the same small, security-sensitive OS layer. This
feature provides it once while leaving product-specific process, state, and
recovery decisions in the consuming CLI.

### REQ: owner-only-state

`ProtectOwnerOnly` MUST make a regular file or directory accessible only to the
current user. On Windows it MUST install a protected DACL with one current-user
allow entry, then reassert the current user as owner. The DACL update MUST grant
the owner-write right before the owner update so inherited administrative-group
ownership does not require an elevated token. It MUST NOT treat Unix mode bits
as proof of Windows privacy.

`ValidateOwnerOnly` MUST reject symlinks, special files, other owners, group or
world Unix permissions, Windows ACLs that grant another principal access,
directories whose private ACL is not inherited by children, and an open handle
whose directory entry was replaced after opening.

### REQ: cancellable-advisory-lock

`TryLock` MUST attempt one non-blocking exclusive advisory lock. `Lock` MUST
retry without busy-spinning until it succeeds or its context is cancelled.
`Unlock` MUST release the same file-handle lock on Unix and Windows.

### REQ: detached-start

`ConfigureDetached` MUST start a child in a new session on Unix, and on Windows
with `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_BREAKAWAY_FROM_JOB`
and a hidden window.

`StartDetached` MUST start a fresh copy of the caller's command (path,
arguments, environment and working directory only) configured that way, with
stdin from the null device and stdout and stderr to a caller-provided log file.
It MUST NOT let the child inherit any other handle, so a caller whose own
stdout is a pipe reaches EOF when it exits while the child keeps running. On
Windows, when creation with breakaway fails with `ERROR_ACCESS_DENIED`, it MUST
retry once without `CREATE_BREAKAWAY_FROM_JOB`. It MUST reject a command that
sets its own standard streams, extra files, system process attributes, or a
context, `Cancel` or `WaitDelay` (a detached child does not follow them), and
MUST return the started process so the caller can observe an early exit.

### REQ: process-identity

`ProcessIdentity` MUST return an opaque token that is equal across calls for
the life of a running process and differs for a later process that reuses the
pid, on Linux, macOS and Windows without cgo. It MUST be built only from values
the kernel records once, so wall-clock steps cannot change it: on Linux the
boot id and the raw start time in clock ticks (never converted through
`btime`), on macOS the kernel start timeval, on Windows the creation FILETIME.
It MUST report an exited or unknown pid, including an unreaped zombie and a
Windows process that exited with code 259, as `ErrProcessNotFound`.

`TerminateIfSameProcess` MUST forcibly terminate a pid only when its current
identity equals the recorded one, and otherwise MUST send nothing and return
`ErrProcessMismatch` or `ErrProcessNotFound`. On Windows the check and the
termination MUST use the same process handle.

### REQ: consumer-owned-policy

The package MUST NOT choose daemon states, readiness protocols, start or stop
timeouts, stale-owner rules, recovery actions, ports, or persistence schemas.
It supplies launch and identity mechanics only.

## Acceptance criteria

### AC: protected-lock-journey

**Given** a CLI creates a daemon state directory and lock file

**When** it protects, validates, locks, and unlocks them

**Then** validation succeeds, a competing handle cannot acquire the live lock,
and the same public API compiles on Unix and Windows

**And** Windows rejects foreign owners, missing directory inheritance, and
replaced lock-file directory entries.

**Requirements:** daemon-lifecycle#req:owner-only-state, daemon-lifecycle#req:cancellable-advisory-lock, daemon-lifecycle#req:consumer-owned-policy

### AC: detached-start-journey

**Given** a test-only probe daemon that serves `whoami` and a shutdown endpoint
guarded by a secret file, on Linux, macOS and Windows runners

**When** a probe `start` command whose stdout is a pipe starts it with
`StartDetached`, waits for `whoami`, prints one readiness line and exits

**Then** the reader of the pipe sees EOF within 2 s of the readiness line, the
detached child holds no descriptor for that pipe (listed on Linux), a fresh
process finds the probe answering `whoami`, a shutdown with a wrong secret is
refused, and an authenticated shutdown frees the port and ends the process

**And** on Windows, inside a job object that forbids breakaway, `StartDetached`
still starts the child, which stays in that job.

**Requirements:** daemon-lifecycle#req:detached-start, daemon-lifecycle#req:consumer-owned-policy

### AC: reused-pid-never-signalled

**Given** a running process and its token from `ProcessIdentity`

**When** the token is read again, compared with another process's token, and
`TerminateIfSameProcess` is called with that pid and any other token, then with
the matching one

**Then** repeated reads are equal and the two processes' tokens differ, every
mismatched call returns `ErrProcessMismatch` and the process keeps running, the
matching call terminates it, and afterwards both functions report
`ErrProcessNotFound`

**And** on Linux the token does not change when `btime` changes.

**Requirements:** daemon-lifecycle#req:process-identity

## Open Questions

- Should a later package add a portable secure-open primitive, or should each
  daemon continue choosing its own create/no-follow policy?

---
*This document follows the https://specscore.md/feature-specification*
