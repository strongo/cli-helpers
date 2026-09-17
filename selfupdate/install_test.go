package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- hardLinkNoReplace ---

func TestHardLinkNoReplace_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(src, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := hardLinkNoReplace(src, dest); err != nil {
		t.Fatalf("hardLinkNoReplace: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("dest content = %q, want %q", got, "payload")
	}
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("src should have been unlinked after a successful link, stat err = %v", err)
	}
}

func TestHardLinkNoReplace_DestinationExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := hardLinkNoReplace(src, dest)
	if err == nil {
		t.Fatal("expected an error when dest already exists, got nil")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("errors.Is(err, fs.ErrExist) = false, want true; err = %v", err)
	}

	// Never overwritten (REQ: install-never-overwrites), and never unlinked
	// on a failed link, since removing src on this path would just discard
	// the staged bytes without ever having placed them anywhere.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("dest content = %q, want unchanged %q", got, "original")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("src should still exist after a failed link: %v", err)
	}
}

func TestHardLinkNoReplace_SourceMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "does-not-exist")
	dest := filepath.Join(dir, "dest")

	err := hardLinkNoReplace(src, dest)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, fs.ErrExist) {
		t.Errorf("a missing source must not be reported as fs.ErrExist: %v", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("dest must not be created when src is missing, stat err = %v", statErr)
	}
}

// --- placeNoReplace ---

func TestPlaceNoReplace_Success(t *testing.T) {
	// src mirrors InstallNew's tmpPath: an input living outside dest's own
	// directory (InstallNew extracts into os.TempDir() and cleans it up
	// itself via defer); placeNoReplace only ever writes inside dir.
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "downloaded")
	dest := filepath.Join(dir, "wb")
	if err := os.WriteFile(src, []byte("binary bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := placeNoReplace("wb", dest, src); err != nil {
		t.Fatalf("placeNoReplace: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary bytes" {
		t.Errorf("dest content = %q", got)
	}
	assertOnlyEntries(t, dir, "wb")
}

func TestPlaceNoReplace_DestinationExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "downloaded")
	dest := filepath.Join(dir, "wb")
	if err := os.WriteFile(src, []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := placeNoReplace("wb", dest, src)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindDestinationExists {
		t.Errorf("KindOf(err) = %v, want KindDestinationExists", KindOf(err))
	}
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("error is not *Failure: %v", err)
	}
	if f.Path != dest {
		t.Errorf("Failure.Path = %q, want %q", f.Path, dest)
	}

	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "already here" {
		t.Fatalf("dest content = %q, want unchanged %q", got, "already here")
	}
	// No staging leftovers: only the pre-existing dest file remains.
	assertOnlyEntries(t, dir, "wb")
}

func TestPlaceNoReplace_MissingDestDir(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "downloaded")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "no-such-subdir", "wb")

	err := placeNoReplace("wb", dest, src)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	// A missing destination directory is not a permission failure, and this
	// primitive never creates it (that policy belongs to cliinstall).
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
	var f *Failure
	if errors.As(err, &f) && f.Path != dest {
		t.Errorf("Failure.Path = %q, want %q", f.Path, dest)
	}
}

func TestPlaceNoReplace_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	src := filepath.Join(root, "downloaded")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	readOnlyDir := filepath.Join(root, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o755) })
	dest := filepath.Join(readOnlyDir, "wb")

	err := placeNoReplace("wb", dest, src)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindPermission {
		t.Errorf("KindOf(err) = %v, want KindPermission", KindOf(err))
	}
}

// The link step (as opposed to the earlier stage step, which has its own
// real-filesystem test above) mapping a permission error to KindPermission.
func TestPlaceNoReplace_LinkPermissionDenied(t *testing.T) {
	orig := osLinkFunc
	t.Cleanup(func() { osLinkFunc = orig })
	osLinkFunc = func(string, string) error { return fmt.Errorf("link: %w", fs.ErrPermission) }

	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "downloaded")
	dest := filepath.Join(dir, "wb")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := placeNoReplace("wb", dest, src)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindPermission {
		t.Errorf("KindOf(err) = %v, want KindPermission", KindOf(err))
	}
	assertOnlyEntries(t, dir)
}

