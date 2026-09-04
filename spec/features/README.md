---
format: https://specscore.md/features-index-specification
---

# Features

Feature specifications for this project.

## Index

| Feature | Status | Description |
|---------|--------|-------------|
| [Self-Update Library](self-update/README.md) | Stable | `github.com/strongo/cli-helpers/selfupdate` lets any Go CLI update its own binary in place. It decides how the running binary was installed: a package-manager-owned install is never overwritten directly — the caller is told the exact upgrade command by default, or may explicitly opt in to run that manager command as structured argv — while a manual install (release archive, `go install`) is replaced by downloading the release asset for the host platform, verifying its sha256 against the release checksums, and atomically swapping the executable. A check-only mode reports availability without touching anything, and an explicit version pin installs an exact release. |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/features-index-specification*
