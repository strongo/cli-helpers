package selfupdate

import (
	"archive/tar"
	"archive/zip"
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
	"strings"
	"testing"
)

// --- default AssetName / ChecksumsName / DownloadURL ---

func TestDefaultAssetName(t *testing.T) {
	tests := []struct {
		name    string
		version string
		goos    string
		goarch  string
		want    string
	}{
		{"linux amd64", "1.2.3", "linux", "amd64", "wb_1.2.3_linux_amd64.tar.gz"},
		{"darwin arm64", "1.2.3", "darwin", "arm64", "wb_1.2.3_darwin_arm64.tar.gz"},
		{"windows amd64", "1.2.3", "windows", "amd64", "wb_1.2.3_windows_amd64.zip"},
		{"strips leading v", "v1.2.3", "linux", "amd64", "wb_1.2.3_linux_amd64.tar.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultAssetName("wb", tt.version, tt.goos, tt.goarch); got != tt.want {
				t.Errorf("defaultAssetName(...) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultChecksumsName(t *testing.T) {
	if got := defaultChecksumsName("wb", "v1.2.3"); got != "wb_1.2.3_checksums.txt" {
		t.Errorf("defaultChecksumsName = %q", got)
	}
}

// The download URL is the release's OWN tag URL, never the "/releases/
// latest/download/" alias — this is the fix for the reference
// implementation's pinned-download bug.
func TestDefaultDownloadURL_IsPerTag(t *testing.T) {
	got := defaultDownloadURL("acme/tool", "v1.2.3", "tool_1.2.3_linux_amd64.tar.gz")
	want := "https://github.com/acme/tool/releases/download/v1.2.3/tool_1.2.3_linux_amd64.tar.gz"
	if got != want {
		t.Errorf("defaultDownloadURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "/latest/") {
		t.Errorf("defaultDownloadURL must not use the /releases/latest/ alias: %q", got)
	}
}

// --- archive helpers ---

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

func makeZip(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(binName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// servePerTagRelease starts an httptest.Server that serves assets/checksums
// under /<tag>/<name>, matching defaultDownloadURL's shape, so tests can use
// the package's own default DownloadURL func unmodified except for BaseURL.
func servePerTagRelease(t *testing.T) (*httptest.Server, map[string][]byte) {
	t.Helper()
	files := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, files
}

func perTagDownloadURL(baseURL string) func(repository, tag, asset string) string {
	return func(_, tag, asset string) string {
		return baseURL + "/" + tag + "/" + asset
	}
}

func TestDownloadAndVerify_Success(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	binContent := []byte("the new binary bytes")
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", binContent)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = archive
	files["/"+tag+"/"+defaultChecksumsName("wb", version)] = []byte(checksums)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	path, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if !bytes.Equal(got, binContent) {
		t.Errorf("extracted binary = %q, want %q", got, binContent)
	}
}

func TestDownloadAndVerify_Zip(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	binContent := []byte("windows binary bytes")
	asset := defaultAssetName("wb", version, "windows", "amd64")
	archive := makeZip(t, "wb.exe", binContent)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = archive
	files["/"+tag+"/"+defaultChecksumsName("wb", version)] = []byte(checksums)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "windows", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	path, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if !bytes.Equal(got, binContent) {
		t.Errorf("extracted binary = %q, want %q", got, binContent)
	}
}

// A checksum mismatch aborts BEFORE any extraction — no temp file is
// created (REQ: checksum-before-extract).
func TestDownloadAndVerify_ChecksumMismatchAbortsBeforeExtraction(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("the new binary bytes"))
	wrongHash := sha256Hex([]byte("not the archive"))
	checksums := fmt.Sprintf("%s  %s\n", wrongHash, asset)

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = archive
	files["/"+tag+"/"+defaultChecksumsName("wb", version)] = []byte(checksums)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	var createTempCalled bool
	origCreateTemp := createTempFunc
	createTempFunc = func(dir, pattern string) (tempFile, error) {
		createTempCalled = true
		return origCreateTemp(dir, pattern)
	}
	t.Cleanup(func() { createTempFunc = origCreateTemp })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	path, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err == nil {
		_ = os.Remove(path)
		t.Fatal("expected checksum verification error, got nil")
	}
	if path != "" {
		t.Fatalf("expected no extracted path on mismatch, got %q", path)
	}
	if createTempCalled {
		t.Error("createTempFunc was called on a checksum mismatch; extraction must not be attempted")
	}
	if KindOf(err) != KindChecksum {
		t.Errorf("KindOf(err) = %v, want KindChecksum", KindOf(err))
	}
}

func TestDownloadAndVerify_MissingChecksumEntry(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("bytes"))
	// Lists a different file, not our asset.
	checksums := fmt.Sprintf("%s  wb_%s_darwin_arm64.tar.gz\n", sha256Hex(archive), version)

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = archive
	files["/"+tag+"/"+defaultChecksumsName("wb", version)] = []byte(checksums)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	_, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err == nil {
		t.Fatal("expected error for missing checksum entry, got nil")
	}
	if KindOf(err) != KindChecksum {
		t.Errorf("KindOf(err) = %v, want KindChecksum", KindOf(err))
	}
}

// A 404 asset (or checksums) fetch is KindUnknownTag, not a generic
// KindDownload — this is what lets Update surface "this tag/platform has no
// asset" distinctly (REQ: pinned-unknown-tag).
func TestDownloadAndVerify_AssetNotFoundIsUnknownTag(t *testing.T) {
	srv, _ := servePerTagRelease(t) // no files registered -> every path 404s
	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	_, err := cfg.downloadAndVerify(context.Background(), "v9.9.9", "9.9.9")
	if err == nil {
		t.Fatal("expected error when asset fetch 404s, got nil")
	}
	if KindOf(err) != KindUnknownTag {
		t.Errorf("KindOf(err) = %v, want KindUnknownTag", KindOf(err))
	}
}

func TestDownloadAndVerify_ChecksumsNotFoundIsUnknownTag(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("bytes"))

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = archive
	// checksums intentionally absent.

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	_, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindUnknownTag {
		t.Errorf("KindOf(err) = %v, want KindUnknownTag", KindOf(err))
	}
}

func TestDownloadAndVerify_AssetServerErrorIsDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	_, err := cfg.downloadAndVerify(context.Background(), "v1.0.0", "1.0.0")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindDownload {
		t.Errorf("KindOf(err) = %v, want KindDownload", KindOf(err))
	}
}

func TestFetchBytes_RequestError(t *testing.T) {
	if _, _, err := fetchBytes(context.Background(), http.DefaultClient, "http://example.com/\x7f"); err == nil {
		t.Fatal("expected request build error, got nil")
	}
}

func TestFetchBytes_DoError(t *testing.T) {
	if _, _, err := fetchBytes(context.Background(), &http.Client{}, "http://127.0.0.1:1"); err == nil {
		t.Fatal("expected transport error, got nil")
	}
}

// A response whose declared Content-Length exceeds what the handler actually
// writes forces the net/http client to fail the body read mid-stream (the
// server closes the connection once it detects it wrote less than promised),
// exercising fetchBytes' own io.ReadAll error branch distinctly from a
// request-build or transport (Do) error.
func TestFetchBytes_BodyReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short body"))
	}))
	t.Cleanup(srv.Close)

	if _, _, err := fetchBytes(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("expected a body read error from the truncated response, got nil")
	}
}

