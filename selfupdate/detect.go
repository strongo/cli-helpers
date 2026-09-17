package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
)

// InstallMethod classifies how the running binary reached its current
// location, which is the single fact that decides whether self-replace is
// ever attempted.
type InstallMethod int

const (
	// Managed means a package manager owns the binary; the package redirects
	// to that manager's upgrade command and never writes to the file. This
	// is the zero value, so a Detection nobody explicitly classified reads
	// as the most restrictive, never-self-replace case rather than silently
	// looking like an eligible Manual install.
	Managed InstallMethod = iota
	// Manual means the binary was placed by the user or by `go install` — a
	// release archive extracted by hand, or a GOBIN/GOPATH/bin target.
	// Self-replace is eligible.
	Manual
	// Ambiguous means the path matched neither a configured manager's layout
	// nor a plausible manual location. Per REQ: ambiguous-safe-default,
	// Ambiguous is a distinct outcome from Manual, not a fallback that
	// resolves to it — an unrecognized path is never treated as eligible for
	// self-replace.
	Ambiguous
)

// String renders the install method as a stable, lower_snake_case token
// suitable for machine-readable output, matching the convention Action and
// Verdict already follow.
func (m InstallMethod) String() string {
	switch m {
	case Managed:
		return "managed"
	case Manual:
		return "manual"
	case Ambiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}

// Detection is the result of classifying an executable path.
type Detection struct {
	// Method is how the binary was installed.
	Method InstallMethod
	// Manager identifies the owning package manager when Method is Managed;
	// nil for Manual and Ambiguous.
	Manager *Manager
	// Path is the resolved path that was classified — after following
	// symlinks when DetectSelf performed the resolution, or exactly the
	// input path when Classify was called directly (as the reference CLI's
	// --explain-path does).
	Path string
}

// Classify decides the install method purely from path, checking it against
// each manager's PathMarkers in order and returning the first match. It is
// case-insensitive and treats both '/' and '\' as path separators, so a
// Windows path (e.g. a Scoop or WinGet layout) can be classified on any
// host — which is what lets this package's own tests, and a consumer's
// --explain-path-style tooling, exercise every manager without running on
// that manager's platform.
//
// When no configured manager matches, Classify checks path against
// SystemPackageDirs(goosName, getenvFunc) — the HOST's own OS-package-
// manager directories, evaluated for goosName, the real GOOS this process is
// actually running on (never inferred from path's own shape) — and returns
// Managed with the built-in, redirect-only "system package manager" when it
// matches (REQ: system-package-dirs-are-managed); see SystemPackageDirs' own
// doc comment for why. This check runs regardless of what managers was
// passed, including nil, and always AFTER every configured manager, so a
// catalog manager whose own markers happen to match (e.g. Snap's "/snap/",
// or WinGet's machine-scope markers inside %ProgramFiles%) still takes
// precedence. Unlike the manager-marker loop above — which, being a pure
// substring match, classifies a Windows-shaped path the same way on any
// host — this check is host-relative by design: `--explain-path` of a
// Windows-shaped path run on a Linux host will NOT hit it (goosName is
// "linux" there, so the Windows list is never even consulted), the same way
// a manual `/opt/homebrew/...` path only hits Homebrew's markers because
// those are checked unconditionally. It still performs no filesystem or
// network access, so explainPath's "Classify is a pure function" claim (no
// I/O beyond reading this process's own environment) continues to hold.
//
// When neither a manager nor a system directory matches, a path ending in a
// `bin` directory, or containing a `go/bin` segment (a `go install` target
// under GOBIN or GOPATH/bin), is classified Manual. Anything else is
// Ambiguous: per REQ: ambiguous-safe-default, an unrecognized location never
// resolves to Manual, because that would make self-replace eligible for a
// binary the package cannot actually place.
func Classify(path string, managers []Manager) Detection {
	if det := ClassifyManagers(path, managers); det.Method == Managed {
		return det
	}

	p := normalizePath(path)

	if inSystemPackageDir(p) {
		m := systemPackageManagerFor(goosName)
		return Detection{Method: Managed, Manager: &m, Path: path}
	}

	if looksLikeManualInstall(p) {
		return Detection{Method: Manual, Path: path}
	}

	return Detection{Method: Ambiguous, Path: path}
}

// ClassifyManagers checks path against every manager's PathMarkers ONLY,
// skipping both the built-in system-package-directory check and the Manual/
// Ambiguous fallback that Classify itself performs on top of it — it
// returns Ambiguous, never Manual, for anything no marker matched, since
// "no manager matched" is not the same claim as "this looks like a manual
// install".
//
// It exists for a caller that must classify an UNRESOLVED, PATH-found path
// against catalog-manager dispatch markers only — cliinstall's own
// classifyForUpgrade is the reason this is exported: a Snap-dispatched
// binary (`/snap/bin/ingitdb`, itself a symlink to `/usr/bin/snap`) must be
// recognized as Snap-managed from its UNRESOLVED PATH entry, before symlink
// resolution obscures it, but the built-in system-directory check must see
// ONLY the resolved path — exactly as DetectSelf itself does, resolving
// symlinks first and classifying just the result — so that self-update and
// `upgrade <self>` reach the identical verdict for the identical binary
// (self-update#req:self-update-equals-upgrade-self). Applying the system-
// directory check to an unresolved PATH entry too would diverge from that:
// a shim at `/usr/bin/foo` symlinked out to a manual `/opt/foo/bin/foo`
// would classify Managed via the unresolved path here but Manual via
// DetectSelf, which only ever sees the resolved target.
func ClassifyManagers(path string, managers []Manager) Detection {
	p := normalizePath(path)
	for i := range managers {
		for _, marker := range managers[i].PathMarkers {
			if strings.Contains(p, normalizePath(marker)) {
				return Detection{Method: Managed, Manager: &managers[i], Path: path}
			}
		}
	}
	return Detection{Method: Ambiguous, Path: path}
}

// inSystemPackageDir reports whether normalizedPath (already run through
// normalizePath) lies inside one of the host's own SystemPackageDirs,
// boundary-aware so a sibling directory that merely starts with the same
// characters (normalizePath("/usr/binx") never matches normalizePath("/usr/
// bin")) is never mistaken for it. SystemPackageDirs never returns an empty
// entry (its Windows branch only appends a getenv result once it has
// confirmed that result is non-empty), so every dir here normalizes to a
// non-empty string.
func inSystemPackageDir(normalizedPath string) bool {
	for _, dir := range SystemPackageDirs(goosName, getenvFunc) {
		nd := normalizePath(dir)
		if normalizedPath == nd || strings.HasPrefix(normalizedPath, nd+"/") {
			return true
		}
	}
	return false
}

// normalizePath lowercases s and folds backslashes to forward slashes, so
// marker comparisons are both case- and separator-insensitive.
func normalizePath(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), `\`, "/")
}

// looksLikeManualInstall reports whether an already-normalized path is a
// plausible manual install location: a `go install` target, or a binary
// sitting directly inside any directory named `bin`.
func looksLikeManualInstall(normalized string) bool {
	if strings.Contains(normalized, "/go/bin/") {
		return true
	}
	dir := normalized
	if i := strings.LastIndex(normalized, "/"); i >= 0 {
		dir = normalized[:i]
	}
	return strings.HasSuffix(dir, "/bin")
}

// Test seams: overridable indirections for os/filepath calls so DetectSelf's
// error and symlink-fallback branches are exercisable without a real
// installed binary or a real symlink (REQ: no-network-in-tests extends to
// "no faking the test binary's own location" — these seams are how the
// package's tests satisfy that without ever calling os.Executable for real).
// getenvFunc is the same kind of seam for inSystemPackageDir's Windows
// environment-variable lookups (REQ: system-package-dirs-are-managed); it
// follows replace.go's goosName, which this package already overrides in
// tests to exercise Windows-only behavior from any host.
var (
	osExecutable     = os.Executable
	evalSymlinksFunc = filepath.EvalSymlinks
	getenvFunc       = os.Getenv
)

// DetectSelf resolves the running executable's path, following symlinks
// first — a Homebrew cask shim is typically a symlink into the Caskroom, and
// classifying the symlink itself instead of its target would miss the
// managed install entirely (REQ: detect-managed) — and classifies the
// result against c.Managers. When symlink resolution fails (the target
// doesn't exist, a permission error, or similar), classification falls back
// to the unresolved path rather than failing the whole call: a path that
// can't be resolved is still worth classifying as-is.
func (c Config) DetectSelf() (Detection, error) {
	exe, err := osExecutable()
	if err != nil {
		return Detection{}, err
	}
	resolved, err := evalSymlinksFunc(exe)
	if err != nil {
		resolved = exe
	}
	return Classify(resolved, c.Managers), nil
}
