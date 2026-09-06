package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// updateHarness wires a Config against an httptest.Server for both the
// releases API and per-tag downloads, and against a real (but throwaway,
// t.TempDir()-scoped) file standing in for "the installed executable" — the
// DetectSelf seams point at it instead of the real test-binary path, which
// is what REQ: no-network-in-tests means by "MUST NOT replace a real
// installed binary": this file is not one.
type updateHarness struct {
	t       *testing.T
	dir     string
	target  string
	server  *httptest.Server
	files   map[string][]byte
	hits    int32
	cfg     Config
	current string
}

// newUpdateHarness creates the target file at manualPath (relative to the
// harness's temp dir; use "bin/wb" for a Manual classification, or a path
// containing e.g. "Cellar/wb/1.0/bin/wb" for Managed) and points the
// DetectSelf seams at it.
func newUpdateHarness(t *testing.T, relPath, initialContent string) *updateHarness {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(initialContent), 0o755); err != nil {
		t.Fatal(err)
	}

	h := &updateHarness{t: t, dir: dir, target: target, files: map[string][]byte{}}

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&h.hits, 1)
		if body, ok := h.files[r.URL.Path]; ok {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.server.Close)

	origExe, origEval := osExecutable, evalSymlinksFunc
	osExecutable = func() (string, error) { return target, nil }
	evalSymlinksFunc = func(p string) (string, error) { return p, nil }
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	h.current = "1.0.0"
	h.cfg = Config{
		BinaryName:     "wb",
		Repository:     "acme/wb",
		CurrentVersion: h.current,
		ReleasesAPIURL: h.server.URL + "/releases",
		DownloadURL: func(_, tag, asset string) string {
			return h.server.URL + "/dl/" + tag + "/" + asset
		},
		HTTPClient: h.server.Client(),
	}
	return h
}

// setReleases registers the releases-listing JSON body.
func (h *updateHarness) setReleases(body string) {
	h.files["/releases"] = []byte(body)
}

// addRelease registers a stable (non-prerelease, non-draft) release whose
// asset is a shell script that echoes "wb version <version>" — enough for
// verifyBinaryVersion's default {"--version"} probe to confirm a successful
// swap. tag and version may differ only by a leading "v".
func (h *updateHarness) addAsset(tag, version, scriptBody string) {
	asset := defaultAssetName(h.cfg.BinaryName, version, goosName, goarchName)
	archive := makeTarGzScript(h.t, h.cfg.BinaryName, scriptBody)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)
	h.files["/dl/"+tag+"/"+asset] = archive
	h.files["/dl/"+tag+"/"+defaultChecksumsName(h.cfg.BinaryName, version)] = []byte(checksums)
}

// addAssetWrongChecksum registers an asset whose checksums file lists an
// incorrect digest, for exercising the checksum-mismatch path end to end.
func (h *updateHarness) addAssetWrongChecksum(tag, version string) {
	asset := defaultAssetName(h.cfg.BinaryName, version, goosName, goarchName)
	archive := makeTarGzScript(h.t, h.cfg.BinaryName, "#!/bin/sh\necho wrong\n")
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex([]byte("not the archive")), asset)
	h.files["/dl/"+tag+"/"+asset] = archive
	h.files["/dl/"+tag+"/"+defaultChecksumsName(h.cfg.BinaryName, version)] = []byte(checksums)
}

func (h *updateHarness) targetBytes() string {
	h.t.Helper()
	b, err := os.ReadFile(h.target)
	if err != nil {
		h.t.Fatalf("read target: %v", err)
	}
	return string(b)
}

func makeTarGzScript(t *testing.T, binName, scriptBody string) []byte {
	t.Helper()
	return makeTarGz(t, binName, []byte(scriptBody))
}

