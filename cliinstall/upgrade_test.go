package cliinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strongo/buildinfo"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- shared upgrade test fixtures ----------------------------------------

// releasesJSON builds a GitHub releases-listing body (newest first) from
// plain tags, matching releaseJSON's shape in selfupdate/release.go.
func releasesJSON(tags ...string) string {
	parts := make([]string, len(tags))
	for i, tag := range tags {
		parts[i] = fmt.Sprintf(`{"tag_name":%q,"prerelease":false,"draft":false}`, tag)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// upgradeReleaseServer serves a distinct /releases/<id> listing per catalog
// id from releasesByID, and download files from files, so several targets'
// Config.LatestRelease/Check/UpdateAt calls in the same test resolve
// entirely offline (cli-install#req:no-network-in-tests).
func upgradeReleaseServer(t *testing.T, releasesByID map[string]string, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/releases/") {
			id := strings.TrimPrefix(r.URL.Path, "/releases/")
			if body, ok := releasesByID[id]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		if body, ok := files[r.URL.Path]; ok {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// configureUpgradeRelease points a non-host target's Config at srv, keyed
// by its own catalog id.
func configureUpgradeRelease(srv *httptest.Server) func(Entry, selfupdate.Config) selfupdate.Config {
	return func(e Entry, cfg selfupdate.Config) selfupdate.Config {
		cfg.ReleasesAPIURL = srv.URL + "/releases/" + e.ID
		cfg.DownloadURL = perTagDownloadURL(srv.URL + "/files/" + e.ID)
		cfg.HTTPClient = srv.Client()
		return cfg
	}
}

// hostReleaseConfig points a host's own selfupdate.Config at srv, keyed by
// id (mirroring configureUpgradeRelease for HostConfig, which has no
// ConfigureRelease hook of its own — the host wires its release endpoints
// directly onto the Config it builds).
func hostReleaseConfig(srv *httptest.Server, id string, cfg selfupdate.Config) selfupdate.Config {
	cfg.ReleasesAPIURL = srv.URL + "/releases/" + id
	cfg.DownloadURL = perTagDownloadURL(srv.URL + "/files/" + id)
	cfg.HTTPClient = srv.Client()
	return cfg
}

// jsonRunFor answers "version --json" for exactly one path with version,
// and fails identification for every other path — enough for a single
// located, Installed target in a test that doesn't need multiJSONRun's
// multi-target dispatch.
func jsonRunFor(path, id, version string) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, p string, args []string) ([]byte, error) {
		if p == path && len(args) == 2 && args[0] == "version" && args[1] == "--json" {
			b, _ := jsonMarshalVersion(id, version)
			return b, nil
		}
		return nil, errors.New("unrecognized")
	}
}

func jsonMarshalVersion(id, version string) ([]byte, error) {
	return json.Marshal(buildinfo.VersionJSON{Name: id, Version: version})
}

// fakeDetectHost is the task-22-review-S1 test seam: UpgradeOptions.
// DetectHost, injected so a test controls the host's own classification
// deterministically instead of depending on the real running test binary's
// own path (which selfupdate.Config.DetectSelf would otherwise resolve).
func fakeDetectHost(method selfupdate.InstallMethod, manager *selfupdate.Manager, path string) func() (selfupdate.Detection, error) {
	return func() (selfupdate.Detection, error) {
		return selfupdate.Detection{Method: method, Manager: manager, Path: path}, nil
	}
}

func failingDetectHost(err error) func() (selfupdate.Detection, error) {
	return func() (selfupdate.Detection, error) { return selfupdate.Detection{}, err }
}

// --- pure helpers ---------------------------------------------------------

func TestUpgradeOutcome_String(t *testing.T) {
	cases := map[UpgradeOutcome]string{
		UpgradeOutcomeUpgraded:          "upgraded",
		UpgradeOutcomeManagerExecuted:   "manager_executed",
		UpgradeOutcomeRedirected:        "redirected",
		UpgradeOutcomeAlreadyCurrent:    "already_current",
		UpgradeOutcomeAhead:             "ahead",
		UpgradeOutcomeSkippedNonRelease: "skipped_non_release_build",
		UpgradeOutcomeNotInstalled:      "not_installed",
		UpgradeOutcomeUnrecognized:      "unrecognized",
		UpgradeOutcomeRefused:           "refused",
		UpgradeOutcomeDryRun:            "dry_run",
		UpgradeOutcomeDeclined:          "declined",
		UpgradeOutcomeFailed:            "failed",
		UpgradeOutcome(999):             "unknown",
	}
	for outcome, want := range cases {
		if got := outcome.String(); got != want {
			t.Errorf("UpgradeOutcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

func TestIsNonReleaseBuild(t *testing.T) {
	cases := []struct {
		version      string
		undetermined []string
		want         bool
	}{
		{"dev", nil, true},
		{"(devel)", nil, true},
		{"unknown", nil, true},
		{"custom", []string{"custom"}, true},
		{"1.2.3", nil, false},
		{"v1.2.3", nil, false},
		{"1.2.3-rc.1", nil, false},
		// task-22 review M1: a hyphenated prerelease is valid semver and
		// must not be misclassified as a non-release build.
		{"1.2.0-rc-1", nil, false},
		{"0.5.0-beta-2", nil, false},
		{"0.20.3+dirty", nil, true},
		{"1.2", nil, true},
		{"1.2.3.4", nil, true},
		{"0.0.0-20230101120000-abcdef123456", nil, true},
		{"", nil, true},
	}
	for _, c := range cases {
		if got := isNonReleaseBuild(c.version, c.undetermined); got != c.want {
			t.Errorf("isNonReleaseBuild(%q, %v) = %v, want %v", c.version, c.undetermined, got, c.want)
		}
	}
}

func TestEffectiveUndetermined(t *testing.T) {
	if got := effectiveUndetermined(nil); len(got) != 1 || got[0] != "dev" {
		t.Errorf("effectiveUndetermined(nil) = %v", got)
	}
	if got := effectiveUndetermined([]string{"x"}); len(got) != 1 || got[0] != "x" {
		t.Errorf("effectiveUndetermined([x]) = %v", got)
	}
}

func TestContainsString(t *testing.T) {
	if !containsString([]string{"a", "b"}, "b") {
		t.Error("containsString = false, want true")
	}
	if containsString([]string{"a", "b"}, "c") {
		t.Error("containsString = true, want false")
	}
}

func TestClassifyForUpgrade(t *testing.T) {
	mgr := selfupdate.HomebrewCask("x")
	managers := []selfupdate.Manager{mgr}

	// Managed via the unresolved PATH-found path itself.
	st := Status{Path: "/opt/homebrew/bin/x", ResolvedPath: "/opt/homebrew/bin/x"}
	if det := classifyForUpgrade(st, managers); det.Method != selfupdate.Managed || det.Manager == nil || det.Manager.Name != "Homebrew" || det.Path != st.Path {
		t.Errorf("managed via unresolved path: got %+v", det)
	}

	// Managed via the RESOLVED path only (a Homebrew shim symlink whose
	// unresolved PATH location carries no manager marker of its own).
	st1b := Status{Path: "/home/alex/.local/bin/x", ResolvedPath: "/opt/homebrew/Cellar/x/1.0.0/bin/x"}
	if det := classifyForUpgrade(st1b, managers); det.Method != selfupdate.Managed || det.Path != st1b.Path {
		t.Errorf("managed via resolved path: got %+v", det)
	}

	// Manual PATH shape whose resolved target is also manual: judged on
	// ResolvedPath, against a target with no managers at all.
	st2 := Status{Path: "/home/alex/.local/bin/x", ResolvedPath: "/home/alex/go/bin/x"}
	if det := classifyForUpgrade(st2, nil); det.Method != selfupdate.Manual || det.Path != "/home/alex/go/bin/x" {
		t.Errorf("manual: got %+v", det)
	}

	// Manual-LOOKING PATH shape whose resolved target is a source checkout:
	// ambiguous, per REQ: upgrade-per-target-policy.
	st3 := Status{Path: "/home/alex/.local/bin/x", ResolvedPath: "/home/alex/src/x/x"}
	if det := classifyForUpgrade(st3, managers); det.Method != selfupdate.Ambiguous || det.Path != st3.ResolvedPath {
		t.Errorf("ambiguous: got %+v", det)
	}

	// Empty ResolvedPath falls back to Path.
	st4 := Status{Path: "/home/alex/bin/x"}
	if det := classifyForUpgrade(st4, nil); det.Method != selfupdate.Manual || det.Path != "/home/alex/bin/x" {
		t.Errorf("fallback: got %+v", det)
	}
}

func TestAmbiguousRefusal(t *testing.T) {
	cfg := selfupdate.Config{BinaryName: "x", CurrentVersion: "1.0.0"}
	det := selfupdate.Detection{Method: selfupdate.Ambiguous, Path: "/src/x/x"}
	outcome, failure := ambiguousRefusal(cfg, det)
	if outcome != UpgradeOutcomeRefused {
		t.Errorf("outcome = %v, want Refused", outcome)
	}
	if failure == nil || failure.Kind != selfupdate.KindAmbiguous {
		t.Fatalf("failure = %+v, want KindAmbiguous", failure)
	}
	if !strings.Contains(failure.Error(), "ambiguous") {
		t.Errorf("failure message %q does not mention ambiguous", failure.Error())
	}
}

func TestMapAction(t *testing.T) {
	cases := []struct {
		name       string
		outcome    selfupdate.Outcome
		err        error
		want       UpgradeOutcome
		wantFailed bool
	}{
		{"lookup failure", selfupdate.Outcome{}, &selfupdate.Failure{Kind: selfupdate.KindReleaseLookup, Err: errors.New("x")}, UpgradeOutcomeFailed, true},
		{"updated", selfupdate.Outcome{Action: selfupdate.ActionUpdated}, nil, UpgradeOutcomeUpgraded, false},
		{"manager executed", selfupdate.Outcome{Action: selfupdate.ActionManagerExecuted}, nil, UpgradeOutcomeManagerExecuted, false},
		{"already current", selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent}, nil, UpgradeOutcomeAlreadyCurrent, false},
		{"ahead", selfupdate.Outcome{Action: selfupdate.ActionAhead}, nil, UpgradeOutcomeAhead, false},
		{"redirected", selfupdate.Outcome{Action: selfupdate.ActionRedirected}, nil, UpgradeOutcomeRedirected, false},
		{"planned", selfupdate.Outcome{Action: selfupdate.ActionPlanned, PlannedURL: "https://x/asset.tar.gz"}, nil, UpgradeOutcomeDryRun, false},
		{"unexpected aborted", selfupdate.Outcome{Action: selfupdate.ActionAborted}, nil, UpgradeOutcomeFailed, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			outcome, failure, assetURL, _ := mapAction(c.outcome, c.err)
			if outcome != c.want {
				t.Errorf("outcome = %v, want %v", outcome, c.want)
			}
			if c.wantFailed && failure == nil {
				t.Error("failure = nil, want non-nil")
			}
			if !c.wantFailed && failure != nil {
				t.Errorf("failure = %+v, want nil", failure)
			}
			if c.name == "planned" && assetURL != "https://x/asset.tar.gz" {
				t.Errorf("assetURL = %q", assetURL)
			}
		})
	}
}

func TestUpgradeBatchResult_FailedAndFailure(t *testing.T) {
	b := UpgradeBatchResult{Results: []UpgradeResult{
		{Target: "a", Outcome: UpgradeOutcomeUpgraded},
		{Target: "b", Outcome: UpgradeOutcomeFailed, Failure: &selfupdate.Failure{Kind: selfupdate.KindDownload, Err: errors.New("x")}},
	}}
	if !b.Failed() {
		t.Error("Failed() = false, want true")
	}
	var bf *BatchFailure
	if err := b.Failure(); !errors.As(err, &bf) || len(bf.Failures) != 1 {
		t.Errorf("Failure() = %v", err)
	}

	ok := UpgradeBatchResult{Results: []UpgradeResult{{Target: "a", Outcome: UpgradeOutcomeUpgraded}}}
	if ok.Failed() {
		t.Error("Failed() = true, want false")
	}
	if ok.Failure() != nil {
		t.Error("Failure() != nil for an all-success batch")
	}

	// task-22 review B1: a Refused row carrying a Failure (the only way
	// Refused is ever produced) counts as a batch failure too, exactly like
	// Failed — an ambiguous host fails `self-update` outright, so it must
	// fail `upgrade` too.
	refused := UpgradeBatchResult{Results: []UpgradeResult{
		{Target: "a", Outcome: UpgradeOutcomeRefused, Failure: &selfupdate.Failure{Kind: selfupdate.KindAmbiguous, Err: errors.New("ambiguous")}},
	}}
	if !refused.Failed() {
		t.Error("Failed() = false, want true for a refused/ambiguous row")
	}
	if err := refused.Failure(); !errors.As(err, &bf) || len(bf.Failures) != 1 || bf.Failures[0].Kind != selfupdate.KindAmbiguous {
		t.Errorf("Failure() = %v", err)
	}

	// A Refused row with no Failure (should never happen in practice) does
	// NOT count, mirroring the same nil-guard BatchResult.Failure applies.
	refusedNoFailure := UpgradeBatchResult{Results: []UpgradeResult{{Target: "a", Outcome: UpgradeOutcomeRefused}}}
	if refusedNoFailure.Failed() {
		t.Error("Failed() = true for a Refused row with no Failure, want false")
	}
}

// --- GitHub bearer-auth transport -----------------------------------------

type recordingTransport struct {
	resp   *http.Response
	err    error
	gotReq *http.Request
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.gotReq = req
	return r.resp, r.err
}

func closingBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

func TestGithubAuthTransport_AddsBearerOnlyForAPIHost(t *testing.T) {
	base := &recordingTransport{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: closingBody("")}}
	tr := &githubAuthTransport{base: base, token: "secret"}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := base.gotReq.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("Authorization = %q", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("original request must not be mutated")
	}
}

func TestGithubAuthTransport_NoTokenForOtherHosts(t *testing.T) {
	base := &recordingTransport{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: closingBody("")}}
	tr := &githubAuthTransport{base: base, token: "secret"}
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/x/y/releases/download/v1/a.tar.gz", nil)

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := base.gotReq.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty for a non-API host", got)
	}
}

// TestGithubAuthTransport_NoTokenOverCleartext is task-22 review M4: the
// bearer token MUST NOT be attached to a plain-http request, even to the
// API host itself — a test double or a misconfigured ConfigureRelease must
// never leak it.
func TestGithubAuthTransport_NoTokenOverCleartext(t *testing.T) {
	base := &recordingTransport{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: closingBody("")}}
	tr := &githubAuthTransport{base: base, token: "secret"}
	req, _ := http.NewRequest(http.MethodGet, "http://api.github.com/repos/x/y/releases", nil)

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := base.gotReq.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty over cleartext http", got)
	}
}

