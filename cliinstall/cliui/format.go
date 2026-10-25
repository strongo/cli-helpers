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
// managed install, "manual" otherwise (or "ambiguous"). "manual" is
// selfupdate.InstallMethod's OWN word — this used to say "direct" here,
// which meant text and JSON disagreed about the exact same fact (task-5
// review M5); using the library's own vocabulary in both places removes
// that disagreement rather than inventing a synonym.
func methodLabel(s cliinstall.Status) string {
	switch s.Method {
	case selfupdate.Managed:
		if s.Manager != nil {
			return s.Manager.Name
		}
		return "managed"
	case selfupdate.Manual:
		return "manual"
	default:
		return "ambiguous"
	}
}

// versionLabel renders a status/plan version for text output: "v1.2.3" for
// an ordinary release version, but the bare token unprefixed when it isn't
// one — "dev", "unknown", "(devel)" and similar undetermined placeholders
// (cli-install#req:version-json-contract's own "dev when the build cannot
// determine it") read as "installed vdev" with an unconditional "v" prefix,
// which looks like a typo, not a deliberate placeholder (task-5 review
// M5). A leading digit is treated as an ordinary version; anything else is
// shown as-is.
func versionLabel(v string) string {
	if v == "" {
		return ""
	}
	if v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
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

// statusSummary renders s as the status line REQ: list-relevant and
// REQ: details-before-install both show: the state, and — for a located
// copy — its path, install method and additional-copy count, plus, for an
// installed copy, its version, labelled date and short commit. An
// unrecognized copy with observed output carries a second, indented
// "Output:" line (cli-install#req:status-probe-order: "reported with its
// path and the output that was seen" — task-5 review S4). An unrecognized
// copy's method reads "layout: <word>", not a bare "(<word>)": an
// unrecognized copy is never trusted or verified
// (cli-install#req:unrecognized-copy-not-trusted), and "(manual)" alone
// reads like a trust verdict this package never gave it — "layout:" makes
// clear the word only classifies the PATH's shape, nothing about what runs
// there (task-5 review M5).
func statusSummary(s cliinstall.Status) string {
	switch s.State {
	case cliinstall.NotInstalled:
		return "not installed"
	case cliinstall.Unrecognized:
		line := fmt.Sprintf("unrecognized copy at %s (layout: %s)", s.Path, methodLabel(s))
		if n := len(s.OtherPaths); n > 0 {
			line += fmt.Sprintf("; %d other %s", n, pluralCopies(n))
		}
		if s.Output != "" {
			line += "\n  Output: " + firstLine(s.Output, outputPreviewLimit)
		}
		return line
	case cliinstall.Installed:
		var parts []string
		if v := versionLabel(s.Version); v != "" {
			parts = append(parts, "installed "+v)
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

// outputPreviewLimit bounds how much of an unrecognized copy's observed
// output a text row shows inline — enough to identify what actually
// answered without letting a chatty or malformed binary's output swamp the
// listing (cli-install#req:status-probe-order: "reported with its path and
// the output that was seen" — task-5 review S4).
const outputPreviewLimit = 160

// firstLine returns s's first line, truncated to at most limit runes with
// a trailing ellipsis when either the line or s itself was longer.
func firstLine(s string, limit int) string {
	truncatedMultiline := false
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
		truncatedMultiline = true
	}
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit]) + "..."
	}
	if truncatedMultiline {
		return s + "..."
	}
	return s
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