// Any other link failure (e.g. a destination filesystem that does not
// support hard links) falls into KindUnexpected rather than being
// misreported as a permission or existence failure.
func TestPlaceNoReplace_LinkOtherError(t *testing.T) {
	orig := osLinkFunc
	t.Cleanup(func() { osLinkFunc = orig })
	osLinkFunc = func(string, string) error { return errors.New("operation not supported") }

	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "downloaded")
	dest := filepath.Join(dir, "wb")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := placeNoReplace("wb", dest, src)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
	assertOnlyEntries(t, dir)
}

func assertOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("entries in %s = %v, want exactly %v (no leftover staging files)", dir, got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("entries in %s = %v, missing expected %q", dir, got, w)
		}
	}
}

// --- InstallNew ---

// newInstallServer serves both a releases listing at /releases and, for
// each entry of files, the exact path it's keyed by — combining
// version_test.go's release-listing shape with download_test.go's per-tag
// asset shape so InstallNew's tests can exercise the whole resolve-download-
// verify-place path against one fake server, entirely offline
// (REQ: no-network-in-tests).
func newInstallServer(t *testing.T, releasesBody string, files map[string][]byte) *httptest.Server {
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

func installTestConfig(srv *httptest.Server) Config {
	return Config{
		BinaryName:     "wb",
		Repository:     "acme/wb",
		ReleasesAPIURL: srv.URL + "/releases",
		DownloadURL:    perTagDownloadURL(srv.URL),
		HTTPClient:     srv.Client(),
	}
}

func TestInstallNew_Success(t *testing.T) {
	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	version := "1.2.3"
	tag := "v1.2.3"
	binContent := []byte("the installed binary")
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", binContent)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)

	srv := newInstallServer(t, `[{"tag_name":"v1.2.3","prerelease":false,"draft":false}]`, map[string][]byte{
		"/" + tag + "/" + asset:                               archive,
		"/" + tag + "/" + defaultChecksumsName("wb", version): []byte(checksums),
	})

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "wb")

	cfg := installTestConfig(srv)
	plan, err := cfg.PlanInstall(context.Background())
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if plan.Tag != tag || plan.Version != version {
		t.Errorf("plan = %+v, want tag %q version %q", plan, tag, version)
	}
	if want := installTestConfig(srv).DownloadURL("acme/wb", tag, asset); plan.AssetURL != want {
		t.Errorf("plan.AssetURL = %q, want %q", plan.AssetURL, want)
	}
	result, err := cfg.InstallNew(context.Background(), dest, plan)
	if err != nil {
		t.Fatalf("InstallNew: %v", err)
	}
	if result.Path != dest {
		t.Errorf("result.Path = %q, want %q", result.Path, dest)
	}
	if result.Version != version {
		t.Errorf("result.Version = %q, want %q", result.Version, version)
	}
	if result.Tag != tag {
		t.Errorf("result.Tag = %q, want %q", result.Tag, tag)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binContent) {
		t.Errorf("installed content = %q, want %q", got, binContent)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(dest)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("installed binary is not executable: mode %v", info.Mode())
		}
	}
	assertOnlyEntries(t, destDir, "wb")
}

// REQ: multi-product-repository — a TagPrefix'd repository resolves and
// installs the right product, and InstallResult reports the bare version
// alongside the full published tag.
func TestInstallNew_TagPrefix(t *testing.T) {
	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	version := "0.15.1"
	tag := "cli-v0.15.1"
	binContent := []byte("prefixed product binary")
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", binContent)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)

	releases := `[
		{"tag_name": "servers-v9.9.9", "prerelease": false, "draft": false},
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": false}
	]`
	srv := newInstallServer(t, releases, map[string][]byte{
		"/" + tag + "/" + asset:                               archive,
		"/" + tag + "/" + defaultChecksumsName("wb", version): []byte(checksums),
	})

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "wb")

	cfg := installTestConfig(srv)
	cfg.TagPrefix = "cli-"
	plan, err := cfg.PlanInstall(context.Background())
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	result, err := cfg.InstallNew(context.Background(), dest, plan)
	if err != nil {
		t.Fatalf("InstallNew: %v", err)
	}
	if result.Version != version {
		t.Errorf("result.Version = %q, want %q", result.Version, version)
	}
	if result.Tag != tag {
		t.Errorf("result.Tag = %q, want %q", result.Tag, tag)
	}
}

