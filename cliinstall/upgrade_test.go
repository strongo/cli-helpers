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
// Config.LatestRelease/UpdateAt calls in the same test resolve entirely
// offline (cli-install#req:no-network-in-tests).
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

func TestVersionFromTag(t *testing.T) {
	if got := versionFromTag("cli-v1.2.3", "cli-"); got != "1.2.3" {
		t.Errorf("versionFromTag = %q", got)
	}
	if got := versionFromTag("v1.2.3", ""); got != "1.2.3" {
		t.Errorf("versionFromTag = %q", got)
	}
}

func TestNormalizeVersion(t *testing.T) {
	if got := normalizeVersion(" v1.2.3 "); got != "1.2.3" {
		t.Errorf("normalizeVersion = %q", got)
	}
}

func TestCompareVersion(t *testing.T) {
	if current, latest, verdict := compareVersion(selfupdate.Config{CurrentVersion: "1.0.0"}, "v1.1.0"); current != "1.0.0" || latest != "1.1.0" || verdict != selfupdate.UpdateAvailable {
		t.Errorf("got %q %q %v", current, latest, verdict)
	}
	if _, _, verdict := compareVersion(selfupdate.Config{CurrentVersion: "1.1.0"}, "v1.1.0"); verdict != selfupdate.UpToDate {
		t.Errorf("verdict = %v, want UpToDate", verdict)
	}
	if _, _, verdict := compareVersion(selfupdate.Config{CurrentVersion: "2.0.0"}, "v1.1.0"); verdict != selfupdate.Ahead {
		t.Errorf("verdict = %v, want Ahead", verdict)
	}
	if current, _, verdict := compareVersion(selfupdate.Config{CurrentVersion: "dev"}, "v1.1.0"); verdict != selfupdate.Undetermined || current != "dev" {
		t.Errorf("got %q %v, want dev/Undetermined", current, verdict)
	}
	if _, latest, _ := compareVersion(selfupdate.Config{CurrentVersion: "1.0.0", TagPrefix: "cli-"}, "cli-v1.2.3"); latest != "1.2.3" {
		t.Errorf("latest = %q, want 1.2.3", latest)
	}
}

func TestDecidePlan(t *testing.T) {
	redirectOnly := selfupdate.Homebrew("brew upgrade --cask x")
	executable := selfupdate.HomebrewCask("x")

	cases := []struct {
		name               string
		nonReleaseProceeds bool
		verdict            selfupdate.Verdict
		method             selfupdate.InstallMethod
		manager            *selfupdate.Manager
		want               UpgradeOutcome
	}{
		{"ahead", false, selfupdate.Ahead, selfupdate.Manual, nil, UpgradeOutcomeAhead},
		{"up to date", false, selfupdate.UpToDate, selfupdate.Manual, nil, UpgradeOutcomeAlreadyCurrent},
		{"undetermined not explicit", false, selfupdate.Undetermined, selfupdate.Manual, nil, UpgradeOutcomeAlreadyCurrent},
		{"undetermined explicit", true, selfupdate.Undetermined, selfupdate.Manual, nil, UpgradeOutcomeDryRun},
		{"available manual", false, selfupdate.UpdateAvailable, selfupdate.Manual, nil, UpgradeOutcomeDryRun},
		{"available ambiguous", false, selfupdate.UpdateAvailable, selfupdate.Ambiguous, nil, UpgradeOutcomeRefused},
		{"available managed redirect", false, selfupdate.UpdateAvailable, selfupdate.Managed, &redirectOnly, UpgradeOutcomeRedirected},
		{"available managed nil manager", false, selfupdate.UpdateAvailable, selfupdate.Managed, nil, UpgradeOutcomeRedirected},
		{"available managed executable", false, selfupdate.UpdateAvailable, selfupdate.Managed, &executable, UpgradeOutcomeDryRun},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decidePlan(c.nonReleaseProceeds, c.verdict, c.method, c.manager); got != c.want {
				t.Errorf("decidePlan(...) = %v, want %v", got, c.want)
			}
		})
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
	c := githubHTTPClient(getenv)
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
	})
	tr := c.Transport.(*githubAuthTransport)
	if tr.token != "githubtoken" {
		t.Errorf("token = %q", tr.token)
	}
}