// errTransport is an http.RoundTripper stub that always fails without
// opening any real connection, so downloadAndVerify's OWN fetchBytes-error
// branches (as opposed to a non-200 status) can be exercised deterministically
// and without touching the network at all.
type errTransport struct{ err error }

func (rt errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

// downloadAndVerify's asset request is the FIRST fetchBytes call it makes;
// a transport that always errors therefore fails on that first call.
func TestDownloadAndVerify_AssetTransportError(t *testing.T) {
	cfg := Config{
		BinaryName:  "wb",
		Repository:  "acme/wb",
		HTTPClient:  &http.Client{Transport: errTransport{err: errors.New("boom")}},
		DownloadURL: func(_, tag, asset string) string { return "http://unused.test/" + tag + "/" + asset },
	}.withDefaults()

	_, err := cfg.downloadAndVerify(context.Background(), "v1.0.0", "1.0.0")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindDownload {
		t.Errorf("KindOf(err) = %v, want KindDownload", KindOf(err))
	}
}

// downloadAndVerify's checksums request is a SEPARATE fetchBytes call from
// the asset request. DownloadURL routes the asset name to a real server and
// the checksums name to an address nothing listens on, so the asset fetch
// succeeds and the checksums fetch fails with a transport (not status) error.
func TestDownloadAndVerify_ChecksumsTransportError(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("bytes"))
	checksumsFile := defaultChecksumsName("wb", version)

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = archive

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{
		BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(),
		DownloadURL: func(_, tag, name string) string {
			if name == checksumsFile {
				return "http://127.0.0.1:1/" + tag + "/" + name
			}
			return srv.URL + "/" + tag + "/" + name
		},
	}.withDefaults()

	_, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindDownload {
		t.Errorf("KindOf(err) = %v, want KindDownload", KindOf(err))
	}
}