func stableReleaseJSON(tags ...string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, tag := range tags {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"tag_name":%q,"prerelease":false,"draft":false}`, tag)
	}
	b.WriteString("]")
	return b.String()
}

// --- Managed: never touches anything, no option combination reaches it ---

// A package-manager update cannot promise an arbitrary historical version,
// so its pin is refused before either availability lookup or confirmation.
func TestUpdate_ManagedRedirect_RefusesPinBeforeLookupAndConfirm(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}

	confirmCalled := false
	_, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "9.9.9",
		Confirm:       func(string) (bool, error) { confirmCalled = true; return true, nil },
	})
	if KindOf(err) != KindManagedVersion {
		t.Fatalf("KindOf(err) = %v, want KindManagedVersion", KindOf(err))
	}
	if confirmCalled {
		t.Error("Confirm was called for a managed install; it must never be reached")
	}
	if atomic.LoadInt32(&h.hits) != 0 {
		t.Errorf("HTTP requests were made (%d) for a managed install; expected none", h.hits)
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified for a managed install")
	}
}

// A package manager remains the installation authority, but managed users
// still need the running and latest published versions before the redirect.
func TestUpdate_ManagedRedirectReportsAvailability(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}
	h.setReleases(stableReleaseJSON("v1.1.0"))

	outcome, err := h.cfg.Update(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if outcome.Action != ActionRedirected {
		t.Fatalf("Action = %v, want ActionRedirected", outcome.Action)
	}
	if outcome.Result.Current != "1.0.0" || outcome.Result.Latest != "1.1.0" {
		t.Errorf("Result = %+v, want current 1.0.0 and latest 1.1.0", outcome.Result)
	}
}

func TestUpdate_ManagedAvailabilityReportsNewerEqualAndUnknownCurrent(t *testing.T) {
	for _, tc := range []struct {
		name, current, latest string
		wantVerdict           Verdict
	}{
		{name: "newer", current: "1.0.0", latest: "1.1.0", wantVerdict: UpdateAvailable},
		{name: "equal", current: "1.1.0", latest: "1.1.0", wantVerdict: UpToDate},
		{name: "unknown", current: "dev", latest: "1.1.0", wantVerdict: Undetermined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
			h.cfg.CurrentVersion = tc.current
			h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}
			h.setReleases(stableReleaseJSON("v" + tc.latest))
			var report Availability
			outcome, err := h.cfg.Update(context.Background(), Options{ReportAvailability: func(got Availability) { report = got }})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if outcome.Result.Current != tc.current || outcome.Result.Latest != tc.latest || outcome.Result.Verdict != tc.wantVerdict {
				t.Errorf("Outcome.Result = %+v, want current=%q latest=%q verdict=%v", outcome.Result, tc.current, tc.latest, tc.wantVerdict)
			}
			if report.Result != outcome.Result || report.Warning != nil || report.Detection.Manager == nil {
				t.Errorf("availability report = %+v, want successful managed report", report)
			}
		})
	}
}

func TestUpdate_ManagedAvailabilityLookupFailureIsAdvisory(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}
	var report Availability
	outcome, err := h.cfg.Update(context.Background(), Options{ReportAvailability: func(got Availability) { report = got }})
	if err != nil {
		t.Fatalf("Update() error = %v, want redirect despite lookup failure", err)
	}
	if outcome.Action != ActionRedirected || outcome.ReleaseCheckWarning == nil {
		t.Fatalf("Outcome = %+v, want redirected advisory warning", outcome)
	}
	if outcome.Result.Current != "1.0.0" || outcome.Result.Latest != "" || report.Warning == nil {
		t.Errorf("availability = %+v, want current with unavailable latest", report)
	}
}

func TestUpdate_ManagedAvailabilityLookupHasBoundedDeadline(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}
	h.cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	previous := managedAvailabilityTimeout
	managedAvailabilityTimeout = 10 * time.Millisecond
	t.Cleanup(func() { managedAvailabilityTimeout = previous })

	outcome, err := h.cfg.Update(context.Background(), Options{})
	if err != nil || outcome.Action != ActionRedirected || outcome.ReleaseCheckWarning == nil {
		t.Fatalf("Outcome/error = %+v/%v, want redirected advisory timeout", outcome, err)
	}
}

func TestUpdate_ManagedAvailabilityReportPrecedesConfirmationAndRunner(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb")}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	var events []string
	_, err := h.cfg.Update(context.Background(), Options{
		ReportAvailability: func(Availability) { events = append(events, "report") },
		Confirm:            func(string) (bool, error) { events = append(events, "confirm"); return true, nil },
		RunManaged:         func(context.Context, string, []string) error { events = append(events, "run"); return nil },
		VerifyManaged: func(context.Context, Detection, string, []string, string) (ExecutableIdentity, error) {
			return ExecutableIdentity{Path: "/tmp/wb", ResolvedPath: "/tmp/wb"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"report", "confirm", "run"}) {
		t.Errorf("event order = %v, want report before confirmation and runner", events)
	}
}

func TestUpdate_ManagedExecutable_ConfirmsRunsArgvAndVerifies(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	h.setReleases(stableReleaseJSON("v1.1.0"))

	var transition, executable string
	var args []string
	verified := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		Confirm: func(got string) (bool, error) { transition = got; return true, nil },
		RunManaged: func(_ context.Context, gotExecutable string, gotArgs []string) error {
			executable = gotExecutable
			args = append([]string(nil), gotArgs...)
			return nil
		},
		VerifyManaged: func(_ context.Context, detection Detection, binary string, probeArgs []string, expectedVersion string) (ExecutableIdentity, error) {
			verified = true
			if detection.Manager == nil || detection.Manager.Name != "Homebrew" {
				t.Errorf("managed detection = %+v, want Homebrew", detection)
			}
			if binary != "wb" || !reflect.DeepEqual(probeArgs, []string{"--version"}) {
				t.Errorf("managed probe = %q %v, want wb [--version]", binary, probeArgs)
			}
			if expectedVersion != "1.1.0" {
				t.Errorf("managed expected version = %q, want 1.1.0", expectedVersion)
			}
			return ExecutableIdentity{Path: "/tmp/wb", ResolvedPath: "/tmp/Cellar/wb/1.1.0/wb"}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionManagerExecuted {
		t.Fatalf("Action = %v, want ActionManagerExecuted", outcome.Action)
	}
	if !strings.Contains(transition, "Homebrew") || !strings.Contains(transition, "brew upgrade --cask wb") {
		t.Errorf("transition = %q, want manager and command", transition)
	}
	if executable != "brew" || !reflect.DeepEqual(args, []string{"upgrade", "--cask", "wb"}) {
		t.Errorf("managed command = %q %v, want brew [upgrade --cask wb]", executable, args)
	}
	if !verified {
		t.Error("managed update did not run the post-update version probe")
	}
	if outcome.PostSwapWarning != nil {
		t.Errorf("PostSwapWarning = %v, want nil", outcome.PostSwapWarning)
	}
	if atomic.LoadInt32(&h.hits) != 1 {
		t.Errorf("release requests = %d, want one advisory availability lookup", h.hits)
	}
	if h.targetBytes() != "old binary" {
		t.Error("core replaced a package-manager-owned binary")
	}
}

func TestUpdate_ManagedExecutable_RunsRefreshAndUpgradeInOrder(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew update && brew upgrade --cask wb").WithExecutableUpgradeSteps(
			ManagedCommand{Executable: "brew", Args: []string{"update"}},
			ManagedCommand{Executable: "brew", Args: []string{"upgrade", "--cask", "wb"}},
		),
	}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	var calls []string
	outcome, err := h.cfg.Update(context.Background(), Options{
		RunManaged: func(_ context.Context, executable string, args []string) error {
			calls = append(calls, executable+" "+strings.Join(args, " "))
			return nil
		},
		VerifyManaged: func(_ context.Context, _ Detection, _ string, _ []string, expectedVersion string) (ExecutableIdentity, error) {
			calls = append(calls, "verify "+expectedVersion)
			return ExecutableIdentity{Path: "/tmp/wb", ResolvedPath: "/tmp/Cellar/wb/1.1.0/wb"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if outcome.Action != ActionManagerExecuted {
		t.Fatalf("Action = %s, want manager_executed", outcome.Action)
	}
	want := []string{"brew update", "brew upgrade --cask wb", "verify 1.1.0"}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestUpdate_ManagedExecutable_StopsAfterFailedRefresh(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew update && brew upgrade --cask wb").WithExecutableUpgradeSteps(
			ManagedCommand{Executable: "brew", Args: []string{"update"}},
			ManagedCommand{Executable: "brew", Args: []string{"upgrade", "--cask", "wb"}},
		),
	}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	var calls []string
	_, err := h.cfg.Update(context.Background(), Options{
		RunManaged: func(_ context.Context, executable string, args []string) error {
			calls = append(calls, executable+" "+strings.Join(args, " "))
			return errors.New("refresh failed")
		},
		VerifyManaged: func(context.Context, Detection, string, []string, string) (ExecutableIdentity, error) {
			t.Fatal("verification ran after a failed refresh")
			return ExecutableIdentity{}, nil
		},
	})
	if KindOf(err) != KindManagedCommand || !strings.Contains(err.Error(), "step 1/2") {
		t.Fatalf("error = %v, want typed first-step failure", err)
	}
	if !reflect.DeepEqual(calls, []string{"brew update"}) {
		t.Errorf("calls = %v, want only the failed refresh", calls)
	}
}

func TestUpdate_ManagedExecutable_DryRunReportsCommandWithoutExecuting(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	run := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		DryRun: true,
		RunManaged: func(context.Context, string, []string) error {
			run = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionPlanned || outcome.PlannedCommand != "brew upgrade --cask wb" {
		t.Errorf("outcome = %+v, want planned Homebrew command", outcome)
	}
	if outcome.Result.Current != "1.0.0" || outcome.Result.Latest != "1.1.0" {
		t.Errorf("planned Result = %+v, want current/latest availability", outcome.Result)
	}
	if run {
		t.Error("managed command ran during --dry-run")
	}
}

func TestUpdate_ManagedExecutable_RefusesVersionPinThatIsNotLatest(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	run := false
	_, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "1.2.3",
		RunManaged: func(context.Context, string, []string) error {
			run = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected a managed version-pin refusal")
	}
	if KindOf(err) != KindManagedVersion {
		t.Errorf("KindOf(err) = %v, want KindManagedVersion", KindOf(err))
	}
	if run {
		t.Error("managed command ran despite an unsupported version pin")
	}
}

func TestUpdate_ManagedExecutable_InstallsExactPinWhenItIsLatest(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	h.setReleases(stableReleaseJSON("v1.2.3"))
	run := false
	verifiedTarget := ""
	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "v1.2.3",
		RunManaged: func(_ context.Context, executable string, args []string) error {
			run = executable == "brew" && reflect.DeepEqual(args, []string{"upgrade", "--cask", "wb"})
			return nil
		},
		VerifyManaged: func(_ context.Context, _ Detection, _ string, _ []string, expectedVersion string) (ExecutableIdentity, error) {
			verifiedTarget = expectedVersion
			return ExecutableIdentity{Path: "/tmp/wb", ResolvedPath: "/tmp/wb"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !run || verifiedTarget != "1.2.3" {
		t.Fatalf("run=%v verified target=%q", run, verifiedTarget)
	}
	if outcome.Action != ActionManagerExecuted || outcome.Target != "1.2.3" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestUpdate_ManagedExecutable_ExactPinFailsClosedWhenLatestIsUnavailable(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	run := false
	_, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "1.2.3",
		RunManaged: func(context.Context, string, []string) error {
			run = true
			return nil
		},
	})
	if KindOf(err) != KindManagedVersion || run {
		t.Fatalf("err=%v kind=%v run=%v", err, KindOf(err), run)
	}
}

func TestUpdate_ManagedExecutable_ExactPinPreservesDowngradeGuard(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/2.0.0/bin/wb", "old binary")
	h.cfg.CurrentVersion = "2.0.0"
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	h.setReleases(stableReleaseJSON("v1.2.3"))
	run := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "1.2.3",
		RunManaged: func(context.Context, string, []string) error {
			run = true
			return nil
		},
	})
	if KindOf(err) != KindDowngrade || !outcome.Downgrade || run {
		t.Fatalf("outcome=%+v err=%v kind=%v run=%v", outcome, err, KindOf(err), run)
	}
}

func TestUpdate_ManagedExecutable_CommandFailureIsTyped(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	outcome, err := h.cfg.Update(context.Background(), Options{
		Confirm: func(string) (bool, error) { return true, nil },
		RunManaged: func(context.Context, string, []string) error {
			return errors.New("brew failed")
		},
		VerifyManaged: func(context.Context, Detection, string, []string, string) (ExecutableIdentity, error) {
			return ExecutableIdentity{Path: "/tmp/wb", ResolvedPath: "/tmp/wb"}, nil
		},
	})
	if err == nil {
		t.Fatal("expected managed command failure")
	}
	if KindOf(err) != KindManagedCommand {
		t.Errorf("KindOf(err) = %v, want KindManagedCommand", KindOf(err))
	}
	if outcome.Result.Current != "1.0.0" || outcome.Result.Latest != "1.1.0" {
		t.Errorf("error Result = %+v, want current/latest availability", outcome.Result)
	}
}

func TestUpdate_ManagedExecutable_RequiresRunnerAndVerifier(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}

	_, err := h.cfg.Update(context.Background(), Options{})
	if KindOf(err) != KindManagedCommand || !strings.Contains(err.Error(), "runner") {
		t.Errorf("missing-runner error = %v, want KindManagedCommand naming runner", err)
	}

	_, err = h.cfg.Update(context.Background(), Options{
		RunManaged: func(context.Context, string, []string) error { return nil },
	})
	if KindOf(err) != KindManagedCommand || !strings.Contains(err.Error(), "probe") {
		t.Errorf("missing-verifier error = %v, want KindManagedCommand naming probe", err)
	}
}

func TestUpdate_ManagedExecutable_ConfirmationOutcomes(t *testing.T) {
	newHarness := func(t *testing.T) *updateHarness {
		h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
		h.cfg.Managers = []Manager{
			Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
		}
		h.setReleases(stableReleaseJSON("v1.1.0"))
		return h
	}
	baseOptions := func() Options {
		return Options{
			RunManaged: func(context.Context, string, []string) error { return nil },
			VerifyManaged: func(context.Context, Detection, string, []string, string) (ExecutableIdentity, error) {
				return ExecutableIdentity{Path: "/tmp/wb", ResolvedPath: "/tmp/wb"}, nil
			},
		}
	}

	t.Run("typed refusal passes through", func(t *testing.T) {
		h := newHarness(t)
		opts := baseOptions()
		opts.Confirm = func(string) (bool, error) {
			return false, &Failure{Kind: KindNonInteractive, Err: errors.New("no terminal")}
		}
		_, err := h.cfg.Update(context.Background(), opts)
		if KindOf(err) != KindNonInteractive {
			t.Errorf("KindOf(err) = %v, want KindNonInteractive", KindOf(err))
		}
	})

	t.Run("plain confirmation error is unexpected", func(t *testing.T) {
		h := newHarness(t)
		opts := baseOptions()
		opts.Confirm = func(string) (bool, error) { return false, errors.New("prompt failed") }
		_, err := h.cfg.Update(context.Background(), opts)
		if KindOf(err) != KindUnexpected {
			t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
		}
	})

	t.Run("decline aborts without running", func(t *testing.T) {
		h := newHarness(t)
		run := false
		opts := baseOptions()
		opts.Confirm = func(string) (bool, error) { return false, nil }
		opts.RunManaged = func(context.Context, string, []string) error { run = true; return nil }
		outcome, err := h.cfg.Update(context.Background(), opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.Action != ActionAborted || run {
			t.Errorf("outcome = %+v, run = %v; want aborted without execution", outcome, run)
		}
		if outcome.Result.Current != "1.0.0" || outcome.Result.Latest != "1.1.0" {
			t.Errorf("aborted Result = %+v, want current/latest availability", outcome.Result)
		}
	})
}

func TestUpdate_ManagedExecutable_ProbeFailureIsWarning(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	afterUpdateCalled := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		Confirm:    func(string) (bool, error) { return true, nil },
		RunManaged: func(context.Context, string, []string) error { return nil },
		VerifyManaged: func(context.Context, Detection, string, []string, string) (ExecutableIdentity, error) {
			return ExecutableIdentity{}, errors.New("version probe failed")
		},
		AfterUpdate: func(context.Context, AfterUpdate) error {
			afterUpdateCalled = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("probe after a successful manager command must not fail Update: %v", err)
	}
	if outcome.Action != ActionManagerExecuted || outcome.PostSwapWarning == nil {
		t.Errorf("outcome = %+v, want manager executed with warning", outcome)
	}
	if afterUpdateCalled {
		t.Error("AfterUpdate ran without a verified manager-owned executable")
	}
}

// --- Ambiguous: never resolves to manual ---

// AC: ambiguity-never-becomes-manual
func TestUpdate_Ambiguous(t *testing.T) {
	h := newUpdateHarness(t, "somewhere/random/wb", "old binary")

	outcome, err := h.cfg.Update(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected error for ambiguous install, got nil")
	}
	if KindOf(err) != KindAmbiguous {
		t.Errorf("KindOf(err) = %v, want KindAmbiguous", KindOf(err))
	}
	var f *Failure
	if !errors.As(err, &f) || f.Path != h.target {
		t.Errorf("Failure.Path = %q, want %q", f.Path, h.target)
	}
	if outcome.Detection.Method != Ambiguous {
		t.Errorf("Detection.Method = %v, want Ambiguous", outcome.Detection.Method)
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified for an ambiguous install")
	}
}

// --- Manual: already current ---

// AC: only-verified-bytes-are-installed — "already current" reports so
// having downloaded nothing.
func TestUpdate_AlreadyCurrent(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.0.0"))

	outcome, err := h.cfg.Update(context.Background(), Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionAlreadyCurrent {
		t.Fatalf("Action = %v, want ActionAlreadyCurrent", outcome.Action)
	}
	if outcome.Result.Verdict != UpToDate {
		t.Errorf("Verdict = %v, want UpToDate", outcome.Result.Verdict)
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified when already current")
	}
}

// --- Manual: unsupported platform, refused before any network call ---

func TestUpdate_UnsupportedPlatform(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.SupportedPlatforms = []Platform{{GOOS: "plan9", GOARCH: "386"}}

	_, err := h.cfg.Update(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindUnsupportedPlatform {
		t.Errorf("KindOf(err) = %v, want KindUnsupportedPlatform", KindOf(err))
	}
	if atomic.LoadInt32(&h.hits) != 0 {
		t.Errorf("HTTP requests were made (%d) before the platform check; expected none", h.hits)
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified for an unsupported platform")
	}
}

// --- Manual: release lookup failure ---

func TestUpdate_ReleaseLookupFailure(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	// No /releases file registered -> 404 -> non-200 -> error.

	_, err := h.cfg.Update(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindReleaseLookup {
		t.Errorf("KindOf(err) = %v, want KindReleaseLookup", KindOf(err))
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a release lookup failure")
	}
}

// --- Manual: non-interactive refusal ---

// AC: no-failure-leaves-a-broken-install — a Confirm implementation
// signaling REQ: non-interactive-refusal via a *Failure must have that exact
// kind reach the caller, and must leave the binary untouched.
func TestUpdate_NonInteractiveRefusal(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))

	_, err := h.cfg.Update(context.Background(), Options{
		Confirm: func(string) (bool, error) {
			return false, &Failure{Kind: KindNonInteractive, Err: errors.New("no tty")}
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", KindOf(err))
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a non-interactive refusal")
	}
}

// A plain (non-*Failure) error from Confirm is wrapped as KindUnexpected
// rather than silently discarded or panicking.
func TestUpdate_ConfirmPlainErrorIsUnexpected(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))

	_, err := h.cfg.Update(context.Background(), Options{
		Confirm: func(string) (bool, error) { return false, errors.New("boom") },
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
}

// --- Manual: confirmation declined (not a failure) ---

func TestUpdate_ConfirmDeclines(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))

	outcome, err := h.cfg.Update(context.Background(), Options{
		Confirm: func(string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatalf("a declined confirmation must not be an error, got: %v", err)
	}
	if outcome.Action != ActionAborted {
		t.Fatalf("Action = %v, want ActionAborted", outcome.Action)
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a declined confirmation")
	}
}

// --- Manual: dry run ---

// AC: reference-cli-proves-it-end-to-end / REQ: dry-run — walks detection,
// target resolution, and the asset URL, then stops before any request to
// the download endpoints and without calling Confirm.
func TestUpdate_DryRun(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))

	confirmCalled := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		DryRun:  true,
		Confirm: func(string) (bool, error) { confirmCalled = true; return true, nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionPlanned {
		t.Fatalf("Action = %v, want ActionPlanned", outcome.Action)
	}
	if outcome.Target != "1.1.0" {
		t.Errorf("Target = %q, want 1.1.0", outcome.Target)
	}
	wantAsset := defaultAssetName("wb", "1.1.0", goosName, goarchName)
	wantURL := h.server.URL + "/dl/v1.1.0/" + wantAsset
	if outcome.PlannedURL != wantURL {
		t.Errorf("PlannedURL = %q, want %q", outcome.PlannedURL, wantURL)
	}
	if confirmCalled {
		t.Error("Confirm was called during a dry run; it must not be")
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified during a dry run")
	}
	// Only the /releases request should have happened — never a download
	// endpoint.
	if atomic.LoadInt32(&h.hits) != 1 {
		t.Errorf("HTTP requests = %d, want exactly 1 (the releases lookup)", h.hits)
	}
}

// A dry run on a pin still evaluates the downgrade guard and reports the
// refusal — it does not silently plan an operation that would actually be
// refused.
func TestUpdate_DryRun_PinnedDowngradeStillRefuses(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.CurrentVersion = "0.5.0"
	h.setReleases(stableReleaseJSON("v0.3.0"))

	_, err := h.cfg.Update(context.Background(), Options{DryRun: true, PinnedVersion: "0.3.0"})
	if err == nil {
		t.Fatal("expected downgrade refusal even during a dry run")
	}
	if KindOf(err) != KindDowngrade {
		t.Errorf("KindOf(err) = %v, want KindDowngrade", KindOf(err))
	}
}

// --- Manual: pinned downgrade guard, both directions ---

// AC: pins-resolve-exactly-and-guard-direction
func TestUpdate_PinnedDowngrade_RefusedWithoutFlag(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.CurrentVersion = "0.5.0"
	h.setReleases(stableReleaseJSON("v0.3.0"))

	outcome, err := h.cfg.Update(context.Background(), Options{PinnedVersion: "0.3.0"})
	if err == nil {
		t.Fatal("expected downgrade refusal, got nil")
	}
	if KindOf(err) != KindDowngrade {
		t.Errorf("KindOf(err) = %v, want KindDowngrade", KindOf(err))
	}
	if !strings.Contains(err.Error(), "0.5.0") || !strings.Contains(err.Error(), "0.3.0") {
		t.Errorf("error %q does not name both versions", err.Error())
	}
	if !outcome.Downgrade {
		t.Error("Outcome.Downgrade = false, want true")
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a refused downgrade")
	}
}

func TestUpdate_PinnedDowngrade_ProceedsWithFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.CurrentVersion = "0.5.0"
	h.setReleases(stableReleaseJSON("v0.3.0"))
	h.addAsset("v0.3.0", "0.3.0", "#!/bin/sh\necho \"wb version 0.3.0\"\n")

	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion:  "0.3.0",
		AllowDowngrade: true,
		Confirm:        func(string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionUpdated {
		t.Fatalf("Action = %v, want ActionUpdated", outcome.Action)
	}
	if !outcome.Downgrade {
		t.Error("Outcome.Downgrade = false, want true")
	}
	if outcome.Target != "0.3.0" {
		t.Errorf("Target = %q, want 0.3.0", outcome.Target)
	}
	if outcome.PostSwapWarning != nil {
		t.Errorf("PostSwapWarning = %v, want nil", outcome.PostSwapWarning)
	}
}

// An undetermined running version disables the downgrade guard rather than
// guessing (REQ: pinned-downgrade-guard).
func TestUpdate_UndeterminedVersionDisablesDowngradeGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.CurrentVersion = "dev" // default undetermined placeholder
	h.setReleases(stableReleaseJSON("v0.3.0"))
	h.addAsset("v0.3.0", "0.3.0", "#!/bin/sh\necho \"wb version 0.3.0\"\n")

	confirmed := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "0.3.0", // would be a "downgrade" from any real version
		Confirm:       func(string) (bool, error) { confirmed = true; return true, nil },
	})
	if err != nil {
		t.Fatalf("guard must not trigger for an undetermined current version: %v", err)
	}
	if !confirmed {
		t.Error("Confirm was not called; the guard must not have short-circuited before it")
	}
	if outcome.Action != ActionUpdated {
		t.Fatalf("Action = %v, want ActionUpdated", outcome.Action)
	}
	if outcome.Downgrade {
		t.Error("Outcome.Downgrade = true, want false: direction cannot be established for an undetermined version")
	}
}

// --- Manual: pinned exact tag, v-prefix-agnostic, from its OWN URL ---

// AC: pins-resolve-exactly-and-guard-direction — a pin must fetch that
// release's own asset URL, not whatever the "latest" happens to be.
func TestUpdate_PinnedResolvesExactTagAndOwnAssetURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.CurrentVersion = "1.0.0"
	// A newer "latest" exists too, as a trap: the pinned older release must
	// be fetched from ITS OWN url, not whatever "latest" resolves to.
	h.setReleases(stableReleaseJSON("v5.0.0", "v1.2.0"))
	h.addAsset("v1.2.0", "1.2.0", "#!/bin/sh\necho \"wb version 1.2.0\"\n")
	// Deliberately do NOT register v5.0.0's asset: if the pinned path ever
	// consulted "latest" for its target, the download would 404.

	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "v1.2.0",
		Confirm:       func(string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Target != "1.2.0" {
		t.Errorf("Target = %q, want 1.2.0 (not the sentinel latest 5.0.0)", outcome.Target)
	}
	if got := h.targetBytes(); !strings.Contains(got, "1.2.0") {
		t.Errorf("target file content = %q, does not look like the v1.2.0 script", got)
	}
}

// --- Manual: pinned unknown tag ---

// AC: pins-resolve-exactly-and-guard-direction
func TestUpdate_PinnedUnknownTag(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.0.0"))

	_, err := h.cfg.Update(context.Background(), Options{PinnedVersion: "v9.9.9"})
	if err == nil {
		t.Fatal("expected error for an unpublished pin, got nil")
	}
	if KindOf(err) != KindUnknownTag {
		t.Errorf("KindOf(err) = %v, want KindUnknownTag", KindOf(err))
	}
	if !strings.Contains(err.Error(), "9.9.9") {
		t.Errorf("error %q does not name the requested tag", err.Error())
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified for an unknown pinned tag")
	}
}

// --- Manual: download failure (non-404) ---

func TestUpdate_DownloadServerError(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))
	// Registers the release but not any asset/checksums files under a path
	// that would 200; instead point at a handler that always 500s by
	// overriding the server entirely for this one test.
	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases" {
			_, _ = w.Write([]byte(stableReleaseJSON("v1.1.0")))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(badServer.Close)
	h.cfg.ReleasesAPIURL = badServer.URL + "/releases"
	h.cfg.DownloadURL = func(_, tag, asset string) string { return badServer.URL + "/dl/" + tag + "/" + asset }
	h.cfg.HTTPClient = badServer.Client()

	_, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindDownload {
		t.Errorf("KindOf(err) = %v, want KindDownload", KindOf(err))
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a download server error")
	}
}

// --- Manual: checksum mismatch aborts before any write ---

// AC: only-verified-bytes-are-installed / no-failure-leaves-a-broken-install
func TestUpdate_ChecksumMismatchAbortsBeforeWrite(t *testing.T) {
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))
	h.addAssetWrongChecksum("v1.1.0", "1.1.0")

	_, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err == nil {
		t.Fatal("expected checksum error, got nil")
	}
	if KindOf(err) != KindChecksum {
		t.Errorf("KindOf(err) = %v, want KindChecksum", KindOf(err))
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a checksum mismatch")
	}
}

// --- Manual: staging/permission failures leave the target untouched ---

// AC: no-failure-leaves-a-broken-install / REQ: permission-failure-identifiable
func TestUpdate_PermissionFailureIsIdentifiable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))
	h.addAsset("v1.1.0", "1.1.0", "#!/bin/sh\necho \"wb version 1.1.0\"\n")

	origRename := renameFunc
	t.Cleanup(func() { renameFunc = origRename })
	renameFunc = func(string, string) error { return fmt.Errorf("rename: %w", fs.ErrPermission) }

	_, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindPermission {
		t.Errorf("KindOf(err) = %v, want KindPermission", KindOf(err))
	}
	var f *Failure
	if !errors.As(err, &f) || f.Path != h.target {
		t.Errorf("Failure.Path = %q, want %q", f.Path, h.target)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Error("errors.Is(err, fs.ErrPermission) = false, want true")
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a permission failure")
	}
}

// A non-permission staging failure is KindUnexpected, distinguishable from
// KindPermission.
func TestUpdate_NonPermissionStagingFailureIsUnexpected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))
	h.addAsset("v1.1.0", "1.1.0", "#!/bin/sh\necho \"wb version 1.1.0\"\n")

	origRename := renameFunc
	t.Cleanup(func() { renameFunc = origRename })
	renameFunc = func(string, string) error { return errors.New("disk full") }

	_, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified after a staging failure")
	}
}

// A failure resolving the running executable's own path is KindUnexpected.
func TestUpdate_DetectSelfErrorIsUnexpected(t *testing.T) {
	origExe := osExecutable
	t.Cleanup(func() { osExecutable = origExe })
	osExecutable = func() (string, error) { return "", errors.New("no exe") }

	cfg := Config{BinaryName: "wb", Repository: "acme/wb"}
	_, err := cfg.Update(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
}

// --- Manual: full successful swap, with and without a post-swap warning ---

// AC: only-verified-bytes-are-installed
func TestUpdate_SuccessfulSwapConfirmsVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))
	h.addAsset("v1.1.0", "1.1.0", "#!/bin/sh\necho \"wb version 1.1.0\"\n")

	var gotTransition string
	outcome, err := h.cfg.Update(context.Background(), Options{
		Confirm: func(transition string) (bool, error) { gotTransition = transition; return true, nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionUpdated {
		t.Fatalf("Action = %v, want ActionUpdated", outcome.Action)
	}
	if outcome.PostSwapWarning != nil {
		t.Errorf("PostSwapWarning = %v, want nil", outcome.PostSwapWarning)
	}
	if gotTransition != "1.0.0 → 1.1.0" {
		t.Errorf("transition = %q, want %q", gotTransition, "1.0.0 → 1.1.0")
	}
	got := h.targetBytes()
	if !strings.Contains(got, "1.1.0") {
		t.Errorf("target content = %q, does not look like the swapped-in script", got)
	}
	info, err := os.Stat(h.target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("swapped binary is not executable: mode %v", info.Mode())
	}
}

// The swap already succeeded when the post-swap probe fails; Update must
// still report success with the mismatch surfaced as a warning
// (REQ: post-swap-version-check).
func TestUpdate_PostSwapVersionMismatchIsWarningNotFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.setReleases(stableReleaseJSON("v1.1.0"))
	// The swapped-in script reports the WRONG version.
	h.addAsset("v1.1.0", "1.1.0", "#!/bin/sh\necho \"wb version 0.0.1\"\n")

	outcome, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatalf("a failed post-swap probe must not fail Update: %v", err)
	}
	if outcome.Action != ActionUpdated {
		t.Fatalf("Action = %v, want ActionUpdated", outcome.Action)
	}
	if outcome.PostSwapWarning == nil {
		t.Fatal("PostSwapWarning = nil, want a non-nil warning for a version mismatch")
	}
	// The swap DID happen — the file was in fact replaced.
	if got := h.targetBytes(); !strings.Contains(got, "0.0.1") {
		t.Errorf("target content = %q; the swap should have proceeded despite the probe mismatch", got)
	}
}

// --- TagPrefix: multi-product repository (the synchestra-releases case) ---

// The real case driving TagPrefix: one repository publishes multiple
// products, e.g. tag "servers-v0.26.1" holding asset
// "synchestra-channel_0.26.1_<os>_<arch>.tar.gz" — the download URL's tag
// segment is the FULL prefixed tag (that is the actual release path on
// GitHub) while the asset filename carries only the bare version. Getting
// this pair backwards is the single most likely bug, so both directions are
// asserted explicitly, and a decoy release for another product proves the
// swap never touched it.
func TestUpdate_TagPrefixFullSwapUsesFullTagAndBareVersionAsset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/synchestra-channel", "old binary")
	h.cfg.BinaryName = "synchestra-channel"
	h.cfg.TagPrefix = "servers-"
	h.cfg.CurrentVersion = "0.26.0"
	h.setReleases(stableReleaseJSON("cli-v0.15.1", "servers-v0.26.1"))
	h.addAsset("servers-v0.26.1", "0.26.1", "#!/bin/sh\necho \"synchestra-channel version 0.26.1\"\n")
	// Deliberately do NOT register any cli- asset: if the servers- binary
	// ever resolved the cli- tag/version pairing instead of its own, this
	// download would 404.

	var requestedURLs []string
	origDownloadURL := h.cfg.DownloadURL
	h.cfg.DownloadURL = func(repo, tag, asset string) string {
		u := origDownloadURL(repo, tag, asset)
		requestedURLs = append(requestedURLs, u)
		return u
	}

	outcome, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionUpdated {
		t.Fatalf("Action = %v, want ActionUpdated", outcome.Action)
	}
	if outcome.Target != "0.26.1" {
		t.Errorf("Target = %q, want %q (the bare version, from the prefixed tag)", outcome.Target, "0.26.1")
	}
	if got := h.targetBytes(); !strings.Contains(got, "0.26.1") {
		t.Errorf("target content = %q, does not look like the servers- product's own script", got)
	}
	if len(requestedURLs) == 0 {
		t.Fatal("no download URLs were requested")
	}
	assetURL := requestedURLs[0]
	if !strings.Contains(assetURL, "/servers-v0.26.1/") {
		t.Errorf("asset URL %q does not use the full prefixed tag %q as its release path", assetURL, "servers-v0.26.1")
	}
	if !strings.HasSuffix(assetURL, "synchestra-channel_0.26.1_"+goosName+"_"+goarchName+".tar.gz") {
		t.Errorf("asset URL %q does not name the asset with the bare version 0.26.1 (not the tag prefix)", assetURL)
	}
	if strings.Contains(assetURL[strings.LastIndex(assetURL, "/")+1:], "servers-") {
		t.Errorf("asset URL %q: the asset FILENAME must never carry the tag prefix, only the release path does", assetURL)
	}
}

// A pin on the multi-product repository must also resolve to this product's
// own tag/asset pairing, never the other product's, even when both publish
// the exact same bare version.
func TestUpdate_TagPrefixPinnedResolvesOwnProductAssetOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/synchestra-channel", "old binary")
	h.cfg.BinaryName = "synchestra-channel"
	h.cfg.TagPrefix = "servers-"
	h.cfg.CurrentVersion = "0.10.0"
	h.setReleases(stableReleaseJSON("cli-v0.15.1", "servers-v0.15.1"))
	h.addAsset("servers-v0.15.1", "0.15.1", "#!/bin/sh\necho \"synchestra-channel version 0.15.1\"\n")
	// A decoy: same bare version, wrong product. If a pin of "0.15.1" ever
	// matched this instead, the swap would install the wrong binary.
	h.addAsset("cli-v0.15.1", "0.15.1", "#!/bin/sh\necho \"WRONG PRODUCT\"\n")

	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "0.15.1",
		Confirm:       func(string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Target != "0.15.1" {
		t.Errorf("Target = %q, want 0.15.1", outcome.Target)
	}
	got := h.targetBytes()
	if strings.Contains(got, "WRONG PRODUCT") {
		t.Fatal("the pin resolved to the OTHER product's release")
	}
	if !strings.Contains(got, "0.15.1") {
		t.Errorf("target content = %q, does not look like the servers- product's own script", got)
	}
}

// Dry run must report the exact URL a real run would fetch — verified here
// by comparing DryRun's PlannedURL against the asset URL an actual run
// requests for the identical multi-product scenario.
func TestUpdate_DryRun_URLMatchesRealRunWithTagPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/synchestra-channel", "old binary")
	h.cfg.BinaryName = "synchestra-channel"
	h.cfg.TagPrefix = "servers-"
	h.cfg.CurrentVersion = "0.26.0"
	h.setReleases(stableReleaseJSON("cli-v0.15.1", "servers-v0.26.1"))
	h.addAsset("servers-v0.26.1", "0.26.1", "#!/bin/sh\necho \"synchestra-channel version 0.26.1\"\n")

	dryOutcome, err := h.cfg.Update(context.Background(), Options{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}
	if dryOutcome.PlannedURL == "" {
		t.Fatal("PlannedURL is empty")
	}

	var gotAssetURL string
	origDownloadURL := h.cfg.DownloadURL
	first := true
	h.cfg.DownloadURL = func(repo, tag, asset string) string {
		u := origDownloadURL(repo, tag, asset)
		if first {
			gotAssetURL = u // the asset request is always the first DownloadURL call
			first = false
		}
		return u
	}

	if _, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }}); err != nil {
		t.Fatalf("unexpected error on real run: %v", err)
	}
	if gotAssetURL != dryOutcome.PlannedURL {
		t.Errorf("real run fetched %q, dry run planned %q — must match", gotAssetURL, dryOutcome.PlannedURL)
	}
}

// --- Configured undetermined placeholder feeds through Update's Result ---

func TestUpdate_ConfiguredUndeterminedPlaceholder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}
	h := newUpdateHarness(t, "bin/wb", "old binary")
	h.cfg.CurrentVersion = "unknown"
	h.cfg.UndeterminedVersions = []string{"unknown"}
	h.setReleases(stableReleaseJSON("v1.1.0"))
	h.addAsset("v1.1.0", "1.1.0", "#!/bin/sh\necho \"wb version 1.1.0\"\n")

	outcome, err := h.cfg.Update(context.Background(), Options{Confirm: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Result.Verdict != Undetermined {
		t.Errorf("Verdict = %v, want Undetermined", outcome.Result.Verdict)
	}
	if outcome.Result.Current != "unknown" {
		t.Errorf("Current = %q, want the placeholder reported as-is", outcome.Result.Current)
	}
}

func TestUpdate_AfterUpdateRunsOnlyForSuccessfulManualOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary not portable to windows")
	}

	t.Run("updated", func(t *testing.T) {
		h := newUpdateHarness(t, "bin/wb", "old binary")
		h.setReleases(stableReleaseJSON("v1.1.0"))
		h.addAsset("v1.1.0", "1.1.0", "#!/bin/sh\necho \"wb version 1.1.0\"\n")
		var got AfterUpdate
		outcome, err := h.cfg.Update(context.Background(), Options{
			Confirm: func(string) (bool, error) { return true, nil },
			AfterUpdate: func(_ context.Context, update AfterUpdate) error {
				got = update
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if outcome.Action != ActionUpdated || got.Outcome.Action != ActionUpdated {
			t.Fatalf("outcomes = %s / %s, want updated", outcome.Action, got.Outcome.Action)
		}
		if got.Executable.Path != h.target || got.Executable.ResolvedPath != h.target {
			t.Errorf("executable = %+v, want both paths %q", got.Executable, h.target)
		}
	})

	t.Run("already current", func(t *testing.T) {
		h := newUpdateHarness(t, "bin/wb", "old binary")
		h.setReleases(stableReleaseJSON("v1.0.0"))
		called := false
		outcome, err := h.cfg.Update(context.Background(), Options{AfterUpdate: func(_ context.Context, update AfterUpdate) error {
			called = update.Outcome.Action == ActionAlreadyCurrent
			return nil
		}})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if outcome.Action != ActionAlreadyCurrent || !called {
			t.Errorf("action = %s, callback called = %v; want already current callback", outcome.Action, called)
		}
	})

	t.Run("already current dry run", func(t *testing.T) {
		h := newUpdateHarness(t, "bin/wb", "old binary")
		h.setReleases(stableReleaseJSON("v1.0.0"))
		called := false
		outcome, err := h.cfg.Update(context.Background(), Options{
			DryRun: true,
			AfterUpdate: func(context.Context, AfterUpdate) error {
				called = true
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if outcome.Action != ActionAlreadyCurrent {
			t.Errorf("Action = %s, want already_current", outcome.Action)
		}
		if called {
			t.Error("AfterUpdate ran for an already-current dry run")
		}
	})

	for _, tc := range []struct {
		name       string
		wantAction Action
		wantErr    bool
		run        func(t *testing.T, h *updateHarness, opts Options) (Outcome, error)
	}{
		{
			name:       "dry run",
			wantAction: ActionPlanned,
			run: func(_ *testing.T, h *updateHarness, opts Options) (Outcome, error) {
				h.setReleases(stableReleaseJSON("v1.1.0"))
				return h.cfg.Update(context.Background(), opts)
			},
		},
		{
			name:       "declined",
			wantAction: ActionAborted,
			run: func(_ *testing.T, h *updateHarness, opts Options) (Outcome, error) {
				h.setReleases(stableReleaseJSON("v1.1.0"))
				opts.Confirm = func(string) (bool, error) { return false, nil }
				return h.cfg.Update(context.Background(), opts)
			},
		},
		{
			name:       "redirected",
			wantAction: ActionRedirected,
			run: func(_ *testing.T, h *updateHarness, opts Options) (Outcome, error) {
				h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}
				osExecutable = func() (string, error) {
					return filepath.Join(h.dir, "Cellar", "wb", "1.0.0", "bin", "wb"), nil
				}
				return h.cfg.Update(context.Background(), opts)
			},
		},
		{
			name:    "failure",
			wantErr: true,
			run: func(_ *testing.T, h *updateHarness, opts Options) (Outcome, error) {
				h.target = filepath.Join(h.dir, "unknown", "wb")
				osExecutable = func() (string, error) { return h.target, nil }
				return h.cfg.Update(context.Background(), opts)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "bin/wb", "old binary")
			calls := 0
			opts := Options{DryRun: tc.name == "dry run", AfterUpdate: func(context.Context, AfterUpdate) error {
				calls++
				return nil
			}}
			outcome, err := tc.run(t, h, opts)
			if tc.wantErr {
				if err == nil {
					t.Fatal("Update() error = nil, want failure")
				}
			} else if err != nil {
				t.Fatalf("Update() error = %v", err)
			} else if outcome.Action != tc.wantAction {
				t.Errorf("Action = %s, want %s", outcome.Action, tc.wantAction)
			}
			if calls != 0 {
				t.Errorf("AfterUpdate calls = %d, want 0 for %s", calls, tc.name)
			}
		})
	}
}

func TestUpdate_AfterUpdateManagerReusesVerifiedExecutableAndKeepsCancellationNonfatal(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{
		Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
	}
	newPath := filepath.Join(h.dir, "bin", "wb")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got AfterUpdate
	outcome, err := h.cfg.Update(ctx, Options{
		RunManaged: func(context.Context, string, []string) error {
			cancel()
			return nil
		},
		VerifyManaged: func(context.Context, Detection, string, []string, string) (ExecutableIdentity, error) {
			return ExecutableIdentity{Path: newPath, ResolvedPath: filepath.Join(h.dir, "Cellar", "wb", "1.1.0", "bin", "wb")}, nil
		},
		AfterUpdate: func(gotCtx context.Context, update AfterUpdate) error {
			got = update
			return gotCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("successful manager update must remain successful after callback cancellation: %v", err)
	}
	if outcome.Action != ActionManagerExecuted {
		t.Fatalf("Action = %s, want manager_executed", outcome.Action)
	}
	if got.Executable.Path != newPath || got.Executable.ResolvedPath != filepath.Join(h.dir, "Cellar", "wb", "1.1.0", "bin", "wb") {
		t.Errorf("callback executable = %+v, want new manager paths", got.Executable)
	}
	if !errors.Is(outcome.AfterUpdateWarning, context.Canceled) {
		t.Errorf("AfterUpdateWarning = %v, want context canceled", outcome.AfterUpdateWarning)
	}
}

func TestInstalledExecutableIdentityResolution(t *testing.T) {
	cfg := Config{BinaryName: "wb"}
	origEval, origAbs := evalSymlinksFunc, absPath
	t.Cleanup(func() { evalSymlinksFunc, absPath = origEval, origAbs })

	t.Run("manager identity must already be verified", func(t *testing.T) {
		_, err := cfg.installedExecutable(&Outcome{Action: ActionManagerExecuted}, nil)
		if err == nil || !strings.Contains(err.Error(), "was not verified") {
			t.Errorf("installedExecutable() error = %v, want missing verification failure", err)
		}
	})

	t.Run("relative manual path resolves installed identity", func(t *testing.T) {
		evalSymlinksFunc = func(path string) (string, error) { return path + ".installed", nil }
		absPath = func(path string) (string, error) {
			if path != "bin/wb" {
				t.Fatalf("Abs path = %q, want bin/wb", path)
			}
			return "/tmp/bin/wb", nil
		}
		identity, err := cfg.installedExecutable(&Outcome{Action: ActionUpdated, Detection: Detection{Path: "bin/wb"}}, nil)
		if err != nil {
			t.Fatalf("installedExecutable() error = %v", err)
		}
		if identity.Path != "/tmp/bin/wb" || identity.ResolvedPath != "/tmp/bin/wb.installed" {
			t.Errorf("identity = %+v, want absolute invocation and resolved installed path", identity)
		}
	})

	t.Run("unresolvable executable skips callback with nonfatal warning", func(t *testing.T) {
		called := false
		outcome := Outcome{Action: ActionManagerExecuted, Detection: Detection{Path: "/tmp/old/wb"}}
		cfg.runAfterUpdate(context.Background(), Options{AfterUpdate: func(context.Context, AfterUpdate) error {
			called = true
			return nil
		}}, &outcome, nil)
		if called || outcome.AfterUpdateWarning == nil || !strings.Contains(outcome.AfterUpdateWarning.Error(), "was not verified") {
			t.Fatalf("callback called = %v, warning = %v; want skipped callback and missing verification warning", called, outcome.AfterUpdateWarning)
		}
		if outcome.Action != ActionManagerExecuted {
			t.Fatalf("completed update action changed to %s", outcome.Action)
		}
	})

	t.Run("absolute path resolution failure", func(t *testing.T) {
		absPath = func(string) (string, error) { return "", errors.New("cannot make absolute") }
		_, err := cfg.installedExecutable(&Outcome{Action: ActionUpdated, Detection: Detection{Path: "bin/wb"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "cannot make absolute") {
			t.Errorf("installedExecutable() error = %v, want absolute-path failure", err)
		}
		absPath = origAbs
	})

	t.Run("resolved executable failure", func(t *testing.T) {
		missing := errors.New("missing installed target")
		evalSymlinksFunc = func(string) (string, error) { return "", missing }
		_, err := cfg.installedExecutable(&Outcome{Action: ActionUpdated, Detection: Detection{Path: "/tmp/bin/wb"}}, nil)
		if !errors.Is(err, missing) || !strings.Contains(err.Error(), "resolve installed executable") {
			t.Errorf("installedExecutable() error = %v, want resolved target failure", err)
		}
	})
}

func TestRunAfterUpdateResolutionFailureIsNonfatalAndSkippedWhenIneligible(t *testing.T) {
	cfg := Config{}
	origAbs := absPath
	absPath = func(string) (string, error) { return "", errors.New("cannot make absolute") }
	t.Cleanup(func() { absPath = origAbs })

	called := false
	outcome := Outcome{Action: ActionUpdated, Detection: Detection{Path: "bin/wb"}}
	cfg.runAfterUpdate(context.Background(), Options{AfterUpdate: func(context.Context, AfterUpdate) error {
		called = true
		return nil
	}}, &outcome, nil)
	if called {
		t.Error("AfterUpdate callback ran despite executable-resolution failure")
	}
	if outcome.AfterUpdateWarning == nil || !strings.Contains(outcome.AfterUpdateWarning.Error(), "cannot make absolute") {
		t.Errorf("AfterUpdateWarning = %v, want executable-resolution failure", outcome.AfterUpdateWarning)
	}

	called = false
	ineligible := Outcome{Action: ActionPlanned}
	cfg.runAfterUpdate(context.Background(), Options{AfterUpdate: func(context.Context, AfterUpdate) error {
		called = true
		return nil
	}}, &ineligible, nil)
	if called || ineligible.AfterUpdateWarning != nil {
		t.Errorf("planned outcome callback state = called %v, warning %v; want no callback or warning", called, ineligible.AfterUpdateWarning)
	}
}
