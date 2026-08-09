package selfupdate

import (
	"context"
	"strings"
)

// Verdict is the outcome of comparing the running build against the latest
// stable release.
type Verdict int

const (
	// UpToDate means the current version equals the latest stable release.
	UpToDate Verdict = iota
	// UpdateAvailable means a newer stable release exists.
	UpdateAvailable
	// Undetermined means the current version is one of Config's
	// UndeterminedVersions (e.g. an unstamped local build) and so cannot be
	// meaningfully compared at all — it is reported as neither up to date
	// nor available, per REQ: undetermined-version.
	Undetermined
)

// String renders v the way a consumer's machine-readable output (e.g.
// cobracmd's --format json) is expected to spell it: a stable,
// snake_case token rather than Go's default numeric %v.
func (v Verdict) String() string {
	switch v {
	case UpToDate:
		return "up_to_date"
	case UpdateAvailable:
		return "update_available"
	case Undetermined:
		return "undetermined"
	default:
		return "unknown"
	}
}

// CheckResult captures the comparison between the running build and the
// latest stable release. Current and Latest are both normalized (no leading
// "v") except when Verdict is Undetermined, in which case Current is
// reported exactly as configured (e.g. "dev") since it is not a version at
// all.
type CheckResult struct {
	Current string
	Latest  string
	Verdict Verdict
}

// Check reports whether a newer stable release is available without
// downloading or writing anything — the read-only counterpart to Update,
// usable on its own (a "--check" flag) or before deciding whether to call
// Update at all.
func (c Config) Check(ctx context.Context) (CheckResult, error) {
	cfg := c.withDefaults()

	latestTag, err := cfg.latestStableTag(ctx)
	if err != nil {
		return CheckResult{}, &Failure{Kind: KindReleaseLookup, Err: err}
	}
	latest := normalize(latestTag)

	if cfg.isUndetermined(cfg.CurrentVersion) {
		return CheckResult{Current: cfg.CurrentVersion, Latest: latest, Verdict: Undetermined}, nil
	}

	current := normalize(cfg.CurrentVersion)
	verdict := UpdateAvailable
	if CompareVersions(current, latest) == 0 {
		verdict = UpToDate
	}
	return CheckResult{Current: current, Latest: latest, Verdict: verdict}, nil
}

// normalize strips a single leading "v" from a version string, so "v1.2.3"
// and "1.2.3" compare and display identically.
func normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// CompareVersions orders two semver-ish version strings, returning -1 if
// a < b, 0 if they are equal, and +1 if a > b. A leading "v" is ignored.
// Comparison is by numeric major/minor/patch; a suffix after the first "-"
// (a prerelease, or a Go pseudo-version's "0.yyyymmddhhmmss-abcdef123456")
// sorts below the same core version without one, per semver — which is what
// makes a Go pseudo-version compare as "below its eventual release" rather
// than as an undetermined or unrelated value (REQ: undetermined-version).
// This is a minimal comparison sufficient for the self-update downgrade
// guard and stable-release ordering, not a full semver implementation (it
// does not, for instance, order multi-field prerelease identifiers
// numerically per the full semver spec).
func CompareVersions(a, b string) int {
	ac, apre := splitVersion(a)
	bc, bpre := splitVersion(b)

	for i := 0; i < 3; i++ {
		if ac[i] != bc[i] {
			if ac[i] < bc[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == "" && bpre == "":
		return 0
	case apre == "" && bpre != "":
		return 1
	case apre != "" && bpre == "":
		return -1
	default:
		return strings.Compare(apre, bpre)
	}
}

// splitVersion parses a version string into its numeric [major, minor,
// patch] and any suffix after the first "-". Missing or non-numeric
// components are treated as 0, so a malformed input degrades to "0.0.0" plus
// whatever text followed the first "-" rather than panicking or erroring —
// version comparison has no error return, so it must never crash on
// attacker- or typo-controlled input like a --version pin.
func splitVersion(v string) ([3]int, string) {
	v = normalize(v)
	var pre string
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	var core [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int(r-'0')
		}
		core[i] = n
	}
	return core, pre
}