// A checksums fetch that fails with a non-200, non-404 status (e.g. a 500)
// is KindDownload, distinct from both TestDownloadAndVerify_
// ChecksumsNotFoundIsUnknownTag (404) and the asset-side equivalent
// TestDownloadAndVerify_AssetServerErrorIsDownload.
func TestDownloadAndVerify_ChecksumsServerErrorIsDownload(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	archive := makeTarGz(t, "wb", []byte("bytes"))
	checksumsFile := defaultChecksumsName("wb", version)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, asset):
			_, _ = w.Write(archive)
		case strings.HasSuffix(r.URL.Path, checksumsFile):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	_, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindDownload {
		t.Errorf("KindOf(err) = %v, want KindDownload", KindOf(err))
	}
}

// A checksum that verifies correctly against corrupt archive bytes still
// must fail — at extraction, not verification — proving downloadAndVerify's
// own post-verification extractBinary error branch (KindUnexpected) is
// reachable even though checksum verification (which runs first, per
// REQ: checksum-before-extract) already passed.
func TestDownloadAndVerify_ExtractionErrorAfterChecksumVerified(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	asset := defaultAssetName("wb", version, "linux", "amd64")
	garbage := []byte("not a valid gzip archive at all")
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(garbage), asset)

	srv, files := servePerTagRelease(t)
	files["/"+tag+"/"+asset] = garbage
	files["/"+tag+"/"+defaultChecksumsName("wb", version)] = []byte(checksums)

	origOS, origArch := goosName, goarchName
	goosName, goarchName = "linux", "amd64"
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })

	cfg := Config{BinaryName: "wb", Repository: "acme/wb", HTTPClient: srv.Client(), DownloadURL: perTagDownloadURL(srv.URL)}.withDefaults()
	_, err := cfg.downloadAndVerify(context.Background(), tag, version)
	if err == nil {
		t.Fatal("expected extraction error, got nil")
	}
	if KindOf(err) != KindUnexpected {
		t.Errorf("KindOf(err) = %v, want KindUnexpected", KindOf(err))
	}
}

// --- findChecksum ---

func TestFindChecksum_SkipsBlankAndMalformedLines(t *testing.T) {
	checksums := []byte("\n  \nonlyonefield\nabc def ghi\ndeadbeef  wb_x.tar.gz\n")
	got, err := findChecksum(checksums, "wb_x.tar.gz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "deadbeef" {
		t.Fatalf("findChecksum = %q, want deadbeef", got)
	}
}

