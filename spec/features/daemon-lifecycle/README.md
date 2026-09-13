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

### REQ: consumer-owned-policy

The package MUST NOT choose daemon states, process launch behavior, stale-owner
rules, recovery actions, ports, or persistence schemas.

## Acceptance criteria

### AC: protected-lock-journey

**Given** a CLI creates a daemon state directory and lock file

**When** it protects, validates, locks, and unlocks them

**Then** validation succeeds, a competing handle cannot acquire the live lock,
and the same public API compiles on Unix and Windows

**And** Windows rejects foreign owners, missing directory inheritance, and
replaced lock-file directory entries.

**Requirements:** daemon-lifecycle#req:owner-only-state, daemon-lifecycle#req:cancellable-advisory-lock, daemon-lifecycle#req:consumer-owned-policy

## Open Questions

- Should a later package add a portable secure-open primitive, or should each
  daemon continue choosing its own create/no-follow policy?

---
*This document follows the https://specscore.md/feature-specification*