func TestGithubAuthTransport_NoTokenConfigured(t *testing.T) {
	base := &recordingTransport{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: closingBody("")}}
	tr := &githubAuthTransport{base: base}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := base.gotReq.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty with no token configured", got)
	}
}

func TestGithubAuthTransport_RateLimitDetected(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	base := &recordingTransport{resp: &http.Response{StatusCode: http.StatusForbidden, Header: h, Body: closingBody("blocked")}}
	tr := &githubAuthTransport{base: base}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	resp, err := tr.RoundTrip(req)
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
	var rl *githubRateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *githubRateLimitError", err)
	}
	if !strings.Contains(rl.Error(), "GH_TOKEN") {
		t.Errorf("message %q does not name GH_TOKEN", rl.Error())
	}
}

// TestGithubAuthTransport_RateLimitWithTokenWordsMessageDifferently is
// task-22 review M6: when a token WAS already sent, "set GH_TOKEN" is not
// an actionable remedy — the message must say so differently.
func TestGithubAuthTransport_RateLimitWithTokenWordsMessageDifferently(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	base := &recordingTransport{resp: &http.Response{StatusCode: http.StatusForbidden, Header: h, Body: closingBody("blocked")}}
	tr := &githubAuthTransport{base: base, token: "secret"}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	_, err := tr.RoundTrip(req)
	var rl *githubRateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *githubRateLimitError", err)
	}
	if strings.Contains(rl.Error(), "set GH_TOKEN") {
		t.Errorf("message %q still suggests setting GH_TOKEN despite one being sent", rl.Error())
	}
}

func TestGithubAuthTransport_TooManyRequestsRateLimitDetected(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	base := &recordingTransport{resp: &http.Response{StatusCode: http.StatusTooManyRequests, Header: h, Body: closingBody("blocked")}}
	tr := &githubAuthTransport{base: base}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	if _, err := tr.RoundTrip(req); !errors.As(err, new(*githubRateLimitError)) {
		t.Fatalf("err = %v, want *githubRateLimitError", err)
	}
}

func Test403WithoutRateLimitHeaderPassesThrough(t *testing.T) {
	base := &recordingTransport{resp: &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: closingBody("nope")}}
	tr := &githubAuthTransport{base: base}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("resp = %+v", resp)
	}
}

func TestGithubAuthTransport_BaseErrorPassesThrough(t *testing.T) {
	base := &recordingTransport{err: errors.New("dial failed")}
	tr := &githubAuthTransport{base: base}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)

	if _, err := tr.RoundTrip(req); err == nil || err.Error() != "dial failed" {
		t.Errorf("err = %v, want dial failed", err)
	}
}

func TestGithubAuthTransport_NilBaseDefaultsToHTTPDefaultTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(srv.Close)

	tr := &githubAuthTransport{}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestGithubHTTPClient_TokenPriority(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case "GH_TOKEN":
			return "ghtoken"
		case "GITHUB_TOKEN":
			return "githubtoken"
		}
		return ""
	}
	c := githubHTTPClient(getenv, nil)
	tr, ok := c.Transport.(*githubAuthTransport)
	if !ok || tr.token != "ghtoken" {
		t.Errorf("transport = %+v", tr)
	}
}

func TestGithubHTTPClient_FallsBackToGithubToken(t *testing.T) {
	c := githubHTTPClient(func(k string) string {
		if k == "GITHUB_TOKEN" {
			return "githubtoken"
		}
		return ""
	}, nil)
	tr := c.Transport.(*githubAuthTransport)
	if tr.token != "githubtoken" {
		t.Errorf("token = %q", tr.token)
	}
}

func TestGithubHTTPClient_NilGetenv(t *testing.T) {
	c := githubHTTPClient(nil, nil)
	tr := c.Transport.(*githubAuthTransport)
	if tr.token != "" {
		t.Errorf("token = %q, want empty", tr.token)
	}
}

func TestWithGitHubAuth_DefaultsWhenNil(t *testing.T) {
	cfg := withGitHubAuth(selfupdate.Config{}, func(string) string { return "" })
	if cfg.HTTPClient == nil {
		t.Fatal("HTTPClient not defaulted")
	}
}

// TestWithGitHubAuth_WrapsExistingTransport is task-22 review M5: a host or
// ConfigureRelease that already set its own HTTPClient must not silently
// lose the bearer-auth/rate-limit behavior — its Transport is wrapped, not
// bypassed.
func TestWithGitHubAuth_WrapsExistingTransport(t *testing.T) {
	base := &recordingTransport{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: closingBody("")}}
	custom := &http.Client{Transport: base}
	cfg := withGitHubAuth(selfupdate.Config{HTTPClient: custom}, func(k string) string {
		if k == "GH_TOKEN" {
			return "secret"
		}
		return ""
	})
	if cfg.HTTPClient == custom {
		t.Fatal("HTTPClient was not wrapped (same pointer)")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases", nil)
	if _, err := cfg.HTTPClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if got := base.gotReq.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("Authorization = %q, want the wrapped transport to still attach the bearer token", got)
	}
}

