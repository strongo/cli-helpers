package cliinstall

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/strongo/buildinfo"
)

func runFunc(steps map[string]func() ([]byte, error)) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, _ string, args []string) ([]byte, error) {
		key := ""
		for i, a := range args {
			if i > 0 {
				key += " "
			}
			key += a
		}
		if f, ok := steps[key]; ok {
			return f()
		}
		return nil, errors.New("unexpected step " + key)
	}
}

func ok(b []byte) func() ([]byte, error) {
	return func() ([]byte, error) { return b, nil }
}

// --- identify: step 1 (version --json) --------------------------------

func TestIdentify_Step1_Matches(t *testing.T) {
	target := Entry{ID: "foo"}
	vj := buildinfo.VersionJSON{Name: "foo", Version: "1.2.3", Commit: "abc+dirty", Date: "2026-01-01T00:00:00Z", DateSource: buildinfo.DateSourceCommit}
	b, _ := json.Marshal(vj)
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok(b),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if !res.matched || res.source != VersionSourceJSON {
		t.Fatalf("res = %+v, want matched via VersionSourceJSON", res)
	}
	if res.version != "1.2.3" || res.commit != "abc+dirty" || res.date != vj.Date || res.dateSource != buildinfo.DateSourceCommit {
		t.Errorf("res = %+v", res)
	}
}

func TestIdentify_Step1_NoneUnknownNormalized(t *testing.T) {
	target := Entry{ID: "foo"}
	vj := buildinfo.VersionJSON{Name: "foo", Version: "1.0.0", Commit: "none", Date: "unknown", DateSource: ""}
	b, _ := json.Marshal(vj)
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok(b),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.commit != "" || res.date != "" {
		t.Errorf("res = %+v, want commit/date normalized to empty", res)
	}
}

func TestIdentify_Step1_EmptyName_FallsThrough(t *testing.T) {
	target := Entry{ID: "foo"}
	b, _ := json.Marshal(buildinfo.VersionJSON{Version: "1.0.0"})
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok(b),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched (empty name falls through)", res)
	}
}

func TestIdentify_Step1_NameMismatch_FallsThrough(t *testing.T) {
	target := Entry{ID: "foo"}
	b, _ := json.Marshal(buildinfo.VersionJSON{Name: "bar", Version: "1.0.0"})
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok(b),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched", res)
	}
}

func TestIdentify_Step1_InvalidJSON_FallsThroughAndCapturesOutput(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("not json at all")),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched", res)
	}
	if res.output != "not json at all" {
		t.Errorf("output = %q, want step 1's captured text", res.output)
	}
}

// --- identify: step 2 (plain version text) ------------------------------

func TestIdentify_Step2_InlineForm_Matches(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("foo 1.2.3 (abcdef) 2026-01-01T00:00:00Z\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if !res.matched || res.source != VersionSourceText {
		t.Fatalf("res = %+v, want matched via VersionSourceText", res)
	}
	if res.version != "1.2.3" || res.commit != "abcdef" || res.date != "2026-01-01T00:00:00Z" {
		t.Errorf("res = %+v", res)
	}
	if res.dateSource != "" {
		t.Errorf("dateSource = %q, want empty for a text probe", res.dateSource)
	}
}

func TestIdentify_Step2_InlineForm_AtBeforeDate(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("foo 1.2.3 (abcdef) @2026-01-01T00:00:00Z\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if !res.matched || res.date != "2026-01-01T00:00:00Z" {
		t.Fatalf("res = %+v, want date with '@' stripped", res)
	}
}

func TestIdentify_Step2_InlineForm_NoneUnknownNormalized(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("foo 1.2.3 (none) unknown\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if !res.matched || res.commit != "" || res.date != "" {
		t.Fatalf("res = %+v, want commit/date normalized to empty", res)
	}
}

func TestIdentify_Step2_InlineForm_NameMismatch_FallsThrough(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("othercli 1.2.3 (abcdef) 2026-01-01T00:00:00Z\n")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched", res)
	}
}

func TestIdentify_Step2_WbForm_Matches(t *testing.T) {
	target := Entry{ID: "wb"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("wb 0.72.2\nrevision: 92c5a01\nbuilt: 2026-08-01T00:00:00Z\n")),
	})}
	res := identify(context.Background(), env, "/bin/wb", target)
	if !res.matched || res.source != VersionSourceText {
		t.Fatalf("res = %+v, want matched via the wb form", res)
	}
	if res.version != "0.72.2" || res.commit != "92c5a01" || res.date != "2026-08-01T00:00:00Z" {
		t.Errorf("res = %+v", res)
	}
}

