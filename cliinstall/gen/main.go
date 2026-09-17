// Command gen records, under cliinstall/testdata/snapshots/, a snapshot of
// each catalog CLI's real published release asset list and (where one
// exists) its real Homebrew cask file. TestCatalogAssetNamingMatchesRelease
// Snapshots and TestCatalogCaskOSMatchesCaskSnapshots (in the cliinstall
// package) read only the committed output of this program; neither they
// nor any other test invokes gen itself, so the test suite stays
// network-free (cli-install#req:no-network-in-tests) while the snapshots
// they check against stay traceable to a real "gh" invocation.
//
// Run it by hand, from the module root, whenever a catalog entry's release
// naming changes or a periodic refresh is wanted:
//
//	go generate ./cliinstall/...
//
// It shells out to the "gh" CLI, which must already be authenticated
// (`gh auth status`).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// snapshotsDir is where gen writes its output, relative to the working
// directory `go generate` runs this command in: the directory containing
// the //go:generate directive (cliinstall/), never the module root.
const snapshotsDir = "testdata/snapshots"

// target describes where to record one catalog CLI's snapshot from. It is
// deliberately independent of the cliinstall package's own Entry type: gen
// is the thing that PRODUCES evidence an Entry is checked against, so it
// must not import cliinstall and trust the very values it is meant to
// verify.
type target struct {
	// id is the catalog id; snapshots are written to
	// <snapshotsDir>/releases/<id>.json and, when caskRepo is set,
	// <snapshotsDir>/casks/<id>.rb.
	id string
	// repo is "owner/repo" that publishes this CLI's GitHub Releases.
	repo string
	// tagPrefix, when non-empty, selects this CLI's releases within repo by
	// filtering tag names to this prefix and taking the newest (repos that
	// publish more than one product's releases, e.g. synchestra's mirror).
	tagPrefix string
	// caskRepo is "owner/repo" of the Homebrew tap holding this CLI's
	// generated cask file; empty means this CLI publishes no cask.
	caskRepo string
	// caskPath is caskRepo's path to the cask file.
	caskPath string
}

var targets = []target{
	{id: "wb", repo: "sneat-dev/wb", caskRepo: "sneat-dev/homebrew-tap", caskPath: "Casks/wb.rb"},
	{id: "specscore", repo: "specscore/specscore-cli", caskRepo: "specscore/homebrew-tap", caskPath: "Casks/specscore.rb"},
	{id: "chatwright", repo: "chatwright/cli", caskRepo: "chatwright/homebrew-tap", caskPath: "Casks/chatwright.rb"},
	{id: "codegrapher", repo: "code-grapher/codegrapher", caskRepo: "code-grapher/homebrew-tap", caskPath: "Casks/codegrapher.rb"},
	{id: "cover100", repo: "sneat-dev/cover100-cli"},
	{id: "ovdb", repo: "openvaultdb/ovdb", caskRepo: "openvaultdb/homebrew-tap", caskPath: "Casks/ovdb.rb"},
	{id: "synchestra", repo: "synchestra-io/synchestra-releases", tagPrefix: "cli-"},
	{id: "ingitdb", repo: "ingitdb/ingitdb-cli", caskRepo: "ingitdb/homebrew-cli", caskPath: "Casks/ingitdb.rb"},
	{id: "datatug", repo: "datatug/datatug-cli", caskRepo: "datatug/homebrew-tap", caskPath: "Casks/datatug.rb"},
}

// releaseSnapshot is the recorded shape of <snapshotsDir>/releases/<id>.json.
type releaseSnapshot struct {
	Repo    string   `json:"repo"`
	Tag     string   `json:"tag"`
	Version string   `json:"version"`
	Assets  []string `json:"assets"`
}

// runGH invokes the "gh" CLI and returns its trimmed stdout. It is a
// package variable, not a direct call to execGH, purely so tests can
// replace it with canned responses and prove every branch of run,
// recordRelease, latestTag, and recordCask without touching the network or
// requiring "gh" to be installed; execGH itself (the real subprocess
// wiring) has its own dedicated tests using a fake "gh" on PATH.
var runGH = execGH