func TestUpgradeTargetConfig(t *testing.T) {
	target := Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}
	called := false
	opts := UpgradeOptions{
		Env: batchEnv(nil, "", nil, nil, nil),
		ConfigureRelease: func(e Entry, cfg selfupdate.Config) selfupdate.Config {
			called = true
			if e.ID != "ovdb" {
				t.Errorf("ConfigureRelease target = %q", e.ID)
			}
			return cfg
		},
	}
	cfg := upgradeTargetConfig(target, "1.0.0", opts)
	if !called {
		t.Error("ConfigureRelease was not called")
	}
	if cfg.CurrentVersion != "1.0.0" || cfg.BinaryName != "ovdb" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.HTTPClient == nil {
		t.Error("HTTPClient not defaulted")
	}
}

func TestUpgradeHostConfig(t *testing.T) {
	opts := UpgradeOptions{
		Env:        batchEnv(nil, "", nil, nil, nil),
		HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"},
	}
	cfg := upgradeHostConfig(opts)
	if cfg.BinaryName != "cover100" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.HTTPClient == nil {
		t.Error("HTTPClient not defaulted")
	}
}

func TestDetectHostFunc(t *testing.T) {
	called := false
	fake := func() (selfupdate.Detection, error) { called = true; return selfupdate.Detection{}, nil }
	if _, err := detectHostFunc(UpgradeOptions{DetectHost: fake})(); err != nil || !called {
		t.Error("detectHostFunc did not use the injected DetectHost")
	}

	// Nil DetectHost defaults to opts.HostConfig.DetectSelf — a real
	// selfupdate.Config method reference, just proven callable here (its
	// own behavior is selfupdate's, not this package's, to test).
	fn := detectHostFunc(UpgradeOptions{HostConfig: selfupdate.Config{BinaryName: "x"}})
	if fn == nil {
		t.Error("detectHostFunc returned nil with no DetectHost configured")
	}
}

// --- target selection ------------------------------------------------------

func TestPlanUpgrade_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("PlanUpgrade did not panic for a host id absent from the catalog")
		}
	}()
	_, _ = PlanUpgrade(context.Background(), nil, UpgradeOptions{HostID: "nosuchhost"})
}

func TestCheckUpgrades_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("CheckUpgrades did not panic for a host id absent from the catalog")
		}
	}()
	_, _ = CheckUpgrades(context.Background(), nil, UpgradeOptions{HostID: "nosuchhost"})
}

func TestPlanUpgrade_UnknownNameFailsWholeBatch(t *testing.T) {
	probed := false
	run := func(context.Context, string, []string) ([]byte, error) {
		probed = true
		return nil, errors.New("must not be called")
	}
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, run, noRunManaged)
	opts := UpgradeOptions{HostID: "cover100", Env: env}

	result, err := PlanUpgrade(context.Background(), []string{"nosuchcli", "ovdb"}, opts)
	if err == nil {
		t.Fatal("expected a batch-level unknown-target failure")
	}
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v", selfupdate.KindOf(err))
	}
	if !strings.Contains(err.Error(), "nosuchcli") {
		t.Errorf("error %q does not name the unknown target", err.Error())
	}
	if len(result.Results) != 0 {
		t.Errorf("result.Results = %v, want empty", result.Results)
	}
	if probed {
		t.Error("a valid target's probe ran despite an unknown name in the same batch")
	}
}

func TestPlanUpgrade_NotInstalledAndUnrecognized(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no match") },
		noRunManaged,
	)
	opts := UpgradeOptions{HostID: "cover100", Env: env}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb", "datatug"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("Results = %v", result.Results)
	}
	if r := result.Results[0]; r.Target != "ovdb" || r.Outcome != UpgradeOutcomeUnrecognized {
		t.Errorf("Results[0] = %+v, want ovdb/unrecognized", r)
	}
	if r := result.Results[1]; r.Target != "datatug" || r.Outcome != UpgradeOutcomeNotInstalled || r.InstallHint != "cover100 install datatug" {
		t.Errorf("Results[1] = %+v, want datatug/not_installed with install hint", r)
	}
}

func TestCheckUpgrades_NotInstalledAndUnrecognized(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no match") },
		noRunManaged,
	)
	opts := UpgradeOptions{HostID: "cover100", Env: env}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb", "datatug"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if r := result.Results[0]; r.Target != "ovdb" || r.Outcome != UpgradeOutcomeUnrecognized {
		t.Errorf("Results[0] = %+v, want ovdb/unrecognized", r)
	}
	if r := result.Results[1]; r.Target != "datatug" || r.Outcome != UpgradeOutcomeNotInstalled {
		t.Errorf("Results[1] = %+v, want datatug/not_installed", r)
	}
}

// TestPlanUpgrade_HostDetectFails is task-22 review S1's own failure path:
// DetectHost erroring fails the host row with KindUnexpected, matching
// selfupdate.Config.Update's own "resolve running executable" failure.
func TestPlanUpgrade_HostDetectFails(t *testing.T) {
	env := batchEnv(nil, "", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		DetectHost: failingDetectHost(errors.New("os.Executable failed")),
	}

	result, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindUnexpected {
		t.Errorf("result = %+v, want Failed/KindUnexpected", r)
	}
}

// TestCheckUpgrades_HostDetectFails is task-22 second review D2: a host
// detection failure under --check must NOT fail the row — it falls back to
// Ambiguous (Refused) and the check still reports current/latest/verdict,
// exactly as selfupdate/cobracmd's own runCheck falls back on a detectFunc
// failure (checkFunc's own success is unaffected by detectFunc failing
// afterward).
func TestCheckUpgrades_HostDetectFails(t *testing.T) {
	env := batchEnv(nil, "", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.1.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: failingDetectHost(errors.New("os.Executable failed")),
	}

	result, err := CheckUpgrades(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeRefused || r.InstallMethod != selfupdate.Ambiguous {
		t.Errorf("result = %+v, want Refused/Ambiguous (D2 fallback), not Failed", r)
	}
	if r.Failure == nil || r.Failure.Kind != selfupdate.KindAmbiguous {
		t.Errorf("Failure = %+v, want KindAmbiguous", r.Failure)
	}
	if r.Latest != "1.1.0" {
		t.Errorf("Latest = %q, want 1.1.0 (the lookup still ran and succeeded)", r.Latest)
	}
}

// --- non-release build skipping -------------------------------------------

func TestPlanUpgrade_NonReleaseBuildSkippedUnderAll(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "0.20.3+dirty"),
		noRunManaged,
	)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"},
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}

	result, err := PlanUpgrade(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	var ovdbResult, hostResult *UpgradeResult
	for i := range result.Results {
		switch result.Results[i].Target {
		case "ovdb":
			ovdbResult = &result.Results[i]
		case "cover100":
			hostResult = &result.Results[i]
		}
	}
	if ovdbResult == nil {
		t.Fatalf("ovdb result = nil, want SkippedNonRelease (result.Results = %+v)", result.Results)
	}
	if ovdbResult.Outcome != UpgradeOutcomeSkippedNonRelease {
		t.Errorf("ovdb result = %+v, want SkippedNonRelease", ovdbResult)
	}
	if ovdbResult.Latest != "" || ovdbResult.Tag != "" {
		t.Errorf("ovdb result carries lookup data despite never being looked up: %+v", ovdbResult)
	}
	if hostResult == nil || hostResult.Outcome != UpgradeOutcomeSkippedNonRelease {
		t.Errorf("host result = %+v, want SkippedNonRelease (dev build under --all)", hostResult)
	}
}

func TestCheckUpgrades_NonReleaseBuildSkippedUnderAll(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"},
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := CheckUpgrades(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Outcome != UpgradeOutcomeSkippedNonRelease {
		t.Errorf("Results = %+v, want exactly the skipped host", result.Results)
	}
}

func TestPlanUpgrade_NonReleaseBuildProceedsWhenExplicit(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "dev"),
		noRunManaged,
	)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeDryRun {
		t.Errorf("Outcome = %v, want DryRun (pending) for an explicit non-release build", r.Outcome)
	}
	if !r.NonReleaseBuild {
		t.Error("NonReleaseBuild = false, want true")
	}
	if r.Tag != "v1.0.0" || r.Latest != "1.0.0" {
		t.Errorf("r = %+v, want a real lookup to have run", r)
	}
}

// --- --all / bare-report target selection ---------------------------------

func TestPlanUpgrade_AllSelectsInstalledPlusHost(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "1.0.0"),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v1.0.0"), "cover100": releasesJSON("v1.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		ConfigureRelease: configureUpgradeRelease(srv),
		HostConfig:       hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost:       fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}

	result, err := PlanUpgrade(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	// Only ovdb (installed) and the host itself should be present — every
	// other catalog id is neither located nor the host.
	if len(result.Results) != 2 {
		t.Fatalf("Results = %+v, want exactly [ovdb, cover100]", result.Results)
	}
	if result.Results[0].Target != "ovdb" {
		t.Errorf("Results[0].Target = %q, want ovdb", result.Results[0].Target)
	}
	last := result.Results[len(result.Results)-1]
	if !last.Host || last.Target != "cover100" {
		t.Errorf("last result = %+v, want the host last", last)
	}
}

func TestPlanUpgrade_BareReportBehavesLikeAll(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("none") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env, // All left false, names left nil
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}

	result, err := PlanUpgrade(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if len(result.Results) != 1 || !result.Results[0].Host {
		t.Fatalf("Results = %+v, want exactly the host row", result.Results)
	}
	if result.Results[0].Verdict != selfupdate.UpToDate {
		t.Errorf("Verdict = %v, want UpToDate", result.Results[0].Verdict)
	}
	if result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("Outcome = %v, want AlreadyCurrent", result.Results[0].Outcome)
	}
}