func TestIdentify_Step2_WbForm_OrderIndependentAndCaseInsensitiveLabels(t *testing.T) {
	target := Entry{ID: "wb"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("wb 0.72.2\nBuilt: 2026-08-01T00:00:00Z\nRevision: 92c5a01\n")),
	})}
	res := identify(context.Background(), env, "/bin/wb", target)
	if !res.matched || res.commit != "92c5a01" || res.date != "2026-08-01T00:00:00Z" {
		t.Fatalf("res = %+v", res)
	}
}

func TestIdentify_Step2_WbForm_MissingBuiltLine_FallsThrough(t *testing.T) {
	target := Entry{ID: "wb"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("wb 0.72.2\nrevision: 92c5a01\n")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/wb", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched (no built: line)", res)
	}
}

func TestIdentify_Step2_EmptyOutput_FallsThrough(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched", res)
	}
}

func TestIdentify_Step2_SingleFieldFirstLine_FallsThrough(t *testing.T) {
	target := Entry{ID: "foo"}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("just-one-token\n")),
		"--version":      ok([]byte("")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched", res)
	}
}

// --- identify: step 3 (--version + legacy signature) ---------------------

func TestIdentify_Step3_MatchesDeclaredSignature(t *testing.T) {
	target := Entry{ID: "foo", LegacyVersionSignatures: []string{"v1.0.0", "1.0.0-legacy"}}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("v1.0.0\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if !res.matched || res.source != VersionSourceFlag {
		t.Fatalf("res = %+v, want matched via VersionSourceFlag", res)
	}
	if res.version != "1.0.0" {
		t.Errorf("version = %q, want leading v stripped", res.version)
	}
	if res.commit != "" || res.date != "" {
		t.Errorf("res = %+v, want no commit/date from a flag-only probe", res)
	}
}

func TestIdentify_Step3_NoDeclaredSignature_Unrecognized(t *testing.T) {
	target := Entry{ID: "foo"} // no LegacyVersionSignatures
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("1.0.0\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched (no declared signature)", res)
	}
	if res.output != "1.0.0" {
		t.Errorf("output = %q, want the flag step's output retained", res.output)
	}
}

func TestIdentify_Step3_MultiTokenOutput_Unrecognized(t *testing.T) {
	target := Entry{ID: "foo", LegacyVersionSignatures: []string{"1.0.0 extra"}}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("1.0.0 extra\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched (not a single token)", res)
	}
}

func TestIdentify_Step3_MultiLineOutput_Unrecognized(t *testing.T) {
	target := Entry{ID: "foo", LegacyVersionSignatures: []string{"1.0.0"}}
	env := Env{Run: runFunc(map[string]func() ([]byte, error){
		"version --json": ok([]byte("")),
		"version":        ok([]byte("")),
		"--version":      ok([]byte("1.0.0\nextra line\n")),
	})}
	res := identify(context.Background(), env, "/bin/foo", target)
	if res.matched {
		t.Fatalf("res = %+v, want unmatched (more than one non-blank line)", res)
	}
}

// --- identify: timeout ------------------------------------------------------

