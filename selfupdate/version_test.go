package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newReleasesServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVerdict_String(t *testing.T) {
	cases := []struct {
		v    Verdict
		want string
	}{
		{UpToDate, "up_to_date"},
		{UpdateAvailable, "update_available"},
		{Undetermined, "undetermined"},
		{Verdict(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.v.String(); got != c.want {
			t.Errorf("Verdict(%d).String() = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.5.0", "v0.3.0", 1},
		{"v0.3.0", "v0.5.0", -1},
		{"0.5.0", "v0.5.0", 0},
		{"v1.2.0", "v1.10.0", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.0.0-rc.1", "v1.0.0", -1},
		{"v1.0.0", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.1", "v1.0.0-rc.2", -1},
		// A Go pseudo-version orders below its eventual release
		// (REQ: undetermined-version: "it orders below its release per
		// semver" — it is a known version, not an undetermined one).
		{"v0.6.0-0.20230115120000-abcdef123456", "v0.6.0", -1},
		{"v0.6.0", "v0.6.0-0.20230115120000-abcdef123456", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d; want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSplitVersion_NonNumeric(t *testing.T) {
	core, pre := splitVersion("1.x.3-beta")
	if core != [3]int{1, 0, 3} {
		t.Fatalf("core = %v, want [1 0 3]", core)
	}
	if pre != "beta" {
		t.Fatalf("pre = %q, want beta", pre)
	}
}

// --- Config.Check ---

func TestCheck_UpToDate(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v0.6.0","prerelease":false,"draft":false}]`)
	cfg := Config{Repository: "acme/tool", CurrentVersion: "0.6.0", ReleasesAPIURL: srv.URL, HTTPClient: srv.Client()}

	got, err := cfg.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != UpToDate {
		t.Fatalf("verdict = %v, want UpToDate", got.Verdict)
	}
	if got.Current != "0.6.0" || got.Latest != "0.6.0" {
		t.Fatalf("result = %+v", got)
	}
}

func TestCheck_UpdateAvailable(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v0.6.0","prerelease":false,"draft":false}]`)
	cfg := Config{Repository: "acme/tool", CurrentVersion: "0.5.0", ReleasesAPIURL: srv.URL, HTTPClient: srv.Client()}

	got, err := cfg.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != UpdateAvailable {
		t.Fatalf("verdict = %v, want UpdateAvailable", got.Verdict)
	}
	if got.Current != "0.5.0" || got.Latest != "0.6.0" {
		t.Fatalf("result = %+v", got)
	}
}

// A configured placeholder ("unknown", not just the default "dev") must be
// reported Undetermined, never UpToDate (REQ: undetermined-version).
func TestCheck_ConfiguredUndeterminedPlaceholder(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v0.6.0","prerelease":false,"draft":false}]`)
	cfg := Config{
		Repository: "acme/tool", CurrentVersion: "unknown",
		UndeterminedVersions: []string{"unknown"},
		ReleasesAPIURL:       srv.URL, HTTPClient: srv.Client(),
	}

	got, err := cfg.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != Undetermined {
		t.Fatalf("verdict = %v, want Undetermined", got.Verdict)
	}
	if got.Current != "unknown" {
		t.Fatalf("Current = %q, want the placeholder reported as-is", got.Current)
	}
}

// The default placeholder is "dev" when UndeterminedVersions is unset.
func TestCheck_DefaultUndeterminedPlaceholder(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v0.6.0","prerelease":false,"draft":false}]`)
	cfg := Config{Repository: "acme/tool", CurrentVersion: "dev", ReleasesAPIURL: srv.URL, HTTPClient: srv.Client()}

	got, err := cfg.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != Undetermined {
		t.Fatalf("verdict = %v, want Undetermined", got.Verdict)
	}
}

// The channel binary in a multi-product repository must report ITS OWN
// version as latest, not another product's — a --check that names the wrong
// product while still exiting clean is the exact failure mode TagPrefix
// exists to prevent. Latest is reported bare (no product prefix), matching
// the shape CurrentVersion is already in.
func TestCheck_TagPrefixReportsOwnProductOnly(t *testing.T) {
	body := `[
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": false},
		{"tag_name": "servers-v0.26.1", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := Config{
		Repository: "synchestra-io/synchestra-releases", CurrentVersion: "0.26.0",
		TagPrefix: "servers-", ReleasesAPIURL: srv.URL, HTTPClient: srv.Client(),
	}

	got, err := cfg.Check(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Latest != "0.26.1" {
		t.Fatalf("Latest = %q, want %q (the servers- product's own bare version, not cli-'s)", got.Latest, "0.26.1")
	}
	if got.Verdict != UpdateAvailable {
		t.Fatalf("Verdict = %v, want UpdateAvailable", got.Verdict)
	}
}

func TestCheck_ReleaseLookupFailureIsTypedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{Repository: "acme/tool", CurrentVersion: "1.0.0", ReleasesAPIURL: srv.URL, HTTPClient: srv.Client()}

	_, err := cfg.Check(context.Background())
	if err == nil {
		t.Fatal("expected error on non-200 response, got nil")
	}
	if KindOf(err) != KindReleaseLookup {
		t.Errorf("KindOf(err) = %v, want KindReleaseLookup", KindOf(err))
	}
}
