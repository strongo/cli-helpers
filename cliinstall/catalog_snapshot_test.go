package cliinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseSnapshot mirrors gen/main.go's releaseSnapshot: the recorded shape
// of testdata/snapshots/releases/<id>.json. Kept as a separate type (not
// shared via import) because gen is package main and never imported by
// this package or its tests (cli-install#req:no-network-in-tests — gen is
// the only thing that touches the network, and it is never on the test
// import graph).
type releaseSnapshot struct {
	Repo    string   `json:"repo"`
	Tag     string   `json:"tag"`
	Version string   `json:"version"`
	Assets  []string `json:"assets"`
}

func loadReleaseSnapshot(t *testing.T, id string) releaseSnapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "snapshots", "releases", id+".json"))
	if err != nil {
		t.Fatalf("%s: read release snapshot: %v", id, err)
	}
	var snap releaseSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("%s: decode release snapshot: %v", id, err)
	}
	return snap
}

func loadCaskSnapshot(t *testing.T, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "snapshots", "casks", id+".rb"))
	if err != nil {
		t.Fatalf("%s: read cask snapshot: %v", id, err)
	}
	return string(data)
}

// defaultAssetName mirrors selfupdate.Config's documented GoReleaser-shaped
// default for AssetName (selfupdate/config.go's defaultAssetName, which is
// unexported and so cannot be called directly from this package): every
// catalog entry either leaves AssetName nil, meaning this formula applies,
// or overrides it, in which case the entry's own func is used instead. Both
// paths are exercised below.
func defaultAssetName(binary, version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return binary + "_" + version + "_" + goos + "_" + goarch + "." + ext
}

// defaultChecksumsName mirrors selfupdate.Config's default ChecksumsName
// formula (see defaultAssetName's doc comment).
func defaultChecksumsName(binary, version string) string {
	return binary + "_" + version + "_checksums.txt"
}

func assetSet(assets []string) map[string]bool {
	m := make(map[string]bool, len(assets))
	for _, a := range assets {
		m[a] = true
	}
	return m
}

// TestCatalogAssetNamingMatchesReleaseSnapshots proves
// cli-install#req:catalog-validated's asset-naming rule: "each entry's
// asset and checksums names match a recorded snapshot of that CLI's
// release asset list for every supported platform, including
// checksums.txt for ingitdb on darwin and windows".
func TestCatalogAssetNamingMatchesReleaseSnapshots(t *testing.T) {
	for _, id := range wantIDs {
		t.Run(id, func(t *testing.T) {
			e, ok := ByID(id)
			if !ok {
				t.Fatalf("catalog has no entry %q", id)
			}
			snap := loadReleaseSnapshot(t, id)
			if snap.Repo != e.Repository {
				t.Errorf("snapshot repo %q != entry Repository %q", snap.Repo, e.Repository)
			}
			have := assetSet(snap.Assets)

			assetName := e.AssetName
			if assetName == nil {
				assetName = defaultAssetName
			}
			checksumsName := e.ChecksumsName
			if checksumsName == nil {
				checksumsName = defaultChecksumsName
			}

			if want := checksumsName(e.ID, snap.Version); !have[want] {
				t.Errorf("checksums file %q not found in snapshot assets %v", want, snap.Assets)
			}
			for _, p := range e.SupportedPlatforms {
				want := assetName(e.ID, snap.Version, p.GOOS, p.GOARCH)
				if !have[want] {
					t.Errorf("asset %q (platform %s/%s) not found in snapshot assets %v", want, p.GOOS, p.GOARCH, snap.Assets)
				}
			}
		})
	}
}

// TestCatalogCaskOSMatchesCaskSnapshots proves cli-install#req:catalog-
// validated's cask rule: "each cask's declared operating systems match its
// recorded cask snapshot" — every entry.CaskOS "darwin"/"linux" value maps
// to an "on_macos"/"on_linux" block actually present in the recorded cask
// file, and vice versa (M1's "generated casks declare both macOS and Linux
// binaries" disposition).
func TestCatalogCaskOSMatchesCaskSnapshots(t *testing.T) {
	for _, id := range wantIDs {
		e, ok := ByID(id)
		if !ok {
			t.Fatalf("catalog has no entry %q", id)
		}
		if !e.HasCask() {
			continue
		}
		t.Run(id, func(t *testing.T) {
			raw := loadCaskSnapshot(t, id)
			if !strings.Contains(raw, "cask \""+id+"\"") {
				t.Errorf("cask snapshot does not declare cask %q: %s", id, raw)
			}
			hasMacOS := strings.Contains(raw, "on_macos")
			hasLinux := strings.Contains(raw, "on_linux")

			wantDarwin, wantLinux := false, false
			for _, os := range e.CaskOS {
				switch os {
				case "darwin":
					wantDarwin = true
				case "linux":
					wantLinux = true
				default:
					t.Errorf("entry declares unrecognized CaskOS %q", os)
				}
			}
			if wantDarwin != hasMacOS {
				t.Errorf("CaskOS darwin=%v but cask snapshot on_macos block present=%v", wantDarwin, hasMacOS)
			}
			if wantLinux != hasLinux {
				t.Errorf("CaskOS linux=%v but cask snapshot on_linux block present=%v", wantLinux, hasLinux)
			}
		})
	}
}

// TestEntriesWithoutCaskHaveNoCaskSnapshot documents which catalog entries
// publish no Homebrew cask, so the previous test's `continue` is a
// deliberate skip, not a silently-passing gap.
func TestEntriesWithoutCaskHaveNoCaskSnapshot(t *testing.T) {
	want := map[string]bool{"cover100": true, "synchestra": true}
	for _, id := range wantIDs {
		e, _ := ByID(id)
		if e.HasCask() {
			continue
		}
		if !want[id] {
			t.Errorf("entry %q unexpectedly has no cask", id)
		}
		delete(want, id)
	}
	for id := range want {
		t.Errorf("expected cask-less entry %q was not found in the catalog", id)
	}
}