func TestFindChecksum_ScannerError(t *testing.T) {
	big := make([]byte, 80*1024)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := findChecksum(big, "whatever"); err == nil {
		t.Fatal("expected scanner error for oversized line, got nil")
	}
}

// --- extract branches ---

func TestExtractFromTarGz_BadGzip(t *testing.T) {
	if _, err := extractFromTarGz("wb", []byte("not a gzip"), "wb"); err == nil {
		t.Fatal("expected gzip error, got nil")
	}
}

func TestExtractFromTarGz_BadTar(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write([]byte("this is definitely not a valid tar stream payload"))
	_ = gw.Close()
	if _, err := extractFromTarGz("wb", buf.Bytes(), "wb"); err == nil {
		t.Fatal("expected tar read error, got nil")
	}
}

func TestExtractFromTarGz_NotFound(t *testing.T) {
	archive := makeTarGz(t, "other-file", []byte("x"))
	if _, err := extractFromTarGz("wb", archive, "wb"); err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

func TestExtractFromZip_BadZip(t *testing.T) {
	if _, err := extractFromZip("wb", []byte("not a zip"), "wb.exe"); err == nil {
		t.Fatal("expected zip error, got nil")
	}
}

func TestExtractFromZip_NotFound(t *testing.T) {
	archive := makeZip(t, "other.exe", []byte("x"))
	if _, err := extractFromZip("wb", archive, "wb.exe"); err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

func TestExtractFromZip_OpenError(t *testing.T) {
	raw := makeZip(t, "wb.exe", []byte("payload"))
	patchZipMethod(t, raw, 99)
	if _, err := extractFromZip("wb", raw, "wb.exe"); err == nil {
		t.Fatal("expected open error for unsupported compression, got nil")
	}
}

// patchZipMethod rewrites the 2-byte compression-method field (little-endian)
// in every local-file-header (PK\x03\x04, field at +8) and central-directory
// header (PK\x01\x02, field at +10) found in buf, forcing zip.File.Open to
// fail on an otherwise well-formed archive.
func patchZipMethod(t *testing.T, buf []byte, method uint16) {
	t.Helper()
	lo, hi := byte(method), byte(method>>8)
	for i := 0; i+4 <= len(buf); i++ {
		switch {
		case buf[i] == 'P' && buf[i+1] == 'K' && buf[i+2] == 3 && buf[i+3] == 4 && i+10 <= len(buf):
			buf[i+8], buf[i+9] = lo, hi
		case buf[i] == 'P' && buf[i+1] == 'K' && buf[i+2] == 1 && buf[i+3] == 2 && i+12 <= len(buf):
			buf[i+10], buf[i+11] = lo, hi
		}
	}
}

// --- writeTempBinary branches ---

func TestWriteTempBinary_CreateError(t *testing.T) {
	orig := createTempFunc
	t.Cleanup(func() { createTempFunc = orig })
	createTempFunc = func(string, string) (tempFile, error) {
		return nil, errors.New("no temp")
	}
	if _, err := writeTempBinary("wb", strings.NewReader("x")); err == nil {
		t.Fatal("expected create-temp error, got nil")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read fail") }

func TestWriteTempBinary_CopyError(t *testing.T) {
	if _, err := writeTempBinary("wb", errReader{}); err == nil {
		t.Fatal("expected copy error, got nil")
	}
}

type closeErrFile struct{ *os.File }

func (c *closeErrFile) Close() error {
	_ = c.File.Close()
	return errors.New("close fail")
}

func TestWriteTempBinary_CloseError(t *testing.T) {
	orig := createTempFunc
	t.Cleanup(func() { createTempFunc = orig })
	createTempFunc = func(dir, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return &closeErrFile{File: f}, nil
	}
	if _, err := writeTempBinary("wb", strings.NewReader("x")); err == nil {
		t.Fatal("expected close error, got nil")
	}
}
