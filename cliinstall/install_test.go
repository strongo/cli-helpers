package cliinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/strongo/buildinfo"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- shared release-server fixtures ------------------------------------

func makeTarGz(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: binName, Mode: 0o755, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newReleaseServer serves a releases listing at /releases and, for each key
// of files, exactly that path — enough for Config.Check and Config.InstallNew
// to resolve, download and verify entirely offline
// (cli-install#req:no-network-in-tests).
func newReleaseServer(t *testing.T, releasesBody string, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(releasesBody))
			return
		}
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// noHostDir reports no host directory, matching how DefaultEnv itself
// behaves in a test process (its own exe isn't the CLI under test) —
// searchDirs treats that as "no host directory to search," not fatal.
func noHostDir() (string, error) { return "", errors.New("no host dir") }

func perTagDownloadURL(baseURL string) func(repository, tag, asset string) string {
	return func(_, tag, asset string) string {
		return baseURL + "/" + tag + "/" + asset
	}
}

// configureReleaseFromServer builds an Options.ConfigureRelease that points
// a target's Config at srv instead of the real GitHub API.
func configureReleaseFromServer(srv *httptest.Server) func(Entry, selfupdate.Config) selfupdate.Config {
	return func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
		cfg.ReleasesAPIURL = srv.URL + "/releases"
		cfg.DownloadURL = perTagDownloadURL(srv.URL)
		cfg.HTTPClient = srv.Client()
		return cfg
	}
}

// --- targetConfig --------------------------------------------------------

func TestTargetConfig_NoOverride(t *testing.T) {
	target := Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}
	cfg := targetConfig(target, Options{})
	if cfg.BinaryName != "ovdb" || cfg.Repository != "openvaultdb/ovdb" || cfg.CurrentVersion != "" {
		t.Errorf("targetConfig = %+v", cfg)
	}
}

func TestTargetConfig_WithOverride(t *testing.T) {
	target := Entry{ID: "ovdb"}
	called := false
	opts := Options{ConfigureRelease: func(e Entry, cfg selfupdate.Config) selfupdate.Config {
		called = true
		if e.ID != "ovdb" {
			t.Errorf("ConfigureRelease target = %q, want ovdb", e.ID)
		}
		cfg.ReleasesAPIURL = "http://example.invalid/releases"
		return cfg
	}}
	cfg := targetConfig(target, opts)
	if !called {
		t.Error("ConfigureRelease was not called")
	}
	if cfg.ReleasesAPIURL != "http://example.invalid/releases" {
		t.Errorf("ReleasesAPIURL = %q", cfg.ReleasesAPIURL)
	}
}

// --- dryRunResult ----------------------------------------------------------

func TestDryRunResult_Homebrew(t *testing.T) {
	target := Entry{ID: "ovdb"}
	got := dryRunResult(context.Background(), target, MethodHomebrew, "", "acme/tap/ovdb", Status{}, []string{"warn"}, Options{})
	if got.Outcome != OutcomeDryRun || got.Method != MethodHomebrew {
		t.Fatalf("dryRunResult = %+v", got)
	}
	if len(got.CaskArgv) == 0 || got.CaskArgv[len(got.CaskArgv)-1] != "acme/tap/ovdb" {
		t.Errorf("CaskArgv = %v", got.CaskArgv)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "warn" {
		t.Errorf("Warnings = %v", got.Warnings)
	}
}

func TestDryRunResult_DirectSuccess(t *testing.T) {
	srv := newReleaseServer(t, `[{"tag_name":"v1.2.3","prerelease":false,"draft":false}]`, nil)
	target := Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}
	opts := Options{ConfigureRelease: configureReleaseFromServer(srv)}

	got := dryRunResult(context.Background(), target, MethodDirect, "/home/alex/.local/bin", "", Status{}, nil, opts)
	if got.Outcome != OutcomeDryRun || got.Method != MethodDirect {
		t.Fatalf("dryRunResult = %+v", got)
	}
	if got.Destination != "/home/alex/.local/bin/ovdb" {
		t.Errorf("Destination = %q", got.Destination)
	}
	if got.Version != "1.2.3" || got.Tag != "v1.2.3" {
		t.Errorf("Version/Tag = %q/%q", got.Version, got.Tag)
	}
}

