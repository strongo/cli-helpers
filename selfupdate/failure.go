package selfupdate

import (
	"errors"
	"fmt"
)

// FailureKind is a machine-checkable classification of why Update or Check
// failed. REQ: host-owned-exit-codes exists precisely so each consumer can
// switch on this and map it onto its own exit codes — including two
// consumers that disagree about what a given situation should cost, which
// this type does not adjudicate.
type FailureKind int

const (
	// KindAmbiguous means the install method could not be classified.
	KindAmbiguous FailureKind = iota
	// KindReleaseLookup means the GitHub releases listing could not be
	// fetched or decoded (network error, rate limit, malformed response).
	KindReleaseLookup
	// KindDownload means fetching a release asset or its checksums file
	// failed for a reason other than the asset simply not existing (that
	// case is KindUnknownTag).
	KindDownload
	// KindChecksum means the downloaded asset's sha256 did not match the
	// release's checksums file, or no checksum entry could be found for it.
	// This always occurs before extraction (REQ: checksum-before-extract).
	KindChecksum
	// KindPermission means the replacement failed because the process
	// lacks permission to write the install location. Path is always set.
	KindPermission
	// KindNonInteractive means a self-replace needed confirmation, the
	// caller did not skip it, and no interactive terminal was available to
	// ask (REQ: non-interactive-refusal). The core package never produces
	// this itself — it is intended for an Options.Confirm implementation
	// (typically the cobracmd adapter) to return, so the typed kind still
	// reaches the caller through the normal Update error path.
	KindNonInteractive
	// KindDowngrade means a pinned target was strictly older than the
	// running version and AllowDowngrade was not set.
	KindDowngrade
	// KindUnknownTag means a pinned version matched no published release, or
	// the matched release has no asset for the host platform.
	KindUnknownTag
	// KindUnsupportedPlatform means the host GOOS/GOARCH is not in
	// Config.SupportedPlatforms.
	KindUnsupportedPlatform
	// KindUnexpected is anything else: a staging/rename failure that isn't a
	// permission error, a failure resolving the running executable's own
	// path, or an error returned from an Options.Confirm callback that
	// wasn't already a *Failure.
	KindUnexpected
	// KindManagedVersion means a version pin cannot be proven to match the
	// latest release an executable package-manager update would install, or
	// was requested from a redirect-only manager.
	KindManagedVersion
	// KindManagedCommand means the executable manager runner or its required
	// configuration failed. The underlying process error remains unwrap-able.
	KindManagedCommand
)

// String renders the kind as a stable, lower_snake_case token suitable for
// machine-readable output.
func (k FailureKind) String() string {
	switch k {
	case KindAmbiguous:
		return "ambiguous"
	case KindReleaseLookup:
		return "release_lookup"
	case KindDownload:
		return "download"
	case KindChecksum:
		return "checksum"
	case KindPermission:
		return "permission"
	case KindNonInteractive:
		return "non_interactive"
	case KindDowngrade:
		return "downgrade"
	case KindUnknownTag:
		return "unknown_tag"
	case KindUnsupportedPlatform:
		return "unsupported_platform"
	case KindUnexpected:
		return "unexpected"
	case KindManagedVersion:
		return "managed_version"
	case KindManagedCommand:
		return "managed_command"
	default:
		return "unknown"
	}
}

// Failure is the error type every failure path from Config.Update and
// Config.Check returns. It carries a typed Kind a caller can switch on
// without string-matching, plus the executable Path when the failure is
// path-specific (REQ: permission-failure-identifiable) and the underlying
// error for logging or wrapping.
type Failure struct {
	Kind FailureKind
	// Path is the executable path involved, when applicable (set for
	// KindPermission and KindAmbiguous). Empty otherwise.
	Path string
	// Err is the underlying error, always non-nil.
	Err error
}

// Error satisfies the error interface. The Kind is deliberately not part of
// the message — String() exists for callers that want it, and baking it
// into Error() would pressure every caller into parsing a "kind: message"
// convention instead of using KindOf.
func (f *Failure) Error() string {
	if f.Path != "" {
		return fmt.Sprintf("%s: %v", f.Path, f.Err)
	}
	return f.Err.Error()
}

// Unwrap exposes the underlying error to errors.Is/errors.As, e.g. so a
// caller can check errors.Is(err, fs.ErrPermission) in addition to (or
// instead of) checking Kind.
func (f *Failure) Unwrap() error { return f.Err }

// KindOf returns err's FailureKind when err is (or wraps) a *Failure, and
// KindUnexpected otherwise — including when err is nil, so a caller does not
// need a separate nil check before branching on the kind of a definitely-
// non-nil error.
func KindOf(err error) FailureKind {
	var f *Failure
	if errors.As(err, &f) {
		return f.Kind
	}
	return KindUnexpected
}