func TestGithubHTTPClient_NilGetenv(t *testing.T) {
	c := githubHTTPClient(nil)
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

func TestWithGitHubAuth_PreservesExisting(t *testing.T) {
	custom := &http.Client{}
	cfg := withGitHubAuth(selfupdate.Config{HTTPClient: custom}, func(string) string { return "" })
	if cfg.HTTPClient != custom {
		t.Error("existing HTTPClient was overwritten")
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

// --- target selection ------------------------------------------------------

func TestPlanUpgrade_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("PlanUpgrade did not panic for a host id absent from the catalog")
		}
	}()
	_, _ = PlanUpgrade(context.Background(), nil, UpgradeOptions{HostID: "nosuchhost"})
}

func TestPlanUpgrade_UnknownNameFailsWholeBatch(t *testing.T) {
	probed := false
	run := func(context.Context, string, []string) ([]byte, error) {
		probed = true
		return nil, errors.New("must not be called")
	}
	env := batchEnv(nil, "/opt/cover100", nil, run, noRunManaged)
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
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
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

func TestPlanUpgrade_HostDirErrorTreatedAsEmpty(t *testing.T) {
	env := batchEnv(nil, "", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	env.HostDir = func() (string, error) { return "", errors.New("os.Executable failed") }
	opts := UpgradeOptions{HostID: "cover100", Env: env, HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"}}

	result, err := PlanUpgrade(context.Background(), []string{"cover100"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %v", result.Results)
	}
	// "dev" is a non-release build and cover100 was named explicitly but is
	// its own host, so it is offered — but with an empty HostDir there is
	// simply no PATH-copy comparison to make; this asserts no panic and a
	// sane classification (Ambiguous, since cover100 has no managers and
	// installFilePath("", "cover100") does not look like a bin directory).
	if result.Results[0].InstallMethod != selfupdate.Ambiguous {
		t.Errorf("InstallMethod = %v, want Ambiguous", result.Results[0].InstallMethod)
	}
}

// --- non-release build skipping -------------------------------------------

func TestPlanUpgrade_NonReleaseBuildSkippedUnderAll(t *testing.T) {
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "0.20.3+dirty"),
		noRunManaged,
	)
	opts := UpgradeOptions{HostID: "cover100", All: true, Env: env, HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "dev"}}

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
	if ovdbResult == nil || ovdbResult.Outcome != UpgradeOutcomeSkippedNonRelease {
		t.Errorf("ovdb result = %+v, want SkippedNonRelease", ovdbResult)
	}
	if ovdbResult.Latest != "" || ovdbResult.Tag != "" {
		t.Errorf("ovdb result carries lookup data despite never being looked up: %+v", ovdbResult)
	}
	if hostResult == nil || hostResult.Outcome != UpgradeOutcomeSkippedNonRelease {
		t.Errorf("host result = %+v, want SkippedNonRelease (dev build under --all)", hostResult)
	}
}

func TestPlanUpgrade_NonReleaseBuildProceedsWhenExplicit(t *testing.T) {
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "dev"),
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
	if r.Tag != "v1.0.0" || r.Latest != "1.0.0" {
		t.Errorf("r = %+v, want a real lookup to have run", r)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "non-release build") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a non-release-build notice", r.Warnings)
	}
}

// --- --all / bare-report target selection ---------------------------------

func TestPlanUpgrade_AllSelectsInstalledPlusHost(t *testing.T) {
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"),
		noRunManaged,
	)
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v1.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		ConfigureRelease: configureUpgradeRelease(srv),
		HostConfig:       hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
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
	env := batchEnv(nil, "/opt/cover100", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("none") }, noRunManaged)
	srv := upgradeReleaseServer(t, nil, nil)
	opts := UpgradeOptions{
		HostID: "cover100", Env: env, // All left false, names left nil
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
	}
	// Register cover100's own releases so its lookup succeeds.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/cover100" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(releasesJSON("v1.0.0")))
			return
		}
		http.NotFound(w, r)
	})

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
}

