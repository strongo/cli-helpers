package cliinstall

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/strongo/buildinfo"
)

// identifyResult is identify's internal, pre-Status-shaped outcome: either
// a confirmed match (matched), a hard timeout that aborts the whole probe
// (timedOut), or neither, meaning no step confirmed this target's identity
// (Unrecognized) and output carries whatever was last seen for display.
type identifyResult struct {
	matched  bool
	timedOut bool

	version    string
	commit     string
	date       string
	dateSource string
	source     VersionSource

	output string
}

// identify runs the cli-install#req:status-probe-order steps against path
// in order, stopping at the first one that yields target.ID: (1)
// `version --json` decoded into buildinfo.VersionJSON, whose Name must
// equal target.ID; (2) plain `version` text, either
// "<name> <version> (<commit>) <date>" (tolerating "@" before date) or the
// "wb form" of "<name> <version>" followed by "revision:" and "built:"
// lines; (3) `--version` printing one version token, accepted only when
// target declares a matching LegacyVersionSignatures entry. A step whose
// parsed name differs from target.ID is discarded, not treated as a match,
// and probing continues to the next step. ctx's deadline covers every step
// combined (cli-install#req:status-probe-bounded): once it is exceeded,
// identify stops immediately and reports timedOut regardless of which step
// was running.
func identify(ctx context.Context, env Env, path string, target Entry) identifyResult {
	var res identifyResult

	// Step 1: version --json.
	out, timedOut := runProbeStep(ctx, env, path, []string{"version", "--json"})
	if timedOut {
		res.timedOut = true
		return res
	}
	res.captureOutput(out)
	var vj buildinfo.VersionJSON
	if json.Unmarshal(out, &vj) == nil && vj.Name == target.ID {
		res.matched = true
		res.version = normalizeNoneUnknown(vj.Version)
		res.commit = normalizeNoneUnknown(vj.Commit)
		res.date = normalizeNoneUnknown(vj.Date)
		res.dateSource = vj.DateSource
		res.source = VersionSourceJSON
		return res
	}

	// Step 2: plain version text.
	out, timedOut = runProbeStep(ctx, env, path, []string{"version"})
	if timedOut {
		res.timedOut = true
		return res
	}
	res.captureOutput(out)
	if name, version, commit, date, ok := parseVersionText(string(out)); ok && name == target.ID {
		res.matched = true
		res.version = normalizeVersionToken(version)
		res.commit = normalizeNoneUnknown(commit)
		res.date = normalizeNoneUnknown(date)
		res.source = VersionSourceText
		return res
	}

	// Step 3: --version, only against a declared legacy signature.
	out, timedOut = runProbeStep(ctx, env, path, []string{"--version"})
	if timedOut {
		res.timedOut = true
		return res
	}
	res.captureOutput(out)
	if token, ok := parseSingleVersionToken(string(out)); ok && matchesLegacySignature(target, token) {
		res.matched = true
		res.version = normalizeVersionToken(token)
		res.source = VersionSourceFlag
		return res
	}

	return res
}

// captureOutput keeps the trimmed text of out as the diagnostic Output a
// caller shows for an Unrecognized copy, overwriting whatever an earlier
// step captured — the latest step to actually produce output is the most
// informative one to show, since any earlier step that could have matched
// already returned. A step that produced no output leaves a previous
// capture untouched.
func (r *identifyResult) captureOutput(out []byte) {
	if text := strings.TrimSpace(string(out)); text != "" {
		r.output = text
	}
}

// runProbeStep runs one status-probe-order step and reports whether ctx's
// budget was exhausted, which takes priority over whatever env.Run
// returned: once the deadline passes, the running process is killed
// (Env.Run's contract) and the whole probe is abandoned rather than
// continuing to a step that could not have been given its full budget.
// env.Run is required (a nil Run panics, like calling any nil func value);
// DefaultEnv always sets it.
func runProbeStep(ctx context.Context, env Env, path string, args []string) (out []byte, timedOut bool) {
	out, _ = env.Run(ctx, path, args)
	return out, ctx.Err() != nil
}