func TestCheckUpgrades_BareReportBehavesLikeAll(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("none") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.1.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := CheckUpgrades(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if len(result.Results) != 1 || !result.Results[0].Host {
		t.Fatalf("Results = %+v, want exactly the host row", result.Results)
	}
	if result.Results[0].Verdict != selfupdate.UpdateAvailable {
		t.Errorf("Verdict = %v, want UpdateAvailable", result.Results[0].Verdict)
	}
}

// --- release lookup outcomes (PlanUpgrade / UpdateAt-delegated) -----------

func TestPlanUpgrade_AlreadyCurrentAndAhead(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true, fakeAbsExe(fakeAbsDir("usr", "bin"), "synchestra"): true},
		multiJSONRun(map[string]string{"ovdb": "1.0.0", "synchestra": "9.9.9"}),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, map[string]string{
		"ovdb":       releasesJSON("v1.0.0"),
		"synchestra": releasesJSON("cli-v1.0.0"),
	}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb", "synchestra"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("ovdb outcome = %v, want AlreadyCurrent", result.Results[0].Outcome)
	}
	if result.Results[1].Outcome != UpgradeOutcomeAhead {
		t.Errorf("synchestra outcome = %v, want Ahead", result.Results[1].Outcome)
	}
	if result.Results[1].Latest != "1.0.0" {
		t.Errorf("synchestra Latest = %q, want tag-prefix-stripped 1.0.0", result.Results[1].Latest)
	}
}

func TestCheckUpgrades_AlreadyCurrentAndAhead(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true, fakeAbsExe(fakeAbsDir("usr", "bin"), "synchestra"): true},
		multiJSONRun(map[string]string{"ovdb": "1.0.0", "synchestra": "9.9.9"}),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, map[string]string{
		"ovdb":       releasesJSON("v1.0.0"),
		"synchestra": releasesJSON("cli-v1.0.0"),
	}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb", "synchestra"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("ovdb outcome = %v, want AlreadyCurrent", result.Results[0].Outcome)
	}
	if result.Results[1].Outcome != UpgradeOutcomeAhead {
		t.Errorf("synchestra outcome = %v, want Ahead", result.Results[1].Outcome)
	}
}

func TestPlanUpgrade_ManagedRedirectAndExecutable(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "ingitdb"): true, fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"): true},
		multiJSONRun(map[string]string{"ingitdb": "1.0.0", "wb": "1.0.0"}),
		noRunManaged,
	)
	env.PathDirs = func() []string { return []string{fakeAbsDir("opt", "homebrew", "bin")} }
	env.EvalSymlinks = func(p string) (string, error) { return p, nil }
	srv := upgradeReleaseServer(t, map[string]string{
		"ingitdb": releasesJSON("v2.0.0"),
		"wb":      releasesJSON("v2.0.0"),
	}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"ingitdb", "wb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if r := result.Results[0]; r.Outcome != UpgradeOutcomeRedirected || r.Command == "" {
		t.Errorf("ingitdb result = %+v, want Redirected with a Command", r)
	}
	if r := result.Results[1]; r.Outcome != UpgradeOutcomeDryRun || r.Command == "" {
		t.Errorf("wb result = %+v, want DryRun (pending) with a Command", r)
	}
}

// TestPlanUpgrade_ManagedUpToDateStillOffered is task-22 review B3: a
// managed, EXECUTABLE target that is already current must still reach
// UpdateAt's own confirm/run path (mapped here to the pending DryRun
// outcome) rather than being short-circuited to AlreadyCurrent — self-
// update's own managed-availability-report REQ says a manager upgrade is
// never skipped merely because the version already matches.
func TestPlanUpgrade_ManagedUpToDateStillOffered(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("opt", "homebrew", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"), "wb", "2.0.0"),
		noRunManaged,
	)
	env.EvalSymlinks = func(p string) (string, error) { return p, nil }
	srv := upgradeReleaseServer(t, map[string]string{"wb": releasesJSON("v2.0.0")}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"wb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeDryRun {
		t.Errorf("Outcome = %v, want DryRun (pending) even though wb is already current", r.Outcome)
	}
	if r.Command == "" {
		t.Error("Command is empty, want wb's upgrade command")
	}
}

func TestPlanUpgrade_AmbiguousRefusedRegardlessOfVerdict(t *testing.T) {
	// A path that classifies neither Manual (no "bin" ancestor, no go/bin)
	// nor any manager: /opt/weird is not "bin"-suffixed, so this forces
	// Ambiguous.
	cases := []struct {
		name    string
		version string
	}{
		{"update available", "1.0.0"},
		{"already current", "2.0.0"},
		{"ahead", "9.9.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := batchEnv(nil, fakeAbsDir("opt", "cover100"), map[string]bool{fakeAbsExe(fakeAbsDir("opt", "weird"), "ovdb"): true}, jsonRunFor(fakeAbsExe(fakeAbsDir("opt", "weird"), "ovdb"), "ovdb", c.version), noRunManaged)
			env.PathDirs = func() []string { return []string{fakeAbsDir("opt", "weird")} }
			srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v2.0.0")}, nil)
			opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

			result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
			if err != nil {
				t.Fatalf("PlanUpgrade error = %v", err)
			}
			r := result.Results[0]
			// task-22 review B1: ambiguous is decided BEFORE any verdict —
			// this MUST be Refused for all three verdicts identically.
			if r.Outcome != UpgradeOutcomeRefused {
				t.Errorf("Outcome = %v, want Refused regardless of verdict", r.Outcome)
			}
			if r.Failure == nil || r.Failure.Kind != selfupdate.KindAmbiguous {
				t.Errorf("Failure = %+v, want KindAmbiguous", r.Failure)
			}
		})
	}
}

func TestCheckUpgrades_AmbiguousRefusedButNeverFailsTheReport(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), map[string]bool{fakeAbsExe(fakeAbsDir("opt", "weird"), "ovdb"): true}, jsonRunFor(fakeAbsExe(fakeAbsDir("opt", "weird"), "ovdb"), "ovdb", "1.0.0"), noRunManaged)
	env.PathDirs = func() []string { return []string{fakeAbsDir("opt", "weird")} }
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v2.0.0")}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeRefused || r.Failure == nil || r.Failure.Kind != selfupdate.KindAmbiguous {
		t.Errorf("result = %+v, want Refused/KindAmbiguous", r)
	}
	// CheckUpgrades still shows latest/verdict for an ambiguous target
	// (task-22 review B1's own "keep the lookup" instruction).
	if r.Latest != "2.0.0" {
		t.Errorf("Latest = %q, want 2.0.0 shown despite the refusal", r.Latest)
	}
	// CheckUpgrades itself never treats this as a batch failure — that is
	// cobracmd's own job (only a true lookup failure fails the report); at
	// this layer, Failed()/Failure() on the *batch* still correctly report
	// it, since the caller (cobracmd) filters differently for --check.
	if !result.Failed() {
		t.Error("UpgradeBatchResult.Failed() = false, want true (still a batch-level failure fact; cobracmd's report path chooses not to act on it for --check)")
	}
}

func TestPlanUpgrade_LookupFailure(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "1.0.0"),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, nil, nil) // no "ovdb" key -> 404 from the handler's own NotFound
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindReleaseLookup {
		t.Errorf("result = %+v, want Failed/KindReleaseLookup", r)
	}
}

// TestPlanUpgrade_LookupRetrySucceedsCarriesTagForward is task-22 third
// review N1: a transient failure on resolveUpgradeRow's own first
// LatestRelease attempt must not leave row.Tag empty when a retry (or,
// previously, only UpdateAt's own internal lookup) would have resolved a
// real one — Execute must reuse EXACTLY the tag PlanUpgrade showed and
// confirmed, per cli-install#req:upgrade-resolves-release-once, not
// independently re-search for "latest" a second time.
func TestPlanUpgrade_LookupRetrySucceedsCarriesTagForward(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasesJSON("v1.1.0")))
	}))
	t.Cleanup(srv.Close)

	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "1.0.0"),
		noRunManaged,
	)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			cfg.ReleasesAPIURL = srv.URL
			cfg.HTTPClient = srv.Client()
			return cfg
		},
	}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeDryRun {
		t.Fatalf("result = %+v, want DryRun (pending) despite the first lookup attempt failing", r)
	}
	if r.Tag != "v1.1.0" || r.Latest != "1.1.0" {
		t.Errorf("Tag/Latest = %q/%q, want v1.1.0/1.1.0 carried forward from the successful retry", r.Tag, r.Latest)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("server received %d calls, want at least 2 (first failed, retry succeeded)", calls)
	}
}

func TestCheckUpgrades_LookupFailure(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "1.0.0"),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, nil, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindReleaseLookup {
		t.Errorf("result = %+v, want Failed/KindReleaseLookup", r)
	}
}

// TestPlanUpgrade_ManagedLookupFailureBecomesWarningNotFailure is task-22
// review B3: a MANAGED target's failed lookup must not fail the row — the
// row still proceeds (redirect, or pending if executable), with the lookup
// failure surfaced only as an advisory ReleaseCheckWarning, exactly as
// self-update's own managed-availability-report REQ requires.
func TestPlanUpgrade_ManagedLookupFailureBecomesWarningNotFailure(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("opt", "homebrew", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"), "wb", "1.0.0"),
		noRunManaged,
	)
	env.EvalSymlinks = func(p string) (string, error) { return p, nil }
	srv := upgradeReleaseServer(t, nil, nil) // wb's own lookup 404s
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"wb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeDryRun {
		t.Fatalf("Outcome = %v, want DryRun (pending) despite the failed lookup: %+v", r.Outcome, r)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "latest release unavailable") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a release-check-unavailable warning", r.Warnings)
	}
}