func TestDryRunResult_DirectReleaseLookupFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	target := Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}
	opts := Options{ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
		cfg.ReleasesAPIURL = srv.URL
		cfg.HTTPClient = srv.Client()
		return cfg
	}}

	got := dryRunResult(context.Background(), target, MethodDirect, "/home/alex/.local/bin", "", Status{}, nil, opts)
	if got.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want OutcomeFailed", got.Outcome)
	}
	if got.Failure == nil || got.Failure.Kind != selfupdate.KindReleaseLookup {
		t.Errorf("Failure = %v, want KindReleaseLookup", got.Failure)
	}
}

func TestPlannedTag(t *testing.T) {
	if got := plannedTag(Entry{TagPrefix: "cli-"}, "0.15.1"); got != "cli-v0.15.1" {
		t.Errorf("plannedTag = %q", got)
	}
	if got := plannedTag(Entry{}, "1.0.0"); got != "v1.0.0" {
		t.Errorf("plannedTag = %q", got)
	}
}

// --- executeInstall dispatch -------------------------------------------

func TestExecuteInstall_DispatchesHomebrew(t *testing.T) {
	opts := Options{Env: InstallEnv{RunManaged: func(context.Context, string, []string) error { return errors.New("no brew here") }}}
	got := executeInstall(context.Background(), Entry{ID: "ovdb"}, MethodHomebrew, "", "acme/tap/ovdb", false, nil, opts)
	if got.Method != MethodHomebrew {
		t.Fatalf("executeInstall did not dispatch to Homebrew: %+v", got)
	}
}

func TestExecuteInstall_DispatchesDirect(t *testing.T) {
	opts := Options{Env: InstallEnv{MkdirAll: func(string, os.FileMode) error { return errors.New("permission denied") }}}
	got := executeInstall(context.Background(), Entry{ID: "ovdb"}, MethodDirect, "/home/alex/.local/bin", "", true, nil, opts)
	if got.Method != MethodDirect || got.Failure == nil || got.Failure.Kind != selfupdate.KindPermission {
		t.Fatalf("executeInstall did not dispatch to executeDirectInstall: %+v", got)
	}
}

// --- executeDirectInstall --------------------------------------------------

func directInstallFixture(t *testing.T) (*httptest.Server, []byte, string, string) {
	t.Helper()
	version := "1.2.3"
	tag := "v1.2.3"
	binContent := []byte("the installed binary")
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	asset := fmt.Sprintf("ovdb_%s_%s_%s.%s", version, runtime.GOOS, runtime.GOARCH, ext)
	archive := makeTarGz(t, "ovdb", binContent)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)
	srv := newReleaseServer(t, `[{"tag_name":"`+tag+`","prerelease":false,"draft":false}]`, map[string][]byte{
		"/" + tag + "/" + asset:                           archive,
		"/" + tag + "/ovdb_" + version + "_checksums.txt": []byte(checksums),
	})
	return srv, binContent, version, tag
}

func TestExecuteDirectInstall_Success(t *testing.T) {
	srv, binContent, version, tag := directInstallFixture(t)
	destDir := t.TempDir()

	jr := jsonRun("ovdb", version, "abc123", "2026-01-01T00:00:00Z", buildinfo.DateSourceBuild)
	env := InstallEnv{
		Env: Env{
			HostDir:      noHostDir,
			PathDirs:     func() []string { return []string{destDir} },
			IsExecutable: func(p string) bool { return p == filepath.Join(destDir, "ovdb") },
			Run:          jr,
		},
	}
	opts := Options{Env: env, ConfigureRelease: configureReleaseFromServer(srv)}

	got := executeDirectInstall(context.Background(), Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}, destDir, false, nil, opts)
	if got.Outcome != OutcomeInstalled {
		t.Fatalf("Outcome = %v, want OutcomeInstalled; failure=%v", got.Outcome, got.Failure)
	}
	if got.Version != version || got.Tag != tag {
		t.Errorf("Version/Tag = %q/%q", got.Version, got.Tag)
	}
	content, err := os.ReadFile(filepath.Join(destDir, "ovdb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, binContent) {
		t.Errorf("installed content = %q, want %q", content, binContent)
	}
	if got.Status.State != Installed {
		t.Errorf("post-install Status.State = %v, want Installed", got.Status.State)
	}
	foundHint := false
	for _, w := range got.Warnings {
		if strings.Contains(w, "hash -r") {
			foundHint = true
		}
	}
	if !foundHint {
		t.Errorf("Warnings = %v, want the shell-cache-refresh hint", got.Warnings)
	}
}