// parseVersionText parses a plain `version` subcommand's output against
// REQ: status-probe-order's step 2 shapes, returning ok=false when neither
// matches.
func parseVersionText(s string) (name, version, commit, date string, ok bool) {
	// splitLines always returns at least one element (strings.Split never
	// returns an empty slice), so lines[0] below is always safe to index.
	lines := splitLines(s)
	first := strings.TrimSpace(lines[0])
	if first == "" {
		return "", "", "", "", false
	}

	if n, v, c, d, matched := parseInlineVersionLine(first); matched {
		return n, v, c, d, true
	}

	// The "wb form": "<name> <version>" alone on the first line, with
	// "revision:" and "built:" lines somewhere after it.
	fields := strings.Fields(first)
	if len(fields) == 2 {
		commitVal, hasRevision := findLabeledLine(lines[1:], "revision:")
		dateVal, hasBuilt := findLabeledLine(lines[1:], "built:")
		if hasRevision && hasBuilt {
			return fields[0], fields[1], commitVal, dateVal, true
		}
	}

	return "", "", "", "", false
}

// parseInlineVersionLine parses "<name> <version> (<commit>) <date>",
// tolerating a "@" immediately before date.
func parseInlineVersionLine(line string) (name, version, commit, date string, ok bool) {
	open := strings.Index(line, "(")
	closeIdx := strings.Index(line, ")")
	if open < 0 || closeIdx < 0 || closeIdx < open {
		return "", "", "", "", false
	}
	head := strings.Fields(strings.TrimSpace(line[:open]))
	if len(head) != 2 {
		return "", "", "", "", false
	}
	commit = strings.TrimSpace(line[open+1 : closeIdx])
	date = strings.TrimSpace(line[closeIdx+1:])
	date = strings.TrimSpace(strings.TrimPrefix(date, "@"))
	if date == "" {
		return "", "", "", "", false
	}
	return head[0], head[1], commit, date, true
}

// findLabeledLine returns the trimmed value after the first line (case-
// insensitively) prefixed with label, e.g. "revision:" or "built:".
func findLabeledLine(lines []string, label string) (value string, found bool) {
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(strings.ToLower(trimmed), label) {
			return strings.TrimSpace(trimmed[len(label):]), true
		}
	}
	return "", false
}

// parseSingleVersionToken reports the sole whitespace-separated token of
// s's only non-blank line, and false when s has no lines, more than one
// non-blank line, or more than one token on that line — REQ: status-probe-
// order step 3's "printing one version token".
func parseSingleVersionToken(s string) (token string, ok bool) {
	nonEmpty := 0
	for _, l := range splitLines(s) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		nonEmpty++
		token = l
	}
	if nonEmpty != 1 {
		return "", false
	}
	fields := strings.Fields(token)
	if len(fields) != 1 {
		return "", false
	}
	return fields[0], true
}

// matchesLegacySignature reports whether token exactly matches one of
// target's declared LegacyVersionSignatures.
func matchesLegacySignature(target Entry, token string) bool {
	for _, sig := range target.LegacyVersionSignatures {
		if sig == token {
			return true
		}
	}
	return false
}

// splitLines splits s on "\n" and strips a trailing "\r" from each line,
// tolerating CRLF output.
func splitLines(s string) []string {
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		lines = append(lines, strings.TrimRight(l, "\r"))
	}
	return lines
}

// normalizeNoneUnknown reads a text-probe "none" or "unknown" token
// (case-insensitively) as empty (REQ: status-probe-order: "`none` and
// `unknown` commit or date values MUST be read as empty").
func normalizeNoneUnknown(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.EqualFold(trimmed, "none") || strings.EqualFold(trimmed, "unknown") {
		return ""
	}
	return trimmed
}

// normalizeVersionToken strips a single leading "v", mirroring
// selfupdate's own (unexported) normalize helper, so "v1.2.3" and "1.2.3"
// read identically regardless of which probe step produced them.
func normalizeVersionToken(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}