func TestIdentify_Timeout_StopsImmediately(t *testing.T) {
	target := Entry{ID: "foo"}
	calls := 0
	env := Env{Run: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res := identify(ctx, env, "/bin/foo", target)
	if !res.timedOut {
		t.Fatalf("res = %+v, want timedOut", res)
	}
	if res.matched {
		t.Errorf("res.matched = true, want false on timeout")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want exactly 1 (no further steps after a timeout)", calls)
	}
}

func TestIdentify_TimeoutOnStep2(t *testing.T) {
	target := Entry{ID: "foo"}
	calls := 0
	env := Env{Run: func(ctx context.Context, _ string, args []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(""), nil // step 1: no match, returns immediately
		}
		<-ctx.Done() // step 2: hangs until the shared budget expires
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res := identify(ctx, env, "/bin/foo", target)
	if !res.timedOut {
		t.Fatalf("res = %+v, want timedOut", res)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want exactly 2 (stopped after step 2's timeout)", calls)
	}
}

func TestIdentify_TimeoutOnStep3(t *testing.T) {
	target := Entry{ID: "foo"}
	calls := 0
	env := Env{Run: func(ctx context.Context, _ string, args []string) ([]byte, error) {
		calls++
		if calls <= 2 {
			return []byte(""), nil // steps 1 and 2: no match, return immediately
		}
		<-ctx.Done() // step 3: hangs until the shared budget expires
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res := identify(ctx, env, "/bin/foo", target)
	if !res.timedOut {
		t.Fatalf("res = %+v, want timedOut", res)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want exactly 3 (stopped after step 3's timeout)", calls)
	}
}

// --- parseVersionText / parseInlineVersionLine / findLabeledLine ----------

func TestParseVersionText_EmptyInput(t *testing.T) {
	if _, _, _, _, ok := parseVersionText(""); ok {
		t.Error("want ok=false for empty input")
	}
	if _, _, _, _, ok := parseVersionText("\n\n"); ok {
		t.Error("want ok=false for blank-only input")
	}
}

func TestParseInlineVersionLine_MissingParens(t *testing.T) {
	if _, _, _, _, ok := parseInlineVersionLine("foo 1.0.0 no parens here"); ok {
		t.Error("want ok=false without matching parens")
	}
	if _, _, _, _, ok := parseInlineVersionLine("foo 1.0.0 )backwards("); ok {
		t.Error("want ok=false when ')' precedes '('")
	}
}

func TestParseInlineVersionLine_WrongHeadFieldCount(t *testing.T) {
	if _, _, _, _, ok := parseInlineVersionLine("foo (abc) 2026-01-01"); ok {
		t.Error("want ok=false when head has only 1 field")
	}
	if _, _, _, _, ok := parseInlineVersionLine("foo bar baz (abc) 2026-01-01"); ok {
		t.Error("want ok=false when head has 3 fields")
	}
}

func TestParseInlineVersionLine_EmptyDate(t *testing.T) {
	if _, _, _, _, ok := parseInlineVersionLine("foo 1.0.0 (abc)"); ok {
		t.Error("want ok=false when nothing follows the closing paren")
	}
}

func TestFindLabeledLine_NotFound(t *testing.T) {
	if _, found := findLabeledLine([]string{"nothing here", "still nothing"}, "revision:"); found {
		t.Error("want found=false")
	}
}

// --- parseSingleVersionToken ------------------------------------------------

func TestParseSingleVersionToken(t *testing.T) {
	cases := []struct {
		in       string
		wantTok  string
		wantOK   bool
		describe string
	}{
		{"1.2.3\n", "1.2.3", true, "single token"},
		{"  1.2.3  \n", "1.2.3", true, "trims whitespace"},
		{"", "", false, "empty"},
		{"   \n  \n", "", false, "blank only"},
		{"1.2.3\nextra\n", "", false, "two non-blank lines"},
		{"1.2.3 extra\n", "", false, "two tokens on one line"},
	}
	for _, c := range cases {
		tok, ok := parseSingleVersionToken(c.in)
		if ok != c.wantOK || tok != c.wantTok {
			t.Errorf("%s: parseSingleVersionToken(%q) = (%q, %v), want (%q, %v)", c.describe, c.in, tok, ok, c.wantTok, c.wantOK)
		}
	}
}

// --- matchesLegacySignature / splitLines / normalize helpers --------------

func TestMatchesLegacySignature(t *testing.T) {
	target := Entry{LegacyVersionSignatures: []string{"1.0.0", "2.0.0"}}
	if !matchesLegacySignature(target, "1.0.0") {
		t.Error("want match for declared signature")
	}
	if matchesLegacySignature(target, "3.0.0") {
		t.Error("want no match for undeclared signature")
	}
	if matchesLegacySignature(Entry{}, "1.0.0") {
		t.Error("want no match with no declared signatures")
	}
}

func TestSplitLines_CRLF(t *testing.T) {
	got := splitLines("a\r\nb\r\n")
	want := []string{"a", "b", ""}
	if len(got) != len(want) {
		t.Fatalf("splitLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitLines[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeNoneUnknown(t *testing.T) {
	cases := map[string]string{
		"none":    "",
		"NONE":    "",
		"Unknown": "",
		"  none ": "",
		"abc123":  "abc123",
		"":        "",
	}
	for in, want := range cases {
		if got := normalizeNoneUnknown(in); got != want {
			t.Errorf("normalizeNoneUnknown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeVersionToken(t *testing.T) {
	cases := map[string]string{
		"v1.2.3": "1.2.3",
		"1.2.3":  "1.2.3",
		" v2.0 ": "2.0",
	}
	for in, want := range cases {
		if got := normalizeVersionToken(in); got != want {
			t.Errorf("normalizeVersionToken(%q) = %q, want %q", in, got, want)
		}
	}
}
