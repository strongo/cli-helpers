package cliui

import (
	"fmt"
	"io"
	"strings"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// dateOnly returns rfc3339's date part, dropping any time-of-day and zone
// (cli-install#req:date-labelled-by-source: "show date (date part only)").
// A value with no "T" (already date-only, or empty) is returned unchanged.
func dateOnly(rfc3339 string) string {
	if i := strings.IndexByte(rfc3339, 'T'); i >= 0 {
		return rfc3339[:i]
	}
	return rfc3339
}

// dateLabel names date's source per cli-install#req:date-labelled-by-source:
// "built" for a link-time-stamped date, "committed" for one read from VCS
// commit metadata, "date" when the source cannot be known (a text-probe
// date) — it MUST NOT be called a release date. Returns "" alongside date's
// own "" when no date is known at all.
func dateLabel(dateSource string) string {
	switch dateSource {
	case "build":
		return "built"
	case "commit":
		return "committed"
	default:
		return "date"
	}
}

// shortCommit truncates commit to REQ: list-relevant's "short commit (7
// characters)", preserving a trailing "+dirty" marker
// (cli-install#req:version-json-contract) rather than truncating into it.
func shortCommit(commit string) string {
	if commit == "" {
		return ""
	}
	const dirtySuffix = "+dirty"
	dirty := strings.HasSuffix(commit, dirtySuffix)
	base := strings.TrimSuffix(commit, dirtySuffix)
	if len(base) > 7 {
		base = base[:7]
	}
	if dirty {
		return base + dirtySuffix
	}
	return base
}

// methodLabel renders s.Method/s.Manager as the one word REQ: list-relevant
// wants next to a located copy's path: the owning manager's name for a
// managed install, "direct" for a manual one, "ambiguous" otherwise.
func methodLabel(s cliinstall.Status) string {
	switch s.Method {
	case selfupdate.Managed:
		if s.Manager != nil {
			return s.Manager.Name
		}
		return "managed"
	case selfupdate.Manual:
		return "direct"
	default:
		return "ambiguous"
	}
}

// installMethodJSON renders s.Method for JSON the way s.Method is
// documented: "meaningful only when State is Installed or Unrecognized".
// selfupdate.InstallMethod's zero value is Managed, so s.Method.String()
// alone would render "managed" for a NotInstalled status that never
// classified anything — this returns "" instead, so the field's
// "omitempty" tag drops it, the same as VersionSource's own zero value
// already does for VersionSourceNone.
func installMethodJSON(s cliinstall.Status) string {
	if s.State == cliinstall.NotInstalled {
		return ""
	}
	return s.Method.String()
}

func pluralCopies(n int) string {
	if n == 1 {
		return "copy"
	}
	return "copies"
}

// statusSummary renders s as the one-line status REQ: list-relevant and
// REQ: details-before-install both show: the state, and — for a located
// copy — its path, install method and additional-copy count, plus, for an
// installed copy, its version, labelled date and short commit.
func statusSummary(s cliinstall.Status) string {
	switch s.State {
	case cliinstall.NotInstalled:
		return "not installed"
	case cliinstall.Unrecognized:
		line := fmt.Sprintf("unrecognized copy at %s (%s)", s.Path, methodLabel(s))
		if n := len(s.OtherPaths); n > 0 {
			line += fmt.Sprintf("; %d other %s", n, pluralCopies(n))
		}
		return line
	case cliinstall.Installed:
		var parts []string
		if s.Version != "" {
			parts = append(parts, "installed v"+s.Version)
		} else {
			parts = append(parts, "installed")
		}
		if s.Date != "" {
			parts = append(parts, dateLabel(s.DateSource)+" "+dateOnly(s.Date))
		}
		if c := shortCommit(s.Commit); c != "" {
			parts = append(parts, c)
		}
		line := strings.Join(parts, ", ")
		line += fmt.Sprintf(" at %s (%s)", s.Path, methodLabel(s))
		if n := len(s.OtherPaths); n > 0 {
			line += fmt.Sprintf("; %d other %s", n, pluralCopies(n))
		}
		return line
	default:
		return "unknown"
	}
}

// dedupedWarnings unions row.Status.Warnings with row.Plan.Warnings (when
// Plan is set), preserving first-seen order and dropping duplicates — some
// Result-building paths already fold Status.Warnings into their own
// Warnings, others (a dry run's) do not, so a reader that wants every
// warning exactly once needs both sources merged rather than a pick of one.
func dedupedWarnings(row Row) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(list []string) {
		for _, w := range list {
			if seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	add(row.Status.Warnings)
	if row.Plan != nil {
		add(row.Plan.Warnings)
	}
	return out
}

// writeWarnings writes every row's deduplicated warnings to errOut, one per
// line, prefixed with the target's own id — REQ: machine-readable-output's
// "progress and warnings on stderr", applied uniformly to text output too.
func writeWarnings(errOut io.Writer, rows []Row) {
	for _, row := range rows {
		name := RowName(row)
		for _, w := range dedupedWarnings(row) {
			fmt.Fprintf(errOut, "install: warning: %s: %s\n", name, w) //nolint:errcheck
		}
	}
}