func TestExecuteDirectInstall_NotOnPathWarning(t *testing.T) {
	srv, _, version, _ := directInstallFixture(t)
	destDir := t.TempDir()

	jr := jsonRun("ovdb", version, "", "", "")
	env := InstallEnv{
		Env: Env{
			HostDir:      noHostDir,
			PathDirs:     func() []string { return nil }, // destDir never on PATH
			IsExecutable: func(p string) bool { return p == filepath.Join(destDir, "ovdb") },
			Run:          jr,
		},
	}
	opts := Options{Env: env, ConfigureRelease: configureReleaseFromServer(srv)}

	got := executeDirectInstall(context.Background(), Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}, destDir, false, nil, opts)
	if got.Outcome != OutcomeInstalled {
		t.Fatalf("Outcome = %v, want OutcomeInstalled; failure=%v", got.Outcome, got.Failure)
	}
	found := false
	for _, w := range got.Warnings {
		if strings.Contains(w, "not on PATH") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a not-on-PATH warning", got.Warnings)
	}
}

func TestExecuteDirectInstall_MkdirFails(t *testing.T) {
	env := InstallEnv{MkdirAll: func(string, os.FileMode) error { return errors.New("permission denied") }}
	opts := Options{Env: env}

	got := executeDirectInstall(context.Background(), Entry{ID: "ovdb"}, "/home/alex/.local/bin", true, nil, opts)
	if got.Outcome != OutcomeFailed || got.Failure == nil || got.Failure.Kind != selfupdate.KindPermission {
		t.Fatalf("executeDirectInstall = %+v, want a KindPermission failure", got)
	}
}

func TestExecuteDirectInstall_InstallNewFails(t *testing.T) {
	srv := newReleaseServer(t, `[{"tag_name":"v1.2.3","prerelease":false,"draft":false}]`, nil) // no asset/checksums published
	destDir := t.TempDir()
	opts := Options{Env: InstallEnv{}, ConfigureRelease: configureReleaseFromServer(srv)}

	got := executeDirectInstall(context.Background(), Entry{ID: "ovdb", Repository: "openvaultdb/ovdb"}, destDir, false, nil, opts)
	if got.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %v, want OutcomeFailed", got.Outcome)
	}
	if got.Failure == nil {
		t.Fatal("Failure = nil, want a typed failure")
	}
}

// --- executeHomebrewInstall --------------------------------------------

func TestExecuteHomebrewInstall_NoRunnerConfigured(t *testing.T) {
	got := executeHomebrewInstall(context.Background(), Entry{ID: "ovdb"}, "acme/tap/ovdb", nil, Options{})
	if got.Outcome != OutcomeFailed || got.Failure == nil || got.Failure.Kind != selfupdate.KindManagedCommand {
		t.Fatalf("executeHomebrewInstall = %+v, want KindManagedCommand", got)
	}
}

func TestExecuteHomebrewInstall_RunManagedFails(t *testing.T) {
	var gotExecutable string
	var gotArgs []string
	env := InstallEnv{RunManaged: func(_ context.Context, executable string, args []string) error {
		gotExecutable, gotArgs = executable, args
		return errors.New("exit status 1")
	}}
	got := executeHomebrewInstall(context.Background(), Entry{ID: "ovdb"}, "acme/tap/ovdb", nil, Options{Env: env})
	if got.Outcome != OutcomeFailed || got.Failure == nil || got.Failure.Kind != selfupdate.KindManagedCommand {
		t.Fatalf("executeHomebrewInstall = %+v, want KindManagedCommand", got)
	}
	if gotExecutable != "brew" {
		t.Errorf("executable = %q, want brew", gotExecutable)
	}
	want := []string{"install", "--cask", "acme/tap/ovdb"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}
	if !strings.Contains(got.Failure.Error(), "brew update") {
		t.Errorf("Failure = %q, want it to name `brew update` as a remedy", got.Failure.Error())
	}
}

func TestExecuteHomebrewInstall_Success(t *testing.T) {
	jr := jsonRun("ovdb", "1.2.3", "abc", "2026-01-01T00:00:00Z", buildinfo.DateSourceBuild)
	env := InstallEnv{
		Env: Env{
			HostDir:      noHostDir,
			PathDirs:     func() []string { return []string{"/opt/homebrew/bin"} },
			IsExecutable: func(p string) bool { return p == "/opt/homebrew/bin/ovdb" },
			Run:          jr,
		},
		RunManaged: func(context.Context, string, []string) error { return nil },
	}
	got := executeHomebrewInstall(context.Background(), Entry{ID: "ovdb"}, "acme/tap/ovdb", nil, Options{Env: env})
	if got.Outcome != OutcomeInstalled {
		t.Fatalf("Outcome = %v, want OutcomeInstalled", got.Outcome)
	}
	if got.Version != "1.2.3" {
		t.Errorf("Version = %q", got.Version)
	}
	if got.Destination != "" {
		t.Errorf("Destination = %q, want empty for a Homebrew install", got.Destination)
	}
}

