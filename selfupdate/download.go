package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

// fetchBytes GETs url and returns the body alongside the HTTP status code,
// so callers can distinguish "not found" (worth reporting as
// KindUnknownTag: this release or platform simply has no such asset) from
// any other failure (KindDownload).
func fetchBytes(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// downloadAndVerify downloads the release asset matching the host platform
// for the given tag/version, verifies its sha256 against that same
// release's checksums file, and — only on a match — extracts the binary
// into a fresh temp file whose path is returned. Verification always
// precedes extraction (REQ: checksum-before-extract): on a mismatch, a
// missing checksum entry, or a fetch failure, this returns before any bytes
// are extracted or written, and the caller's existing binary is never
// touched.
//
// tag is the release's own published tag (used to build the download URL,
// per REQ: pinned-exact-tag) and version is the normalized display version
// (used for asset/checksum naming) — they differ whenever the tag carries a
// leading "v".
func (c Config) downloadAndVerify(ctx context.Context, tag, version string) (string, error) {
	asset := c.AssetName(c.BinaryName, version, goosName, goarchName)
	assetBytes, status, err := fetchBytes(ctx, c.HTTPClient, c.DownloadURL(c.Repository, tag, asset))
	if err != nil {
		return "", &Failure{Kind: KindDownload, Err: fmt.Errorf("download %s: %w", asset, err)}
	}
	if status == http.StatusNotFound {
		return "", &Failure{Kind: KindUnknownTag, Err: fmt.Errorf("no asset %q published for release %s", asset, tag)}
	}
	if status != http.StatusOK {
		return "", &Failure{Kind: KindDownload, Err: fmt.Errorf("download of %s failed: status %d", asset, status)}
	}

	checksumsFile := c.ChecksumsName(c.BinaryName, version)
	checksumBytes, cStatus, err := fetchBytes(ctx, c.HTTPClient, c.DownloadURL(c.Repository, tag, checksumsFile))
	if err != nil {
		return "", &Failure{Kind: KindDownload, Err: fmt.Errorf("download %s: %w", checksumsFile, err)}
	}
	if cStatus == http.StatusNotFound {
		return "", &Failure{Kind: KindUnknownTag, Err: fmt.Errorf("no checksums file %q published for release %s", checksumsFile, tag)}
	}
	if cStatus != http.StatusOK {
		return "", &Failure{Kind: KindDownload, Err: fmt.Errorf("download of %s failed: status %d", checksumsFile, cStatus)}
	}

	expected, err := findChecksum(checksumBytes, asset)
	if err != nil {
		return "", &Failure{Kind: KindChecksum, Err: err}
	}
	sum := sha256.Sum256(assetBytes)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actual, expected) {
		return "", &Failure{Kind: KindChecksum, Err: fmt.Errorf("checksum verification failed for %s: expected %s, got %s", asset, expected, actual)}
	}

	// Verified — safe to extract.
	path, err := extractBinary(c.BinaryName, assetBytes, goosName)
	if err != nil {
		return "", &Failure{Kind: KindUnexpected, Err: err}
	}
	return path, nil
}

// findChecksum parses a GoReleaser checksums.txt (lines of
// "<sha256hex>  <file>") and returns the expected hex digest for asset. A
// missing entry is an error, per REQ: checksum-before-extract ("a missing or
// unfetchable checksum entry" must abort the same as a mismatch).
func findChecksum(checksums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == asset {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum entry found for %s", asset)
}

// extractBinary extracts binaryName from a verified archive into a temp
// file and returns its path. The archive format is chosen by goos (.zip for
// windows, .tar.gz otherwise), matching GoReleaser's own per-platform
// archive format.
func extractBinary(binaryName string, archive []byte, goos string) (string, error) {
	want := binaryName
	if goos == "windows" {
		want += ".exe"
	}
	if goos == "windows" {
		return extractFromZip(binaryName, archive, want)
	}
	return extractFromTarGz(binaryName, archive, want)
}

func extractFromTarGz(binaryName string, archive []byte, want string) (string, error) {
	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", fmt.Errorf("open gzip archive: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar archive: %w", err)
		}
		if path.Base(hdr.Name) == want {
			return writeTempBinary(binaryName, tr)
		}
	}
	return "", fmt.Errorf("binary %s not found in archive", want)
}

func extractFromZip(binaryName string, archive []byte, want string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", fmt.Errorf("open zip archive: %w", err)
	}
	for _, f := range zr.File {
		if path.Base(f.Name) == want {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("open %s in archive: %w", want, err)
			}
			defer func() { _ = rc.Close() }()
			return writeTempBinary(binaryName, rc)
		}
	}
	return "", fmt.Errorf("binary %s not found in archive", want)
}

// tempFile is the subset of *os.File used when writing a temp file. It is an
// interface so tests can inject a file whose Close fails after a successful
// copy.
type tempFile interface {
	io.WriteCloser
	Name() string
}

// createTempFunc is a test seam over os.CreateTemp so the temp-file creation
// and close-error branches are exercisable without leaving real temp files
// behind on a failed test run.
var createTempFunc = func(dir, pattern string) (tempFile, error) {
	return os.CreateTemp(dir, pattern)
}

// writeTempBinary copies r into a new temp file (named after binaryName, so
// concurrent extractions for different consumers in the same process don't
// collide on a shared generic prefix) and returns its path.
func writeTempBinary(binaryName string, r io.Reader) (string, error) {
	f, err := createTempFunc("", binaryName+"-update-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write temp binary: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close temp binary: %w", err)
	}
	return f.Name(), nil
}