func TestPlanUpgrade_RateLimitMessage(t *testing.T) {
	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(rateLimited.Close)

	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "1.0.0"),
		noRunManaged,
	)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			cfg.ReleasesAPIURL = rateLimited.URL
			return cfg
		},
	}
	// Force GH_TOKEN so githubHTTPClient wraps the (test-overridden)
	// transport, exercising the same code path production traffic uses,
	// while ReleasesAPIURL still points at the local rate-limited fixture.
	opts.Env.Getenv = func(k string) string {
		if k == "GH_TOKEN" {
			return "secret"
		}
		return ""
	}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil {
		t.Fatalf("result = %+v, want Failed", r)
	}
}

func TestPlanUpgrade_LookupConcurrencyBounded(t *testing.T) {
	var current, max int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&max)
			if n <= old || atomic.CompareAndSwapInt32(&max, old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasesJSON("v1.0.0")))
	}))
	t.Cleanup(srv.Close)

	ids := []string{"ovdb", "synchestra", "specscore", "ingitdb", "datatug", "chatwright"}
	executables := map[string]bool{}
	versions := map[string]string{}
	for _, id := range ids {
		executables[fakeAbsExe(fakeAbsDir("usr", "bin"), id)] = true
		versions[id] = "0.1.0"
	}
	env := batchEnv([]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"), executables, multiJSONRun(versions), noRunManaged)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env, LookupConcurrency: 2,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			cfg.ReleasesAPIURL = srv.URL
			cfg.HTTPClient = srv.Client()
			return cfg
		},
	}

	if _, err := PlanUpgrade(context.Background(), ids, opts); err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if got := atomic.LoadInt32(&max); got > 2 {
		t.Errorf("observed max concurrency = %d, want <= 2", got)
	}
}

func TestPlanUpgrade_LookupTimeoutKillsSlowRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "1.0.0"),
		noRunManaged,
	)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env, LookupTimeout: 30 * time.Millisecond,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			cfg.ReleasesAPIURL = srv.URL
			cfg.HTTPClient = srv.Client()
			return cfg
		},
	}

	start := time.Now()
	result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("PlanUpgrade took %s, want the lookup to be killed near the configured timeout", elapsed)
	}
	if result.Results[0].Outcome != UpgradeOutcomeFailed {
		t.Errorf("Outcome = %v, want Failed from the timed-out lookup", result.Results[0].Outcome)
	}
}

// --- ExecuteUpgrade / Upgrade / CheckUpgrades ------------------------------

func pendingManualResult(target, tag, current string) UpgradeResult {
	return UpgradeResult{
		Target: target, Outcome: UpgradeOutcomeDryRun, InstallMethod: selfupdate.Manual,
		ResolvedPath: fakeAbsExe(fakeAbsDir("usr", "bin"), target), Current: current, Tag: tag,
	}
}

func TestExecuteUpgrade_NoPendingTargetsReturnsUnchanged(t *testing.T) {
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{
		{Target: "ovdb", Outcome: UpgradeOutcomeAlreadyCurrent},
	}}
	result, err := ExecuteUpgrade(context.Background(), plan, UpgradeOptions{HostID: "cover100"})
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("Results = %+v", result.Results)
	}
}

func TestExecuteUpgrade_NoConfirmCallbackFails(t *testing.T) {
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{pendingManualResult("ovdb", "v1.1.0", "1.0.0")}}
	result, err := ExecuteUpgrade(context.Background(), plan, UpgradeOptions{HostID: "cover100"})
	if err == nil {
		t.Fatal("expected a non-interactive refusal")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v", selfupdate.KindOf(err))
	}
	if result.Results[0].Outcome != UpgradeOutcomeFailed {
		t.Errorf("Results[0] = %+v", result.Results[0])
	}
}

func TestExecuteUpgrade_ConfirmDeclines(t *testing.T) {
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{pendingManualResult("ovdb", "v1.1.0", "1.0.0")}}
	opts := UpgradeOptions{HostID: "cover100", Confirm: func([]UpgradeResult) (bool, error) { return false, nil }}
	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeDeclined {
		t.Errorf("Outcome = %v, want Declined", result.Results[0].Outcome)
	}
}

func TestExecuteUpgrade_ConfirmReturnsPlainError(t *testing.T) {
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{pendingManualResult("ovdb", "v1.1.0", "1.0.0")}}
	opts := UpgradeOptions{HostID: "cover100", Confirm: func([]UpgradeResult) (bool, error) { return false, errors.New("boom") }}
	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeFailed || result.Results[0].Failure.Kind != selfupdate.KindNonInteractive {
		t.Errorf("Results[0] = %+v", result.Results[0])
	}
}

func TestExecuteUpgrade_ConfirmReturnsTypedFailure(t *testing.T) {
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{pendingManualResult("ovdb", "v1.1.0", "1.0.0")}}
	typed := &selfupdate.Failure{Kind: selfupdate.KindPermission, Err: errors.New("denied")}
	opts := UpgradeOptions{HostID: "cover100", Confirm: func([]UpgradeResult) (bool, error) { return false, typed }}
	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if !errors.Is(err, typed) && selfupdate.KindOf(err) != selfupdate.KindPermission {
		t.Errorf("err = %v, want the typed failure preserved", err)
	}
	if result.Results[0].Failure != typed {
		t.Errorf("Results[0].Failure = %+v, want the exact typed failure", result.Results[0].Failure)
	}
}

// manualUpgradeFixture builds a release server and Env that let a manual
// "ovdb" target (Current "1.0.0") actually be replaced end to end via
// UpdateAt: a real tar.gz asset, checksums file, and a probe Run that
// reports the NEW version once the file has been replaced.
func manualUpgradeFixture(t *testing.T) (*httptest.Server, InstallEnv, string) {
	t.Helper()
	dir := t.TempDir() + "/bin" // must look manual: looksLikeManualInstall requires a "bin"-suffixed directory
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := dir + "/ovdb"
	if err := writeFile(destPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("new-binary-content")
	archive := makeTarGz(t, "ovdb", content)
	sum := sha256Hex(archive)
	checksums := []byte(sum + "  ovdb_1.1.0_" + goosName + "_" + archName() + ".tar.gz\n")

	files := map[string][]byte{
		"/files/ovdb/v1.1.0/ovdb_1.1.0_" + goosName + "_" + archName() + ".tar.gz": archive,
		// ovdb's own catalog entry overrides ChecksumsName to a flat
		// "checksums.txt" (matching its real .goreleaser.yaml), not the
		// library's per-version default name.
		"/files/ovdb/v1.1.0/checksums.txt": checksums,
	}
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v1.1.0")}, files)

	probed := 0
	env := batchEnv(
		[]string{dir}, fakeAbsDir("opt", "cover100"),
		map[string]bool{destPath: true},
		func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != destPath || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
				return nil, errors.New("unrecognized")
			}
			probed++
			version := "1.0.0"
			if probed > 1 {
				version = "1.1.0"
			}
			b, _ := jsonMarshalVersion("ovdb", version)
			return b, nil
		},
		noRunManaged,
	)
	return srv, env, destPath
}