// A destination that already exists is only discovered at placement time,
// after a full verified download — matching
// AC: direct-install-writes-only-verified-new-files, which requires a
// racing file created between status and placement to fail "leaving the
// racing file intact and no stage behind": InstallNew makes no distinction
// between a pre-existing and a mid-call race, since hardLinkNoReplace's
// atomicity covers both identically.
func TestInstallNew_DestinationAlreadyExists(t *testing.T) {
	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	version := "1.0.0"
	tag := "v1.0.0"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("verified bytes"))
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)

	srv := newInstallServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, map[string][]byte{
		"/" + tag + "/" + asset:                               archive,
		"/" + tag + "/" + defaultChecksumsName("wb", version): []byte(checksums),
	})

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "wb")
	if err := os.WriteFile(dest, []byte("racing copy"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := installTestConfig(srv)
	plan, err := cfg.PlanInstall(context.Background())
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	_, err = cfg.InstallNew(context.Background(), dest, plan)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindDestinationExists {
		t.Errorf("KindOf(err) = %v, want KindDestinationExists", KindOf(err))
	}

	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "racing copy" {
		t.Fatalf("dest content = %q, want unchanged %q", got, "racing copy")
	}
	assertOnlyEntries(t, destDir, "wb")
}

func TestInstallNew_ChecksumMismatchLeavesNothing(t *testing.T) {
	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	version := "1.0.0"
	tag := "v1.0.0"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("bytes"))
	wrongHash := sha256Hex([]byte("not the archive"))
	checksums := fmt.Sprintf("%s  %s\n", wrongHash, asset)

	srv := newInstallServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, map[string][]byte{
		"/" + tag + "/" + asset:                               archive,
		"/" + tag + "/" + defaultChecksumsName("wb", version): []byte(checksums),
	})

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "wb")

	cfg := installTestConfig(srv)
	plan, err := cfg.PlanInstall(context.Background())
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	_, err = cfg.InstallNew(context.Background(), dest, plan)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindChecksum {
		t.Errorf("KindOf(err) = %v, want KindChecksum", KindOf(err))
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("dest must not exist after a checksum failure, stat err = %v", statErr)
	}
	assertOnlyEntries(t, destDir)
}

// The platform check runs before any network request, so an unreachable
// ReleasesAPIURL never gets dialed for an unsupported platform. Planning,
// not InstallNew itself, is where this check now lives.
func TestPlanInstall_UnsupportedPlatformMakesNoRequest(t *testing.T) {
	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{
		BinaryName:         "wb",
		Repository:         "acme/wb",
		ReleasesAPIURL:     "http://127.0.0.1:1/releases", // nothing listens here
		SupportedPlatforms: []Platform{{GOOS: "darwin", GOARCH: "arm64"}},
	}

	_, err := cfg.PlanInstall(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindUnsupportedPlatform {
		t.Errorf("KindOf(err) = %v, want KindUnsupportedPlatform (a wrong kind here would mean a request was attempted)", KindOf(err))
	}
}

func TestPlanInstall_ReleaseLookupFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		BinaryName:     "wb",
		Repository:     "acme/wb",
		ReleasesAPIURL: srv.URL,
		HTTPClient:     srv.Client(),
	}

	_, err := cfg.PlanInstall(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindReleaseLookup {
		t.Errorf("KindOf(err) = %v, want KindReleaseLookup", KindOf(err))
	}
}

// InstallNew performs no lookup of its own: a caller who never called
// PlanInstall (a zero InstallPlan) gets a clear KindUnexpected error rather
// than InstallNew silently resolving "latest" behind the caller's back.
func TestInstallNew_ZeroPlanIsRejected(t *testing.T) {
	cfg := Config{BinaryName: "wb", Repository: "acme/wb"}
	destDir := t.TempDir()
	_, err := cfg.InstallNew(context.Background(), filepath.Join(destDir, "wb"), InstallPlan{})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
}