// --- release lookup outcomes ------------------------------------------------

func TestPlanUpgrade_AlreadyCurrentAndAhead(t *testing.T) {
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true, "/usr/bin/synchestra": true},
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

func TestPlanUpgrade_ManagedRedirectAndExecutable(t *testing.T) {
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/opt/homebrew/bin/ingitdb": true, "/opt/homebrew/bin/wb": true},
		multiJSONRun(map[string]string{"ingitdb": "1.0.0", "wb": "1.0.0"}),
		noRunManaged,
	)
	env.PathDirs = func() []string { return []string{"/opt/homebrew/bin"} }
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

func TestPlanUpgrade_AmbiguousRefused(t *testing.T) {
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"),
		noRunManaged,
	)
	// A path that classifies neither Manual (no "bin" ancestor, no go/bin)
	// nor any manager: /usr/bin ends in "bin" so it WOULD be Manual — use a
	// non-bin PATH entry instead to force Ambiguous.
	env.PathDirs = func() []string { return []string{"/opt/weird"} }
	env.IsExecutable = func(p string) bool { return p == "/opt/weird/ovdb" }
	env.Run = jsonRunFor("/opt/weird/ovdb", "ovdb", "1.0.0")
	srv := upgradeReleaseServer(t, map[string]string{"ovdb": releasesJSON("v2.0.0")}, nil)
	opts := UpgradeOptions{HostID: "cover100", Env: env, ConfigureRelease: configureUpgradeRelease(srv)}

	result, err := PlanUpgrade(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeRefused {
		t.Errorf("Outcome = %v, want Refused", r.Outcome)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "ambiguous") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want ambiguous guidance", r.Warnings)
	}
}