func TestExecuteUpgrade_ManualReplacementSucceeds(t *testing.T) {
	srv, env, destPath := manualUpgradeFixture(t)
	opts := UpgradeOptions{HostID: "cover100", Yes: true, Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	plan, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if plan.Results[0].Outcome != UpgradeOutcomeDryRun {
		t.Fatalf("plan = %+v, want a pending manual upgrade", plan.Results[0])
	}
	if plan.Results[0].AssetURL == "" {
		t.Error("AssetURL is empty, want the planned manual asset URL (task-22 review S3)")
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeUpgraded {
		t.Fatalf("Outcome = %v, want Upgraded: %+v", r.Outcome, r)
	}
	if r.AssetURL != "" {
		t.Errorf("AssetURL = %q, want empty for a terminal executed result", r.AssetURL)
	}
	got, err := readFile(destPath)
	if err != nil || string(got) != "new-binary-content" {
		t.Errorf("destPath content = %q, err = %v", got, err)
	}
}

func TestUpgrade_ManualReplacementSucceedsEndToEnd(t *testing.T) {
	srv, env, _ := manualUpgradeFixture(t)
	opts := UpgradeOptions{HostID: "cover100", Yes: true, Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := Upgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Upgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeUpgraded {
		t.Errorf("Outcome = %v, want Upgraded", result.Results[0].Outcome)
	}
}

func TestUpgrade_DryRunNeverExecutes(t *testing.T) {
	srv, env, destPath := manualUpgradeFixture(t)
	opts := UpgradeOptions{HostID: "cover100", DryRun: true, Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := Upgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Upgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeDryRun {
		t.Errorf("Outcome = %v, want DryRun", result.Results[0].Outcome)
	}
	got, err := readFile(destPath)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("dry run modified the destination: %q, err = %v", got, err)
	}
}

func TestCheckUpgrades_NeverExecutes(t *testing.T) {
	srv, env, destPath := manualUpgradeFixture(t)
	opts := UpgradeOptions{HostID: "cover100", Yes: true, Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeDryRun {
		t.Errorf("Outcome = %v, want DryRun", result.Results[0].Outcome)
	}
	got, err := readFile(destPath)
	if err != nil || string(got) != "old-binary" {
		t.Errorf("--check modified the destination: %q, err = %v", got, err)
	}
}

func TestUpgrade_PropagatesPlanError(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{HostID: "cover100", Env: env}
	_, err := Upgrade(context.Background(), []string{"nosuchcli"}, opts)
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v", selfupdate.KindOf(err))
	}
}

// --- managed executable execution + finish hint ---------------------------

func TestExecuteUpgrade_ManagerExecutedWithFinishHint(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"wb": releasesJSON("v2.0.0")}, nil)

	var ranSteps [][]string
	runManaged := func(_ context.Context, executable string, args []string) error {
		ranSteps = append(ranSteps, append([]string{executable}, args...))
		return nil
	}

	probed := 0
	env := batchEnv(
		[]string{fakeAbsDir("opt", "homebrew", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"): true},
		func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb") || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
				return nil, errors.New("unrecognized")
			}
			probed++
			version := "1.0.0"
			if probed > 1 {
				version = "2.0.0"
			}
			b, _ := jsonMarshalVersion("wb", version)
			return b, nil
		},
		runManaged,
	)
	env.EvalSymlinks = func(p string) (string, error) { return p, nil }

	opts := UpgradeOptions{
		HostID: "cover100", Yes: true, Env: env, ConfigureRelease: configureUpgradeRelease(srv),
		VerifyManaged: testVerifyManaged(env),
	}
	plan, err := PlanUpgrade(context.Background(), []string{"wb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if plan.Results[0].Outcome != UpgradeOutcomeDryRun {
		t.Fatalf("plan = %+v, want a pending managed upgrade", plan.Results[0])
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeManagerExecuted {
		t.Fatalf("Outcome = %v, want ManagerExecuted: %+v", r.Outcome, r)
	}
	if r.FinishHint != "wb self-update" {
		t.Errorf("FinishHint = %q, want %q", r.FinishHint, "wb self-update")
	}
	if len(ranSteps) == 0 {
		t.Error("RunManaged was never called")
	}
}

// TestExecuteUpgrade_HostManagedExecutedRunsHostAfterUpdate exercises
// executeHostUpgrade directly (as opposed to executeTargetUpgrade): the
// HOST itself is Homebrew-managed and executable, so ExecuteUpgrade must
// call UpdateAt on opts.HostConfig with opts.HostAfterUpdate — the same
// wiring `self-update` uses — never the FinishHint path
// (cli-install#req:host-target-is-running-binary,
// cli-install#req:self-update-equals-upgrade-self).
func TestExecuteUpgrade_HostManagedExecutedRunsHostAfterUpdate(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"wb": releasesJSON("v2.0.0")}, nil)

	var ranSteps [][]string
	runManaged := func(_ context.Context, executable string, args []string) error {
		ranSteps = append(ranSteps, append([]string{executable}, args...))
		return nil
	}

	probed := 0
	env := batchEnv(
		[]string{fakeAbsDir("opt", "homebrew", "bin")}, fakeAbsDir("opt", "homebrew", "bin"),
		map[string]bool{fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"): true},
		func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb") || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
				return nil, errors.New("unrecognized")
			}
			probed++
			version := "1.0.0"
			if probed > 1 {
				version = "2.0.0"
			}
			b, _ := jsonMarshalVersion("wb", version)
			return b, nil
		},
		runManaged,
	)
	env.EvalSymlinks = func(p string) (string, error) { return p, nil }

	homebrew := selfupdate.HomebrewCask("wb")
	hookCalled := false
	opts := UpgradeOptions{
		HostID: "wb", Yes: true, Env: env,
		HostConfig: hostReleaseConfig(srv, "wb", selfupdate.Config{
			BinaryName: "wb", CurrentVersion: "1.0.0",
			Managers: []selfupdate.Manager{homebrew},
		}),
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error {
			hookCalled = true
			return nil
		},
		DetectHost:    fakeDetectHost(selfupdate.Managed, &homebrew, fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb")),
		VerifyManaged: testVerifyManaged(env),
	}

	plan, err := PlanUpgrade(context.Background(), []string{"wb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if plan.Results[0].Outcome != UpgradeOutcomeDryRun || plan.Results[0].Command == "" {
		t.Fatalf("plan = %+v, want a pending managed host upgrade with a Command", plan.Results[0])
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeManagerExecuted {
		t.Fatalf("Outcome = %v, want ManagerExecuted: %+v", r.Outcome, r)
	}
	if r.FinishHint != "" {
		t.Errorf("FinishHint = %q, want empty for the host itself", r.FinishHint)
	}
	if !hookCalled {
		t.Error("HostAfterUpdate was never called")
	}
	if len(ranSteps) == 0 {
		t.Error("RunManaged was never called")
	}
}

// TestPlanUpgrade_HostAlreadyCurrentNeverRunsAfterUpdate is task-22 review
// B2's own root cause, verified directly: selfupdate.Config.UpdateAt's own
// runAfterUpdate skips the hook whenever Options.DryRun is set, and
// PlanUpgrade's UpdateAt call always sets DryRun — so PlanUpgrade alone
// (and therefore `--dry-run`, and therefore `--check`, which does not even
// reach UpdateAt) must NEVER fire the host's after-update hook.
// ExecuteUpgrade is what fires it for a real run — see the next test.
func TestPlanUpgrade_HostAlreadyCurrentNeverRunsAfterUpdate(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)

	calls := 0
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig:      hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost:      fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error { calls++; return nil },
	}

	plan, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if plan.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Fatalf("plan = %+v, want AlreadyCurrent", plan.Results[0])
	}
	if calls != 0 {
		t.Fatalf("HostAfterUpdate called %d times by PlanUpgrade alone, want 0 (DryRun always skips it)", calls)
	}
}

// realHostPath creates a REAL file on disk and returns its path: after-
// update's own installedExecutable calls the real filepath.EvalSymlinks
// (selfupdate's own internal, non-injectable var) on the detected path, so
// any test that lets a real AfterUpdate hook actually fire needs a path
// that genuinely resolves, exactly as manualUpgradeFixture already does
// for a real swap.
func realHostPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir() + "/bin"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := dir + "/cover100"
	if err := writeFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExecuteUpgrade_HostAlreadyCurrentRunsAfterUpdateExactlyOnce is
// task-22 review B2's own fix: ExecuteUpgrade makes one further, real
// (non-dry-run) UpdateAt call for an already-current host specifically, so
// its after-update hook still fires — exactly once, and regardless of
// whether any OTHER pending target in the same batch was declined.
func TestExecuteUpgrade_HostAlreadyCurrentRunsAfterUpdateExactlyOnce(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	hostPath := realHostPath(t)

	calls := 0
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig:      hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost:      fakeDetectHost(selfupdate.Manual, nil, hostPath),
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error { calls++; return nil },
	}

	plan, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if plan.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Fatalf("plan = %+v, want AlreadyCurrent", plan.Results[0])
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("Outcome = %v, want AlreadyCurrent unchanged", result.Results[0].Outcome)
	}
	if calls != 1 {
		t.Errorf("HostAfterUpdate called %d times by ExecuteUpgrade, want exactly 1", calls)
	}
}

// TestExecuteUpgrade_HostAlreadyCurrentHookRunsEvenWhenOtherTargetDeclined
// proves the batch-decline path does not suppress the host's own no-op
// hook: self-update itself never confirms a no-op, so a decline elsewhere
// in the same batch must not change that.
func TestExecuteUpgrade_HostAlreadyCurrentHookRunsEvenWhenOtherTargetDeclined(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0"), "ovdb": releasesJSON("v2.0.0")}, nil)
	calls := 0
	hostPath := realHostPath(t)
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{
		pendingManualResult("ovdb", "v2.0.0", "1.0.0"),
		{Target: "cover100", Host: true, Outcome: UpgradeOutcomeAlreadyCurrent, InstallMethod: selfupdate.Manual, ResolvedPath: hostPath, Current: "1.0.0", Tag: "v1.0.0"},
	}}
	opts := UpgradeOptions{
		HostID: "cover100", Env: batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged),
		HostConfig:      hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		Confirm:         func([]UpgradeResult) (bool, error) { return false, nil }, // decline ovdb's pending upgrade
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error { calls++; return nil },
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeDeclined {
		t.Errorf("ovdb outcome = %v, want Declined", result.Results[0].Outcome)
	}
	if result.Results[1].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("host outcome = %v, want AlreadyCurrent", result.Results[1].Outcome)
	}
	if calls != 1 {
		t.Errorf("HostAfterUpdate called %d times, want exactly 1 despite ovdb's decline", calls)
	}
}

// TestExecuteUpgrade_HostAlreadyCurrentHookRunsOnNonInteractiveRefusal is
// task-22 third review N2: a non-interactive refusal of the OTHER pending
// target's confirmation (no --yes, no Confirm callback) must not suppress
// the already-current host's own after-update hook — that host row was
// never part of the confirmation set at all (nothing to download or
// write), so REQ: confirmation-gate's own "before any download or write"
// scope never applies to it, exactly as self-update itself never gates
// this hook behind any prompt.
func TestExecuteUpgrade_HostAlreadyCurrentHookRunsOnNonInteractiveRefusal(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0"), "ovdb": releasesJSON("v2.0.0")}, nil)
	calls := 0
	hostPath := realHostPath(t)
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{
		pendingManualResult("ovdb", "v2.0.0", "1.0.0"),
		{Target: "cover100", Host: true, Outcome: UpgradeOutcomeAlreadyCurrent, InstallMethod: selfupdate.Manual, ResolvedPath: hostPath, Current: "1.0.0", Tag: "v1.0.0"},
	}}
	opts := UpgradeOptions{
		HostID: "cover100", Env: batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged),
		HostConfig:      hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error { calls++; return nil },
		// Yes is false and Confirm is nil: ovdb's own pending upgrade hits
		// the non-interactive refusal.
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Fatalf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
	if result.Results[0].Outcome != UpgradeOutcomeFailed {
		t.Errorf("ovdb outcome = %v, want Failed (the non-interactive refusal)", result.Results[0].Outcome)
	}
	if result.Results[1].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("host outcome = %v, want AlreadyCurrent unchanged", result.Results[1].Outcome)
	}
	if calls != 1 {
		t.Errorf("HostAfterUpdate called %d times, want exactly 1 despite ovdb's non-interactive refusal", calls)
	}
}