// --- verifyInstalled --------------------------------------------------------

func TestVerifyInstalled_DirectMatch(t *testing.T) {
	jr := jsonRun("ovdb", "1.2.3", "abc", "2026-01-01T00:00:00Z", buildinfo.DateSourceBuild)
	env := Env{
		HostDir:      noHostDir,
		PathDirs:     func() []string { return []string{"/bin"} },
		IsExecutable: func(p string) bool { return p == "/bin/ovdb" },
		Run:          jr,
	}
	status, warnings := verifyInstalled(context.Background(), Entry{ID: "ovdb"}, Options{Env: InstallEnv{Env: env}}, MethodDirect, "/bin/ovdb", "1.2.3")
	if status.State != Installed {
		t.Fatalf("status.State = %v, want Installed", status.State)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestVerifyInstalled_DirectVerificationFailed(t *testing.T) {
	env := Env{
		HostDir:      noHostDir,
		PathDirs:     func() []string { return nil },
		IsExecutable: func(string) bool { return false },
		Run:          func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") },
	}
	status, warnings := verifyInstalled(context.Background(), Entry{ID: "ovdb"}, Options{Env: InstallEnv{Env: env}}, MethodDirect, "/bin/ovdb", "1.2.3")
	if status.State == Installed {
		t.Fatal("status.State = Installed, want not-installed since nothing was located")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "/bin/ovdb") {
		t.Errorf("warnings = %v, want a warning naming the destination", warnings)
	}
}

func TestVerifyInstalled_HomebrewVerificationFailedNamesQuarantine(t *testing.T) {
	env := Env{
		HostDir:      noHostDir,
		PathDirs:     func() []string { return nil },
		IsExecutable: func(string) bool { return false },
		Run:          func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") },
	}
	_, warnings := verifyInstalled(context.Background(), Entry{ID: "ovdb"}, Options{Env: InstallEnv{Env: env}}, MethodHomebrew, "", "")
	if len(warnings) == 0 || !strings.Contains(warnings[0], "quarantine") {
		t.Errorf("warnings = %v, want a quarantine remedy for a Homebrew install", warnings)
	}
}

func TestVerifyInstalled_VersionMismatch(t *testing.T) {
	jr := jsonRun("ovdb", "9.9.9", "", "", "")
	env := Env{
		HostDir:      noHostDir,
		PathDirs:     func() []string { return []string{"/bin"} },
		IsExecutable: func(p string) bool { return p == "/bin/ovdb" },
		Run:          jr,
	}
	_, warnings := verifyInstalled(context.Background(), Entry{ID: "ovdb"}, Options{Env: InstallEnv{Env: env}}, MethodDirect, "/bin/ovdb", "1.2.3")
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "9.9.9") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a version-mismatch warning naming 9.9.9", warnings)
	}
}

func TestVerifyInstalled_DifferentCopyOnPath(t *testing.T) {
	jr := jsonRun("ovdb", "1.2.3", "", "", "")
	env := Env{
		HostDir:      noHostDir,
		PathDirs:     func() []string { return []string{"/other"} },
		IsExecutable: func(p string) bool { return p == "/other/ovdb" },
		Run:          jr,
	}
	_, warnings := verifyInstalled(context.Background(), Entry{ID: "ovdb"}, Options{Env: InstallEnv{Env: env}}, MethodDirect, "/bin/ovdb", "1.2.3")
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "/other/ovdb") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a warning naming the different copy found on PATH", warnings)
	}
}

func TestVerifyInstalled_HomebrewSuccessSkipsDirectOnlyChecks(t *testing.T) {
	jr := jsonRun("ovdb", "1.2.3", "", "", "")
	env := Env{
		HostDir:      noHostDir,
		PathDirs:     func() []string { return []string{"/opt/homebrew/bin"} },
		IsExecutable: func(p string) bool { return p == "/opt/homebrew/bin/ovdb" },
		Run:          jr,
	}
	status, warnings := verifyInstalled(context.Background(), Entry{ID: "ovdb"}, Options{Env: InstallEnv{Env: env}}, MethodHomebrew, "", "")
	if status.State != Installed {
		t.Fatalf("status.State = %v, want Installed", status.State)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (no expected version/path to compare against for Homebrew)", warnings)
	}
}