func TestPlanUpgrade_LookupFailure(t *testing.T) {
	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"),
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

func TestPlanUpgrade_RateLimitMessage(t *testing.T) {
	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(rateLimited.Close)

	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"),
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
	if !strings.Contains(r.Failure.Error(), "GH_TOKEN") {
		t.Errorf("Failure message %q does not name GH_TOKEN", r.Failure.Error())
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
		executables["/usr/bin/"+id] = true
		versions[id] = "0.1.0"
	}
	env := batchEnv([]string{"/usr/bin"}, "/opt/cover100", executables, multiJSONRun(versions), noRunManaged)
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
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	env := batchEnv(
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/ovdb": true},
		jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"),
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
		ResolvedPath: "/usr/bin/" + target, Current: current, Tag: tag,
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
		[]string{dir}, "/opt/cover100",
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

	result, err := ExecuteUpgrade(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("ExecuteUpgrade error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != UpgradeOutcomeUpgraded {
		t.Fatalf("Outcome = %v, want Upgraded: %+v", r.Outcome, r)
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
	env := batchEnv(nil, "/opt/cover100", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
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
		[]string{"/opt/homebrew/bin"}, "/opt/cover100",
		map[string]bool{"/opt/homebrew/bin/wb": true},
		func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != "/opt/homebrew/bin/wb" || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
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

	opts := UpgradeOptions{HostID: "cover100", Yes: true, Env: env, ConfigureRelease: configureUpgradeRelease(srv)}
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
		[]string{"/opt/homebrew/bin"}, "/opt/homebrew/bin",
		map[string]bool{"/opt/homebrew/bin/wb": true},
		func(_ context.Context, path string, args []string) ([]byte, error) {
			if path != "/opt/homebrew/bin/wb" || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
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

	hookCalled := false
	opts := UpgradeOptions{
		HostID: "wb", Yes: true, Env: env,
		HostConfig: hostReleaseConfig(srv, "wb", selfupdate.Config{
			BinaryName: "wb", CurrentVersion: "1.0.0",
			Managers: []selfupdate.Manager{selfupdate.HomebrewCask("wb")},
		}),
		HostAfterUpdate: func(context.Context, selfupdate.AfterUpdate) error {
			hookCalled = true
			return nil
		},
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

func TestUpgradeVerifyManaged(t *testing.T) {
	target := Entry{ID: "ovdb"}

	t.Run("not installed", func(t *testing.T) {
		env := batchEnv(nil, "", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
		verify := upgradeVerifyManaged(target, UpgradeOptions{Env: env})
		if _, err := verify(context.Background(), selfupdate.Detection{}, "", nil, "1.0.0"); err == nil {
			t.Error("expected an error for a not-installed copy")
		}
	})

	t.Run("version mismatch", func(t *testing.T) {
		env := batchEnv([]string{"/usr/bin"}, "", map[string]bool{"/usr/bin/ovdb": true}, jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"), noRunManaged)
		verify := upgradeVerifyManaged(target, UpgradeOptions{Env: env})
		if _, err := verify(context.Background(), selfupdate.Detection{}, "", nil, "2.0.0"); err == nil {
			t.Error("expected an error for a version mismatch")
		}
	})

	t.Run("success reports Path and Status.ResolvedPath", func(t *testing.T) {
		env := batchEnv([]string{"/usr/bin"}, "", map[string]bool{"/usr/bin/ovdb": true}, jsonRunFor("/usr/bin/ovdb", "ovdb", "1.0.0"), noRunManaged)
		env.EvalSymlinks = nil
		verify := upgradeVerifyManaged(target, UpgradeOptions{Env: env})
		id, err := verify(context.Background(), selfupdate.Detection{}, "", nil, "1.0.0")
		if err != nil {
			t.Fatalf("verify error = %v", err)
		}
		if id.Path != "/usr/bin/ovdb" || id.ResolvedPath != "/usr/bin/ovdb" {
			t.Errorf("identity = %+v", id)
		}
	})
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
	// A direct unit check that executeHostUpgrade always passes hooks=false
	// to finalizeUpgradeResult, regardless of the host's own catalog entry
	// — verified end to end via wb-as-host below, since wb.SelfUpdateHooks
	// is true and would otherwise be the one case that could leak a hint.
	srv := upgradeReleaseServer(t, map[string]string{"wb": releasesJSON("v1.0.0")}, nil)
	env := batchEnv(nil, "/opt/wb", map[string]bool{"/opt/wb/wb": true}, jsonRunFor("/opt/wb/wb", "wb", "1.0.0"), noRunManaged)
	opts := UpgradeOptions{
		HostID: "wb", Yes: true, Env: env,
		HostConfig: hostReleaseConfig(srv, "wb", selfupdate.Config{BinaryName: "wb", CurrentVersion: "1.0.0"}),
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
		[]string{"/usr/bin"}, "/opt/cover100",
		map[string]bool{"/usr/bin/cover100": true},
		jsonRunFor("/usr/bin/cover100", "cover100", "1.0.0"),
		noRunManaged,
	)
	opts := UpgradeOptions{HostID: "cover100", Env: env, HostConfig: selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}}

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
}

func TestPlanUpgrade_HostAllIncludedEvenWhenNotOnPath(t *testing.T) {
	env := batchEnv(nil, "/opt/cover100", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
	srv := upgradeReleaseServer(t, map[string]string{"cover100": releasesJSON("v1.0.0")}, nil)
	opts := UpgradeOptions{
		HostID: "cover100", All: true, Env: env,
		HostConfig: hostReleaseConfig(srv, "cover100", selfupdate.Config{BinaryName: "cover100", CurrentVersion: "1.0.0"}),
	}
	result, err := PlanUpgrade(context.Background(), nil, opts)
	if err != nil {
		t.Fatalf("PlanUpgrade error = %v", err)
	}
	if len(result.Results) != 1 || !result.Results[0].Host {
		t.Fatalf("Results = %+v, want exactly the host row", result.Results)
	}
}

// --- candidate-building direct unit tests ---------------------------------

func TestNamedUpgradeCandidates_Dedupe(t *testing.T) {
	env := batchEnv(nil, "/opt/cover100", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
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
	env := batchEnv(nil, "/opt/wb", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("x") }, noRunManaged)
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