// TestExecuteUpgrade_HostAlreadyCurrentHookRunsOnConfirmError is the same
// N2 invariant for the OTHER early-return path: Confirm itself returning an
// error for the pending (non-host) target.
func TestExecuteUpgrade_HostAlreadyCurrentHookRunsOnConfirmError(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0"), "ovdb": releasesJSON("v2.0.0")}, nil)
	calls := 0
	hostPath := realHostPath(t)
	plan := UpgradeBatchResult{Host: "cover100", Results: []UpgradeResult{
		pendingManualResult("ovdb", "v2.0.0", "1.0.0"),
		{Target: "cover100", Host: true, Outcome: UpgradeOutcomeAlreadyCurrent, InstallMethod: selfupdate.Manual, ResolvedPath: hostPath, Current: "1.0.0", Tag: "v1.0.0"},
	}}
	opts := UpgradeOptions{
		HostID: "cover100", Env: batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged),
		HostConfig:      hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		Confirm:         func([]UpgradeResult) (bool, error) { return false, errors.New("boom") },
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error { calls++; return nil },
	}

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
	if result.Results[1].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Errorf("host outcome = %v, want AlreadyCurrent unchanged", result.Results[1].Outcome)
	}
	if calls != 1 {
		t.Errorf("HostAfterUpdate called %d times, want exactly 1 despite ovdb's Confirm error", calls)
	}
}

// TestUpgrade_HostAlreadyCurrentRunsAfterUpdateExactlyOnceEndToEnd proves
// the same invariant through the full Upgrade (Plan+confirm+Execute)
// convenience call, with --yes set — the case task-22's own coordinator
// ruling names directly ("wb self-update --yes on a current wb restarts
// the daemon... upgrade wb --yes does nothing" was the bug).
func TestUpgrade_HostAlreadyCurrentRunsAfterUpdateExactlyOnceEndToEnd(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)

	calls := 0
	opts := UpgradeOptions{
		HostID: "cover100", Yes: true, Env: env,
		HostConfig:      hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost:      fakeDetectHost(selfupdate.Manual, nil, realHostPath(t)),
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error { calls++; return nil },
	}

	result, err := Upgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("Upgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Fatalf("Outcome = %v, want AlreadyCurrent", result.Results[0].Outcome)
	}
	if calls != 1 {
		t.Errorf("HostAfterUpdate called %d times, want exactly 1", calls)
	}
}

// testVerifyManaged is a small test-only selfupdate.ManagedBinaryVerifier
// mirroring what a cobracmd host wires in production (probe via Probe,
// confirm the version) — cliinstall core no longer builds this itself
// (task-22 review S2: verification is injected, defaulting at the Cobra
// layer to selfcliui.VerifyManagedBinary).
func testVerifyManaged(env InstallEnv) selfupdate.ManagedBinaryVerifier {
	return func(ctx context.Context, detection selfupdate.Detection, binary string, _ []string, expectedVersion string) (selfupdate.ExecutableIdentity, error) {
		target := Entry{ID: filepathBase(binary)}
		if binary == "" && detection.Path != "" {
			target = Entry{ID: filepathBase(detection.Path)}
		}
		statuses := Probe(ctx, []Entry{target}, "", env.Env, ProbeOptions{})
		status := statuses[0]
		if status.State != Installed {
			return selfupdate.ExecutableIdentity{}, fmt.Errorf("could not confirm %s", target.ID)
		}
		if expectedVersion != "" && status.Version != expectedVersion {
			return selfupdate.ExecutableIdentity{}, fmt.Errorf("%s reports %s, expected %s", target.ID, status.Version, expectedVersion)
		}
		return selfupdate.ExecutableIdentity{Path: status.Path, ResolvedPath: status.ResolvedPath}, nil
	}
}

func filepathBase(p string) string {
	if p == "" {
		return ""
	}
	i := strings.LastIndexByte(p, '/')
	return p[i+1:]
}

// --- finalizeUpgradeResult / execute* error and action mapping ------------

func TestFinalizeUpgradeResult(t *testing.T) {
	base := UpgradeResult{Target: "ovdb"}

	t.Run("error maps to Failed", func(t *testing.T) {
		f := &selfupdate.Failure{Kind: selfupdate.KindDownload, Err: errors.New("x")}
		r := finalizeUpgradeResult(base, false, selfupdate.Outcome{}, f)
		if r.Outcome != UpgradeOutcomeFailed || r.Failure != f {
			t.Errorf("r = %+v", r)
		}
	})

	t.Run("already current", func(t *testing.T) {
		r := finalizeUpgradeResult(base, false, selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent}, nil)
		if r.Outcome != UpgradeOutcomeAlreadyCurrent {
			t.Errorf("Outcome = %v", r.Outcome)
		}
	})

	t.Run("ahead", func(t *testing.T) {
		r := finalizeUpgradeResult(base, false, selfupdate.Outcome{Action: selfupdate.ActionAhead}, nil)
		if r.Outcome != UpgradeOutcomeAhead {
			t.Errorf("Outcome = %v", r.Outcome)
		}
	})

	t.Run("redirected", func(t *testing.T) {
		r := finalizeUpgradeResult(base, false, selfupdate.Outcome{Action: selfupdate.ActionRedirected}, nil)
		if r.Outcome != UpgradeOutcomeRedirected {
			t.Errorf("Outcome = %v", r.Outcome)
		}
	})

	t.Run("unexpected action fails", func(t *testing.T) {
		r := finalizeUpgradeResult(base, false, selfupdate.Outcome{Action: selfupdate.ActionAborted}, nil)
		if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindUnexpected {
			t.Errorf("r = %+v", r)
		}
	})

	t.Run("post-swap and after-update warnings carried", func(t *testing.T) {
		r := finalizeUpgradeResult(base, false, selfupdate.Outcome{
			Action:             selfupdate.ActionUpdated,
			PostSwapWarning:    errors.New("post swap"),
			AfterUpdateWarning: errors.New("after update"),
		}, nil)
		if r.Outcome != UpgradeOutcomeUpgraded {
			t.Fatalf("Outcome = %v", r.Outcome)
		}
		joined := strings.Join(r.Warnings, "|")
		if !strings.Contains(joined, "post swap") || !strings.Contains(joined, "after update") {
			t.Errorf("Warnings = %v", r.Warnings)
		}
	})

	t.Run("finish hint only for upgraded or manager-executed non-host targets", func(t *testing.T) {
		r := finalizeUpgradeResult(base, true, selfupdate.Outcome{Action: selfupdate.ActionUpdated}, nil)
		if r.FinishHint != "ovdb self-update" {
			t.Errorf("FinishHint = %q", r.FinishHint)
		}
		r2 := finalizeUpgradeResult(base, true, selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent}, nil)
		if r2.FinishHint != "" {
			t.Errorf("FinishHint = %q, want empty for AlreadyCurrent", r2.FinishHint)
		}
	})
}

func TestExecuteHostUpgrade_NeverGetsFinishHint(t *testing.T) {
	// A direct check that executeHostUpgrade always passes hooks=false to
	// finalizeUpgradeResult, regardless of the host's own catalog entry —
	// verified end to end via wb-as-host below, since wb.SelfUpdateHooks is
	// true and would otherwise be the one case that could leak a hint.
	srv := upgradeReleaseServer(t, map[string]string{"wb": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, fakeAbsDir("opt", "wb"), map[string]bool{fakeAbsExe(fakeAbsDir("opt", "wb"), "wb"): true}, jsonRunFor(fakeAbsExe(fakeAbsDir("opt", "wb"), "wb"), "wb", "1.0.0"), noRunManaged)
	opts := UpgradeOptions{
		HostID: "wb", Yes: true, Env: env,
		HostConfig: hostReleaseConfig(srv, "wb", selfupdate.Config{BinaryName: "wb", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "wb"), "wb")),
	}
	result, err := Upgrade(context.Background(), []string{"wb"}, opts)
	if err != nil {
		t.Fatalf("Upgrade error = %v", err)
	}
	if result.Results[0].Outcome != UpgradeOutcomeAlreadyCurrent {
		t.Fatalf("Outcome = %v, want AlreadyCurrent", result.Results[0].Outcome)
	}
	if result.Results[0].FinishHint != "" {
		t.Errorf("FinishHint = %q, want empty for the host itself", result.Results[0].FinishHint)
	}
}

// --- host targeting ---------------------------------------------------------

func TestPlanUpgrade_HostOtherCopyWarning(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "cover100"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "cover100"), "cover100", "1.0.0"),
		noRunManaged,
	)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"},
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}

	// cover100 named explicitly with no lookup server configured: the
	// lookup itself will fail (no ConfigureRelease/HostConfig endpoint),
	// but the other-copy warning is computed before any lookup runs, so it
	// is still present on the failed result.
	result, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "another copy") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want another-copy warning", r.Warnings)
	}
	if len(r.OtherPaths) != 1 || r.OtherPaths[0] != fakeAbsExe(fakeAbsDir("usr", "bin"), "cover100") {
		t.Errorf("OtherPaths = %v, want the PATH copy", r.OtherPaths)
	}
	// task-22 review M2: Status is always zero for the host row — the
	// other PATH copy is named in Warnings/OtherPaths only.
	if r.Status.ID != "" || r.Status.State != NotInstalled || r.Status.Path != "" {
		t.Errorf("Status = %+v, want zero for the host row", r.Status)
	}
}

func TestPlanUpgrade_HostAllIncludedEvenWhenNotOnPath(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := PlanUpgrade(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if len(result.Results) != 1 || !result.Results[0].Host {
		t.Fatalf("Results = %+v, want exactly the host row", result.Results)
	}
}

// --- additional CheckUpgrades / PlanUpgrade coverage ------------------------

func TestCheckUpgrades_UnknownNameFailsWholeBatch(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{HostID: "cover100", Env: env}

	result, err := CheckUpgrades(context.Background(), []string{"nosuchcli"}, opts)
	if err == nil {
		t.Fatal("expected a batch-level unknown-target failure")
	}
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v", selfupdate.KindOf(err))
	}
	if len(result.Results) != 0 {
		t.Errorf("result.Results = %v, want empty", result.Results)
	}
}

