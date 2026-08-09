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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

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

// AC: managed-installs-are-redirected — a version pin and a Confirm that
// would say yes must both be ignored: the managed branch returns before
// either is ever inspected.
func TestUpdate_ManagedRedirect_IgnoresPinAndConfirm(t *testing.T) {
	h := newUpdateHarness(t, "Cellar/wb/1.0.0/bin/wb", "old binary")
	h.cfg.Managers = []Manager{Homebrew("brew upgrade --cask wb")}

	confirmCalled := false
	outcome, err := h.cfg.Update(context.Background(), Options{
		PinnedVersion: "9.9.9",
		Confirm:       func(string) (bool, error) { confirmCalled = true; return true, nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Action != ActionRedirected {
		t.Fatalf("Action = %v, want ActionRedirected", outcome.Action)
	}
	if outcome.Detection.Manager == nil || outcome.Detection.Manager.Name != "Homebrew" {
		t.Fatalf("Detection.Manager = %v, want Homebrew", outcome.Detection.Manager)
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