// exitFunc is os.Exit by default. It is a package variable so a test can
// observe main's non-zero-exit path without ending the test process.
var exitFunc = os.Exit

func main() {
	if err := run(snapshotsDir); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		exitFunc(1)
	}
}

// run records every target's snapshot(s) under root.
func run(root string) error {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("snapshots directory not found at %q (run `go generate ./cliinstall/...` from the module root)", root)
	}
	for _, t := range targets {
		if err := recordRelease(root, t); err != nil {
			return fmt.Errorf("%s: release snapshot: %w", t.id, err)
		}
		if t.caskRepo != "" {
			if err := recordCask(root, t); err != nil {
				return fmt.Errorf("%s: cask snapshot: %w", t.id, err)
			}
		}
		fmt.Println("recorded", t.id)
	}
	return nil
}

// recordRelease resolves t's newest matching release via `gh release view`
// (or, for a tag-prefixed repository, `gh release list` filtered by prefix
// first) and writes its asset name list to <root>/releases/<id>.json.
func recordRelease(root string, t target) error {
	tag, err := latestTag(t)
	if err != nil {
		return err
	}
	args := []string{"release", "view"}
	if tag != "" {
		args = append(args, tag)
	}
	args = append(args, "--repo", t.repo, "--json", "tagName,assets")
	out, err := runGH(args...)
	if err != nil {
		return err
	}
	var parsed struct {
		TagName string `json:"tagName"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return fmt.Errorf("decode gh output: %w", err)
	}
	names := make([]string, 0, len(parsed.Assets))
	for _, a := range parsed.Assets {
		names = append(names, a.Name)
	}
	sort.Strings(names)

	snap := releaseSnapshot{
		Repo:    t.repo,
		Tag:     parsed.TagName,
		Version: strings.TrimPrefix(strings.TrimPrefix(parsed.TagName, t.tagPrefix), "v"),
		Assets:  names,
	}
	// releaseSnapshot holds only strings and a []string, so encoding it can
	// never fail (no channels, funcs, cyclic references, or non-finite
	// floats) — MarshalIndent's error is deliberately ignored rather than
	// guarded by a dead, uncoverable branch.
	payload, _ := json.MarshalIndent(snap, "", "  ")
	payload = append(payload, '\n')
	return os.WriteFile(filepath.Join(root, "releases", t.id+".json"), payload, 0o644)
}

// latestTag returns the release tag to snapshot: "" (meaning "let gh pick
// its own latest") for an unprefixed repository, or the newest tag matching
// t.tagPrefix for a repository that mirrors more than one product's
// releases.
func latestTag(t target) (string, error) {
	if t.tagPrefix == "" {
		return "", nil
	}
	out, err := runGH("release", "list", "--repo", t.repo, "--json", "tagName", "--limit", "100")
	if err != nil {
		return "", err
	}
	var releases []struct {
		TagName string `json:"tagName"`
	}
	if err := json.Unmarshal(out, &releases); err != nil {
		return "", fmt.Errorf("decode gh release list: %w", err)
	}
	// `gh release list` returns newest-published first; the first match is
	// the newest release of this product.
	for _, r := range releases {
		if strings.HasPrefix(r.TagName, t.tagPrefix) {
			return r.TagName, nil
		}
	}
	return "", fmt.Errorf("no release tag with prefix %q found in %s", t.tagPrefix, t.repo)
}

// recordCask fetches t's generated cask file via the GitHub contents API and
// writes its raw content to <root>/casks/<id>.rb.
func recordCask(root string, t target) error {
	out, err := runGH("api", "repos/"+t.caskRepo+"/contents/"+t.caskPath, "--jq", ".content")
	if err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.ReplaceAll(string(out), "\n", "")))
	if err != nil {
		return fmt.Errorf("decode cask content: %w", err)
	}
	return os.WriteFile(filepath.Join(root, "casks", t.id+".rb"), decoded, 0o644)
}

// execGH is runGH's production implementation: it runs "gh" with args and
// returns its trimmed stdout, or an error carrying stderr when it fails.
func execGH(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}