func TestCheckUpgrades_NonHostNonReleaseBuildSkippedUnderAll(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "0.20.3+dirty"),
		noRunManaged,
	)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"},
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := CheckUpgrades(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	var ovdbResult *UpgradeResult
	for i := range result.Results {
		if result.Results[i].Target == "ovdb" {
			ovdbResult = &result.Results[i]
		}
	}
	if ovdbResult == nil || ovdbResult.Outcome != UpgradeOutcomeSkippedNonRelease {
		t.Errorf("ovdb result = %+v, want SkippedNonRelease", ovdbResult)
	}
}

func TestCheckUpgrades_ExplicitNonReleaseBuildProceeds(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "ovdb"), "ovdb", "dev"),
		noRunManaged,
	)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if !r.NonReleaseBuild {
		t.Error("NonReleaseBuild = false, want true")
	}
	if r.Latest != "1.0.0" {
		t.Errorf("Latest = %q, want a real lookup to have run", r.Latest)
	}
}

func TestCheckUpgrades_HostOtherCopyWarning(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("usr", "bin"), "cover100"): true},
		jsonRunFor(fakeAbsExe(fakeAbsDir("usr", "bin"), "cover100"), "cover100", "1.0.0"),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := CheckUpgrades(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if len(r.OtherPaths) != 1 || r.OtherPaths[0] != fakeAbsExe(fakeAbsDir("usr", "bin"), "cover100") {
		t.Errorf("OtherPaths = %v, want the PATH copy", r.OtherPaths)
	}
}

func TestCheckUpgrades_HostExplicitNonReleaseBuildProceeds(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := CheckUpgrades(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if !result.Results[0].NonReleaseBuild {
		t.Error("NonReleaseBuild = false, want true")
	}
}

func TestCheckUpgrades_HostManagedCommandShown(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"wb": releasesJSON("v2.0.0")}, nil)
	homebrew := selfupdate.HomebrewCask("wb")
	opts := UpgradeOptions{
		HostID: "wb", Env: env,
		HostConfig: hostReleaseConfig(srv, "wb", selfupdate.Config{BinaryName: "wb", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Managed, &homebrew, fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb")),
	}
	result, err := CheckUpgrades(context.Background(), []string{"wb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if result.Results[0].Command == "" {
		t.Error("Command is empty, want the manager's upgrade command")
	}
}

func TestCheckUpgrades_HostAmbiguous(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v2.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Ambiguous, nil, fakeAbsExe(fakeAbsDir("src", "cover100"), "cover100")),
	}
	result, err := CheckUpgrades(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeRefused || r.Failure == nil || r.Failure.Kind != selfupdate.KindAmbiguous {
		t.Errorf("result = %+v, want Refused/KindAmbiguous", r)
	}
}

// TestCheckUpgrades_AmbiguousLookupFailureBecomesWarning: an ambiguous
// row's own Check() lookup failing must not produce a SECOND, different
// failure — the row is already terminal (Refused/KindAmbiguous); the
// lookup failure is folded into a warning instead.
// TestCheckUpgrades_AmbiguousLookupFailureFailsExactlyLikeSelfUpdate is
// task-22 second review D1's own regression test: self-update --check
// fails on the Check() error BEFORE it ever consults classification
// (selfupdate/cobracmd's own runCheck calls checkFunc, then returns
// immediately on its error, never reaching detectFunc) — so an ambiguous
// host whose lookup fails MUST fail the row with the release-lookup kind,
// exactly like any other target, not be folded into a mere warning on an
// already-"refused" row. This was the exact gap an earlier revision left:
// classifying ambiguous BEFORE the lookup let the row's own Refused
// classification swallow the lookup failure as a warning.
func TestCheckUpgrades_AmbiguousLookupFailureFailsExactlyLikeSelfUpdate(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), map[string]bool{fakeAbsExe(fakeAbsDir("opt", "weird"), "ovdb"): true}, jsonRunFor(fakeAbsExe(fakeAbsDir("opt", "weird"), "ovdb"), "ovdb", "1.0.0"), noRunManaged)
	env.PathDirs = func() []string { return []string{fakeAbsDir("opt", "weird")} }
	srv := upgradeReleaseServer(t, nil, nil) // no "ovdb" key -> lookup fails
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindReleaseLookup {
		t.Errorf("result = %+v, want Failed/KindReleaseLookup, exactly like self-update --check on the same lookup failure", r)
	}
	// Classification was never even attempted: InstallMethod stays at its
	// zero value (Managed, per selfupdate.InstallMethod's own doc comment
	// on why Managed is deliberately the zero value) since resolveCheckRow
	// returns before ever calling row.classification() here.
	if r.Command != "" {
		t.Errorf("Command = %q, want empty (classification never ran)", r.Command)
	}
}

// TestCheckUpgrades_AmbiguousHostLookupFailureFailsExactlyLikeSelfUpdate is
// the same D1 regression, exercised through the HOST row specifically
// (buildCheckHostRow/resolveCheckRow's own deferred-classification path,
// separate code from the non-host one above).
func TestCheckUpgrades_AmbiguousHostLookupFailureFailsExactlyLikeSelfUpdate(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, nil, nil) // no "cover100" key -> lookup fails
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Ambiguous, nil, fakeAbsExe(fakeAbsDir("src", "cover100"), "cover100")),
	}

	result, err := CheckUpgrades(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindReleaseLookup {
		t.Errorf("result = %+v, want Failed/KindReleaseLookup", r)
	}
}

func TestCheckUpgrades_ManagedRedirectAndExecutable(t *testing.T) {
	env := batchEnv(
		[]string{fakeAbsDir("usr", "bin")}, fakeAbsDir("opt", "cover100"),
		map[string]bool{fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "ingitdb"): true, fakeAbsExe(fakeAbsDir("opt", "homebrew", "bin"), "wb"): true},
		multiJSONRun(map[string]string{"ingitdb": "1.0.0", "wb": "1.0.0"}),
		noRunManaged,
	)
	env.PathDirs = func() []string { return []string{fakeAbsDir("opt", "homebrew", "bin")} }
	env.EvalSymlinks = func(p string) (string, error) { return p, nil }
	srv := upgradeReleaseServer(t, map[string]string{
		"ingitdb": releasesJSON("v2.0.0"),
		"wb":      releasesJSON("v2.0.0"),
	}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := CheckUpgrades(context.Background(), []string{"ingitdb", "wb"}, opts)
	if err != nil {
		t.Fatalf("CheckUpgrades error = %v", err)
	}
	if r := result.Results[0]; r.Outcome != UpgradeOutcomeRedirected {
		t.Errorf("ingitdb result = %+v, want Redirected", r)
	}
	if r := result.Results[1]; r.Outcome != UpgradeOutcomeDryRun {
		t.Errorf("wb result = %+v, want DryRun (offered)", r)
	}
}

func TestPlanUpgrade_HostAmbiguous(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v2.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
		DetectHost: fakeDetectHost(selfupdate.Ambiguous, nil, fakeAbsExe(fakeAbsDir("src", "cover100"), "cover100")),
	}
	result, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeRefused || r.Failure == nil || r.Failure.Kind != selfupdate.KindAmbiguous {
		t.Errorf("result = %+v, want Refused/KindAmbiguous", r)
	}
}

func TestPlanUpgrade_HostExplicitNonReleaseBuildProceeds(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"}),
		DetectHost: fakeDetectHost(selfupdate.Manual, nil, fakeAbsExe(fakeAbsDir("opt", "cover100"), "cover100")),
	}
	result, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if !result.Results[0].NonReleaseBuild {
		t.Error("NonReleaseBuild = false, want true")
	}
}

// --- candidate-building direct unit tests ---------------------------------

func TestNamedUpgradeCandidates_Dedupe(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	opts := UpgradeOptions{HostID: "cover100", Env: env}
	candidates, _, _, err := namedUpgradeCandidates(context.Background(), []string{"ovdb", "ovdb", "cover100"}, opts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2 (deduped ovdb + host)", candidates)
	}
	if candidates[0].id != "ovdb" || candidates[0].explicit != true {
		t.Errorf("candidates[0] = %+v", candidates[0])
	}
	if !candidates[1].isHost || candidates[1].id != "cover100" {
		t.Errorf("candidates[1] = %+v, want the host last", candidates[1])
	}
}

func TestAllUpgradeCandidates_HostLastRegardlessOfCatalogPosition(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "wb"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	candidates, _, _ := allUpgradeCandidates(context.Background(), UpgradeOptions{HostID: "wb", Env: env})
	last := candidates[len(candidates)-1]
	if !last.isHost || last.id != "wb" {
		t.Errorf("last candidate = %+v, want wb (the host)", last)
	}
	for _, c := range candidates[:len(candidates)-1] {
		if c.id == "wb" {
			t.Errorf("host id %q appeared twice in candidates", c.id)
		}
	}
}

func TestResolveUpgradeCandidates_UsesAllWhenNamesEmpty(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	candidates, _, _, err := resolveUpgradeCandidates(context.Background(), nil, UpgradeOptions{HostID: "cover100", Env: env})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(candidates) != 1 || !candidates[0].isHost {
		t.Errorf("candidates = %+v, want exactly the host (nothing else installed)", candidates)
	}
}

func TestResolveUpgradeCandidates_UsesNamedWhenGiven(t *testing.T) {
	env := batchEnv(nil, fakeAbsDir("opt", "cover100"), nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	candidates, _, _, err := resolveUpgradeCandidates(context.Background(), []string{"ovdb"}, UpgradeOptions{HostID: "cover100", Env: env})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(candidates) != 1 || candidates[0].id != "ovdb" {
		t.Errorf("candidates = %+v, want exactly ovdb", candidates)
	}
}

// --- small io helpers used only by this file --------------------------------

func writeFile(path string, content []byte, perm os.FileMode) error {
	return os.WriteFile(path, content, perm)
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func archName() string {
	return runtime.GOARCH
}
