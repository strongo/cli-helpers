package cobracmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- self-update ≡ upgrade <self> equivalence matrix ------------------------
//
// cli-install#ac:self-update-equals-upgrade-self requires that `self-update`
// and `upgrade <self>` "reach the same library call on the running
// executable and produce the same action, target version, ... and failure
// kind" for every install-method classification. selfupdate.Config.
// DetectSelf calls the unexported osExecutable (== os.Executable) with no
// test seam reachable from outside the selfupdate package, so the ONLY way
// to prove this for real — not by asserting against a hand-rolled Detection
// value neither command actually used — is to build a real fixture binary
// (testdata/selfupdateequiv, excluded from this module's own build graph:
// "testdata" is skipped by every Go tool's "./..." pattern) that registers
// BOTH commands from the IDENTICAL selfupdate.Config, place it at a chosen
// path, and exec it. See that fixture's own doc comment for the full
// rationale, and skillsync/selfupdate/two_binary_integration_test.go for the
// precedent this mirrors.
//
// Every scenario below runs `--dry-run --yes --format json`: dry-run never
// downloads, replaces, or invokes a manager command on EITHER side (see
// selfupdate.Config.UpdateAt's own DryRun branches for Manual and Managed,
// and cliinstall.Upgrade's own "opts.DryRun ⇒ never calls ExecuteUpgrade"
// contract) — verified manually against this exact fixture before writing
// this test, including the executable-managed scenario, which never reaches
// RunManaged under DryRun even though the Cobra adapter always wires a REAL
// (non-test-double) ManagedCommandRunner.

func equivBinaryName() string {
	if runtime.GOOS == "windows" {
		return "cover100.exe"
	}
	return "cover100"
}

// buildSelfUpdateEquivFixture builds testdata/selfupdateequiv once. Callers
// copy the result to as many scenario-specific paths as they need via
// placeEquivFixture — DetectSelf/HostDir classify by PATH, not by content,
// so one build is reused for every scenario in this file.
func buildSelfUpdateEquivFixture(t *testing.T) string {
	t.Helper()
	return buildEquivGoFixture(t, "./testdata/selfupdateequiv", "built-"+equivBinaryName())
}

// buildEquivProbe builds testdata/selfupdateequivprobe once — task-22 third
// review S4's "real --yes through an executable manager ... with host hook
// count" needs a real, separately-built executable that can stand in for
// what a package manager's own upgrade step leaves on PATH (see that
// fixture's own doc comment for why a fake RunManaged step alone is not
// enough to reach AfterUpdate).
func buildEquivProbe(t *testing.T) string {
	t.Helper()
	return buildEquivGoFixture(t, "./testdata/selfupdateequivprobe", "built-probe-"+equivBinaryName())
}

// buildEquivGoFixture builds the package at pkgDir into a binary named
// outName under a fresh t.TempDir(), hermetically (task-22 review S5): no
// network, no toolchain surprise, and a PATH reduced to exactly the
// directory containing the resolved `go` binary so the build can never
// accidentally exec a different `go` or a shell-shadowed `cover100` from the
// real environment.
func buildEquivGoFixture(t *testing.T, pkgDir, outName string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not found on PATH; skipping the self-update equivalence matrix (task-22 review S5)")
	}
	out := filepath.Join(t.TempDir(), outName)
	cmd := exec.Command(goBin, "build", "-o", out, pkgDir)
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=mod",
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"PATH="+filepath.Dir(goBin),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s fixture: %v\n%s", pkgDir, err, output)
	}
	return out
}

// placeEquivFixture copies built into a fresh directory ending in subdir
// (e.g. "host/bin" for a manual-looking install), naming the copy exactly
// equivBinaryName() so both DetectSelf and cliinstall's own
// installFilePath(hostDir, hostID) resolve the identical file.
func placeEquivFixture(t *testing.T, built, subdir string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), filepath.FromSlash(subdir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, equivBinaryName())
	if err := os.WriteFile(dest, data, 0o755); err != nil { //nolint:gosec // a deliberately executable fixture copy
		t.Fatal(err)
	}
	return dest
}

// equivReleaseServer serves exactly one /releases listing, offline
// (cli-install#req:no-network-in-tests / self-update's own equivalent).
func equivReleaseServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"tag_name":%q,"prerelease":false,"draft":false}]`, tag) //nolint:errcheck
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// equivAssetReleaseServer is equivReleaseServer plus a real downloadable
// asset and checksums file matching cover100's own GoReleaser-shaped
// defaults (task-22 review S4: "a release server that also serves the
// asset and checksum"), so a real, non-dry-run manual replacement can
// actually complete offline.
func equivAssetReleaseServer(t *testing.T, tag, content string) *httptest.Server {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	archive := makeTarGzFixture(t, "cover100", []byte(content))
	checksum := sha256HexFixture(archive)
	assetName := fmt.Sprintf("cover100_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	checksumsName := fmt.Sprintf("cover100_%s_checksums.txt", version)
	checksumsBody := fmt.Sprintf("%s  %s\n", checksum, assetName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"tag_name":%q,"prerelease":false,"draft":false}]`, tag) //nolint:errcheck
		case "/" + tag + "/" + assetName:
			_, _ = w.Write(archive)
		case "/" + tag + "/" + checksumsName:
			_, _ = io.WriteString(w, checksumsBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type equivRun struct {
	stdout, stderr string
	exitCode       int
}

func runEquivFixture(t *testing.T, binary string, env []string, args ...string) equivRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	res := equivRun{stdout: out.String(), stderr: errOut.String()}
	if err == nil {
		return res
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run %s %v: %v (stderr=%s)", binary, args, err, errOut.String())
	}
	res.exitCode = exitErr.ExitCode()
	return res
}

func equivEnv(t *testing.T, endpoint, currentVersion, marker, executable string, extraPathDirs ...string) []string {
	t.Helper()
	// task-22 review S5: PATH is built ONLY from the given (t.TempDir()-
	// rooted) directories, never inherited from the real environment — a
	// real "cover100" happening to be installed on a developer's own PATH
	// must never change what this subprocess finds. The host's own
	// classification never depends on PATH anyway (DetectSelf/HostDir
	// resolve the real running executable directly); PATH here only
	// controls the deliberate "other copy" scenario below.
	path := strings.Join(extraPathDirs, string(os.PathListSeparator))
	env := []string{
		"FIXTURE_RELEASE_ENDPOINT=" + endpoint,
		"FIXTURE_CURRENT_VERSION=" + currentVersion,
		"PATH=" + path,
		"HOME=" + t.TempDir(), // UserHomeDir must resolve to something for DefaultInstallEnv
	}
	if marker != "" {
		env = append(env, "FIXTURE_MANAGER_MARKER="+marker)
	}
	if executable != "" {
		env = append(env, "FIXTURE_MANAGER_EXECUTABLE="+executable)
	}
	return env
}

// selfUpdateOutcomeJSON is selfupdate/cliui's own outcomeJSON shape,
// decoded independently here since that type is unexported.
type selfUpdateOutcomeJSON struct {
	Action  string `json:"action"`
	Manager string `json:"manager"`
	Command string `json:"command"`
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Target  string `json:"target"`
}

// upgradeTargetJSONForTest mirrors cliui's own unexported upgradeTargetJSON
// closely enough for this test's assertions.
type upgradeDocForTest struct {
	FailureKind string `json:"failure_kind"`
	Targets     []struct {
		Name            string   `json:"name"`
		Action          string   `json:"action"`
		Current         string   `json:"current"`
		Latest          string   `json:"latest"`
		Verdict         string   `json:"verdict"`
		Manager         string   `json:"manager"`
		Command         string   `json:"command"`
		ResolvedPath    string   `json:"resolved_path"`
		OtherPaths      []string `json:"other_paths"`
		Warnings        []string `json:"warnings"`
		FailureKind     string   `json:"failure_kind"`
		NonReleaseBuild bool     `json:"non_release_build"`
	} `json:"targets"`
}

func TestSelfUpdateEqualsUpgradeSelf(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real fixture binary; skipped under -short")
	}
	built := buildSelfUpdateEquivFixture(t)
	srv := equivReleaseServer(t, "v1.1.0")

	tests := []struct {
		name       string
		subdir     string
		marker     string
		executable string
		current    string
		tag        string

		wantSelfAction    string // selfupdate outcomeJSON.action
		wantUpgradeAction string // cliinstall upgradeTargetJSON.action
	}{
		{
			name: "manual", subdir: "host/bin", current: "1.0.0", tag: "v1.1.0",
			wantSelfAction: "planned", wantUpgradeAction: "dry_run",
		},
		{
			name: "executable-managed", subdir: "host/execmgr", marker: "/execmgr/", executable: "1", current: "1.0.0", tag: "v1.1.0",
			wantSelfAction: "planned", wantUpgradeAction: "dry_run",
		},
		{
			name: "redirect-only", subdir: "host/redirectmgr", marker: "/redirectmgr/", current: "1.0.0", tag: "v1.1.0",
			wantSelfAction: "redirected", wantUpgradeAction: "redirected",
		},
		{
			name: "ahead", subdir: "host/bin", current: "9.9.9", tag: "v1.0.0",
			wantSelfAction: "ahead", wantUpgradeAction: "ahead",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag := tt.tag
			if tag != "v1.1.0" {
				// The shared srv above always serves v1.1.0; "ahead" needs
				// its own server offering an OLDER release than tt.current.
				aheadSrv := equivReleaseServer(t, tag)
				runScenario(t, built, tt.subdir, aheadSrv.URL, tt.current, tt.marker, tt.executable, tt.wantSelfAction, tt.wantUpgradeAction)
				return
			}
			runScenario(t, built, tt.subdir, srv.URL, tt.current, tt.marker, tt.executable, tt.wantSelfAction, tt.wantUpgradeAction)
		})
	}

	// Ambiguous IS a true equivalence, per task-22's coordinator ruling
	// (B1): UpgradeBatchResult.Failed()/Failure() now count a Refused row
	// that carries a Failure (always selfupdate.KindAmbiguous) exactly like
	// UpgradeOutcomeFailed, so `upgrade <self> --dry-run --yes` exits
	// non-zero for an ambiguous host with failure_kind "ambiguous" — the
	// SAME outcome `self-update --dry-run --yes` reaches via UpdateAt's own
	// ambiguous check. Both commands still agree on the underlying facts
	// (current/latest) despite the batch-level refusal.
	//
	// This is the one place the two commands' Cobra adapters use a
	// deliberately DIFFERENT read of that same fact: --check/the bare
	// report (runUpgradeReport, via cliinstall.CheckUpgrades) never fails
	// for a refused/ambiguous row — self-update's own `--check` never fails
	// for one either, since selfupdate.Config.Check does not even consult
	// classification (task-22 review B1.3) — so equivalence there is
	// covered separately below, not by this --dry-run/--yes pair.
	t.Run("ambiguous", func(t *testing.T) {
		dest := placeEquivFixture(t, built, "host/plainlocation")
		env := equivEnv(t, srv.URL, "1.0.0", "", "")

		selfRes := runEquivFixture(t, dest, env, "self-update", "--dry-run", "--yes", "--format", "json")
		if selfRes.exitCode == 0 {
			t.Errorf("self-update exit code = 0, want non-zero for an ambiguous install")
		}
		if !strings.Contains(selfRes.stderr, "ambiguous") {
			t.Errorf("self-update stderr = %q, want it to mention ambiguous", selfRes.stderr)
		}

		upRes := runEquivFixture(t, dest, env, "upgrade", "cover100", "--dry-run", "--yes", "--format", "json")
		if upRes.exitCode == 0 {
			t.Fatalf("upgrade <self> exit code = 0, want non-zero (task-22 review B1: a refused/ambiguous row now counts as a batch failure, same as self-update): stdout=%s stderr=%s", upRes.stdout, upRes.stderr)
		}
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
		}
		if len(doc.Targets) != 1 || doc.Targets[0].Action != "refused" {
			t.Fatalf("doc.Targets = %+v, want exactly one 'refused' target", doc.Targets)
		}
		if doc.Targets[0].FailureKind != "ambiguous" {
			t.Errorf("doc.Targets[0].FailureKind = %q, want ambiguous (same failure kind self-update reports)", doc.Targets[0].FailureKind)
		}
		// Ambiguous fails selfupdate.Config.UpdateAt's own check BEFORE any
		// release lookup even runs (self-update#req:ambiguous-safe-default
		// — the same reason self-update's own ambiguous JSON has no
		// current/latest fields at all), so only Current — known without a
		// lookup — is populated; Latest stays empty on both sides.
		if doc.Targets[0].Current != "1.0.0" {
			t.Errorf("upgrade <self> Current = %q, want 1.0.0", doc.Targets[0].Current)
		}
		if doc.Targets[0].Latest != "" {
			t.Errorf("upgrade <self> Latest = %q, want empty (ambiguous never reaches a lookup)", doc.Targets[0].Latest)
		}
	})

	// --check's own equivalence: neither command fails BECAUSE OF THE
	// AMBIGUITY under --check (task-22 review B1.3) — self-update's own
	// --check calls selfupdate.Config.Check, which never even looks at
	// classification, and cliinstall's --check (cliinstall.CheckUpgrades)
	// deliberately narrows its own failure predicate to a genuine lookup
	// failure only, excluding a refused/ambiguous row. This scenario's
	// verdict IS UpdateAvailable, though, and fixtureErrors DOES map that
	// signal to its own dedicated exit code (2) for both commands — so
	// both sides exit 2 here, for the SAME reason (an update exists), and
	// the equivalence this asserts is that they agree, not that either is
	// zero.
	t.Run("ambiguous --check never fails either command", func(t *testing.T) {
		dest := placeEquivFixture(t, built, "host/plainlocation2")
		env := equivEnv(t, srv.URL, "1.0.0", "", "")

		selfRes := runEquivFixture(t, dest, env, "self-update", "--check", "--format", "json")
		if selfRes.exitCode != 2 {
			t.Errorf("self-update --check exit code = %d, want 2 (update-available, not ambiguity, per fixtureErrors): stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
		}

		upRes := runEquivFixture(t, dest, env, "upgrade", "cover100", "--check", "--format", "json")
		if upRes.exitCode != 2 {
			t.Errorf("upgrade <self> --check exit code = %d, want 2 (same reason, same mapped code): stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
		}
	})

	// REQ: host-target-is-running-binary / AC: self-update-equals-upgrade-
	// self "including a host run from a path that is not first on PATH":
	// self-update never consults PATH at all (DetectSelf uses only
	// os.Executable()), so its outcome must be BYTE-IDENTICAL whether or not
	// a decoy copy sits earlier on PATH; upgrade <self> DOES probe PATH for
	// every target including the host, so it must additionally report — but
	// never act on — that other copy.
	t.Run("host run from a path that is not first on PATH", func(t *testing.T) {
		dest := placeEquivFixture(t, built, "realhost/bin")
		decoyDir := filepath.Dir(placeEquivFixture(t, built, "decoy"))
		// Make the decoy an unrecognized copy (garbage content, not a
		// working binary) so this test proves the WARNING/comparison path
		// without needing a second fully working fixture build.
		if err := os.WriteFile(filepath.Join(decoyDir, equivBinaryName()), []byte("not a real binary"), 0o755); err != nil { //nolint:gosec
			t.Fatal(err)
		}

		withoutDecoy := equivEnv(t, srv.URL, "1.0.0", "", "")
		withDecoy := equivEnv(t, srv.URL, "1.0.0", "", "", decoyDir)

		selfWithout := runEquivFixture(t, dest, withoutDecoy, "self-update", "--dry-run", "--yes", "--format", "json")
		selfWith := runEquivFixture(t, dest, withDecoy, "self-update", "--dry-run", "--yes", "--format", "json")
		if selfWithout.stdout != selfWith.stdout {
			t.Errorf("self-update output changed with a decoy PATH copy present:\nwithout: %s\nwith:    %s", selfWithout.stdout, selfWith.stdout)
		}
		var selfOutcome selfUpdateOutcomeJSON
		if err := json.Unmarshal([]byte(selfWith.stdout), &selfOutcome); err != nil {
			t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfWith.stdout)
		}
		if selfOutcome.Action != "planned" {
			t.Errorf("self-update action = %q, want planned", selfOutcome.Action)
		}

		upWith := runEquivFixture(t, dest, withDecoy, "upgrade", "cover100", "--dry-run", "--yes", "--format", "json")
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upWith.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upWith.stdout)
		}
		if len(doc.Targets) != 1 {
			t.Fatalf("doc.Targets = %+v", doc.Targets)
		}
		target := doc.Targets[0]
		if target.Action != "dry_run" {
			t.Errorf("upgrade <self> action = %q, want dry_run (a decoy on PATH must not change the outcome)", target.Action)
		}
		if target.ResolvedPath != dest {
			t.Errorf("upgrade <self> resolved_path = %q, want the REAL running binary %q, never the decoy", target.ResolvedPath, dest)
		}
		foundWarning := false
		for _, w := range target.Warnings {
			if strings.Contains(w, "another copy") && strings.Contains(w, "PATH") {
				foundWarning = true
			}
		}
		if !foundWarning {
			t.Errorf("upgrade <self> warnings = %v, want one naming the other PATH copy", target.Warnings)
		}
	})

	// task-22 review S4: a REAL --yes pair, with a working release asset
	// and checksum, proving self-update and upgrade <self> reach the same
	// selfupdate.Config.UpdateAt outcome (ActionUpdated) for a manual
	// install with an update available — not merely their --dry-run
	// preview of it — using fixtureErrors so the exit code itself (not
	// just zero/non-zero) is compared, and FIXTURE_HOOK_MARKER so the
	// after-update hook's invocation COUNT is compared too.
	t.Run("manual real replacement (--yes)", func(t *testing.T) {
		assetSrv := equivAssetReleaseServer(t, "v1.1.0", "new binary content")

		selfDest := placeEquivFixture(t, built, "self/bin")
		selfMarker := filepath.Join(t.TempDir(), "self-hook.log")
		selfEnv := append(equivEnv(t, assetSrv.URL, "1.0.0", "", ""), "FIXTURE_HOOK_MARKER="+selfMarker)
		selfRes := runEquivFixture(t, selfDest, selfEnv, "self-update", "--yes", "--format", "json")
		if selfRes.exitCode != 0 {
			t.Fatalf("self-update exit code = %d, want 0: stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
		}
		var selfOutcome selfUpdateOutcomeJSON
		if err := json.Unmarshal([]byte(selfRes.stdout), &selfOutcome); err != nil {
			t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfRes.stdout)
		}
		if selfOutcome.Action != "updated" {
			t.Fatalf("self-update action = %q, want updated", selfOutcome.Action)
		}
		selfHookLines := readHookMarker(t, selfMarker)

		upDest := placeEquivFixture(t, built, "up/bin")
		upMarker := filepath.Join(t.TempDir(), "up-hook.log")
		upEnv := append(equivEnv(t, assetSrv.URL, "1.0.0", "", ""), "FIXTURE_HOOK_MARKER="+upMarker)
		upRes := runEquivFixture(t, upDest, upEnv, "upgrade", "cover100", "--yes", "--format", "json")
		if upRes.exitCode != 0 {
			t.Fatalf("upgrade <self> exit code = %d, want 0: stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
		}
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
		}
		if len(doc.Targets) != 1 || doc.Targets[0].Action != "upgraded" {
			t.Fatalf("doc.Targets = %+v, want exactly one 'upgraded' target", doc.Targets)
		}
		upHookLines := readHookMarker(t, upMarker)

		if len(selfHookLines) != len(upHookLines) {
			t.Errorf("hook invocation count: self-update=%d upgrade<self>=%d, want equal", len(selfHookLines), len(upHookLines))
		}
		if len(selfHookLines) != 1 {
			t.Errorf("self-update hook invocations = %v, want exactly 1", selfHookLines)
		}
		if len(selfHookLines) > 0 && len(upHookLines) > 0 && selfHookLines[0] != upHookLines[0] {
			t.Errorf("hook-recorded action differs: self-update=%q upgrade<self>=%q", selfHookLines[0], upHookLines[0])
		}

		got, err := os.ReadFile(upDest)
		if err != nil || string(got) != "new binary content" {
			t.Errorf("upgrade <self> did not replace its own binary: content=%q err=%v", got, err)
		}
	})

	// task-22 review B2 + S4: a REAL --yes pair for an ALREADY-CURRENT
	// host proves the after-update hook fires exactly once on BOTH sides —
	// selfupdate.Config.UpdateAt's own runAfterUpdate skips it under
	// DryRun, so only a real, non-dry-run call (self-update's single call;
	// upgrade <self>'s dedicated second real call for AlreadyCurrent —
	// see cliinstall.ExecuteUpgrade's own doc comment) ever fires it.
	t.Run("already current runs the hook exactly once on both sides (--yes)", func(t *testing.T) {
		srv := equivReleaseServer(t, "v1.0.0")

		selfDest := placeEquivFixture(t, built, "self2/bin")
		selfMarker := filepath.Join(t.TempDir(), "self-hook.log")
		selfEnv := append(equivEnv(t, srv.URL, "1.0.0", "", ""), "FIXTURE_HOOK_MARKER="+selfMarker)
		selfRes := runEquivFixture(t, selfDest, selfEnv, "self-update", "--yes", "--format", "json")
		if selfRes.exitCode != 0 {
			t.Fatalf("self-update exit code = %d, want 0: stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
		}
		var selfOutcome selfUpdateOutcomeJSON
		if err := json.Unmarshal([]byte(selfRes.stdout), &selfOutcome); err != nil {
			t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfRes.stdout)
		}
		if selfOutcome.Action != "already_current" {
			t.Fatalf("self-update action = %q, want already_current", selfOutcome.Action)
		}
		selfHookLines := readHookMarker(t, selfMarker)

		upDest := placeEquivFixture(t, built, "up2/bin")
		upMarker := filepath.Join(t.TempDir(), "up-hook.log")
		upEnv := append(equivEnv(t, srv.URL, "1.0.0", "", ""), "FIXTURE_HOOK_MARKER="+upMarker)
		upRes := runEquivFixture(t, upDest, upEnv, "upgrade", "cover100", "--yes", "--format", "json")
		if upRes.exitCode != 0 {
			t.Fatalf("upgrade <self> exit code = %d, want 0: stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
		}
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
		}
		if len(doc.Targets) != 1 || doc.Targets[0].Action != "already_current" {
			t.Fatalf("doc.Targets = %+v, want exactly one 'already_current' target", doc.Targets)
		}
		upHookLines := readHookMarker(t, upMarker)

		if len(selfHookLines) != 1 {
			t.Errorf("self-update hook invocations = %v, want exactly 1", selfHookLines)
		}
		if len(upHookLines) != 1 {
			t.Errorf("upgrade <self> hook invocations = %v, want exactly 1 (task-22 review B2)", upHookLines)
		}
	})

	// task-22 third review S4: --check with a failed lookup, across every
	// install-method classification — not just ambiguous (already covered
	// above) — proving self-update and upgrade <self> both fail with the
	// SAME mapped exit code regardless of method, exactly the parity D1's
	// own reordering (lookup before classification) is meant to guarantee
	// structurally, not just for the one classification the original
	// review happened to find broken.
	t.Run("--check with a failed lookup fails identically per install method", func(t *testing.T) {
		failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		t.Cleanup(failing.Close)

		cases := []struct {
			name       string
			subdir     string
			marker     string
			executable string
		}{
			{"manual", "checkfail/manual/bin", "", ""},
			{"executable-managed", "checkfail/execmgr", "/checkfailexecmgr/", "1"},
			{"redirect-only", "checkfail/redirectmgr", "/checkfailredirectmgr/", ""},
			{"ambiguous", "checkfail/plain", "", ""},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				dest := placeEquivFixture(t, built, c.subdir)
				env := equivEnv(t, failing.URL, "1.0.0", c.marker, c.executable)

				selfRes := runEquivFixture(t, dest, env, "self-update", "--check", "--format", "json")
				if selfRes.exitCode != 4 {
					t.Fatalf("self-update --check exit code = %d, want 4 (release-lookup, per fixtureErrors): stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
				}

				upRes := runEquivFixture(t, dest, env, "upgrade", "cover100", "--check", "--format", "json")
				if upRes.exitCode != 4 {
					t.Fatalf("upgrade <self> --check exit code = %d, want 4 (same reason, same mapped code): stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
				}
				var doc upgradeDocForTest
				if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
					t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
				}
				if len(doc.Targets) != 1 || doc.Targets[0].Action != "failed" || doc.Targets[0].FailureKind != "release_lookup" {
					t.Errorf("doc.Targets = %+v, want exactly one failed/release_lookup target", doc.Targets)
				}
			})
		}
	})

	// task-22 third review S4: a REAL --yes run through an EXECUTABLE
	// manager (not merely --dry-run's own preview of one), proving
	// self-update and upgrade <self> both reach
	// ActionManagerExecuted/manager_executed AND both run the host's
	// after-update hook exactly once. selfupdate.Config.UpdateAt's managed
	// path only calls AfterUpdate once Options.VerifyManaged has found and
	// probed a real "cover100" executable on PATH reporting the expected
	// post-upgrade version (selfupdate/cliui.VerifyManagedBinary) — a fake
	// RunManaged step alone (e.g. running `go version`) never leaves such a
	// binary behind, so this uses buildEquivProbe's own fixture as BOTH the
	// manager's executable-upgrade step (a harmless no-op) and, placed on
	// PATH under the fixture's exact BinaryName, the thing VerifyManaged
	// finds and probes with --version.
	t.Run("executable manager real run (--yes)", func(t *testing.T) {
		probe := buildEquivProbe(t)
		srv := equivReleaseServer(t, "v2.0.0")

		selfDest := placeEquivFixture(t, built, "selfmgr/mgr")
		selfProbeDest := placeEquivFixture(t, probe, "selfmgr/verify")
		selfMarker := filepath.Join(t.TempDir(), "self-hook.log")
		selfEnv := append(equivEnv(t, srv.URL, "1.0.0", "/selfmgr/", "1", filepath.Dir(selfProbeDest)),
			"FIXTURE_MANAGER_EXECUTABLE_PATH="+probe,
			"FIXTURE_HOOK_MARKER="+selfMarker,
			"PROBE_VERSION=2.0.0",
		)
		selfRes := runEquivFixture(t, selfDest, selfEnv, "self-update", "--yes", "--format", "json")
		if selfRes.exitCode != 0 {
			t.Fatalf("self-update exit code = %d, want 0: stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
		}
		var selfOutcome selfUpdateOutcomeJSON
		if err := json.Unmarshal([]byte(selfRes.stdout), &selfOutcome); err != nil {
			t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfRes.stdout)
		}
		if selfOutcome.Action != "manager_executed" {
			t.Fatalf("self-update action = %q, want manager_executed", selfOutcome.Action)
		}
		selfHookLines := readHookMarker(t, selfMarker)

		upDest := placeEquivFixture(t, built, "upmgr/mgr")
		upProbeDest := placeEquivFixture(t, probe, "upmgr/verify")
		upMarker := filepath.Join(t.TempDir(), "up-hook.log")
		upEnv := append(equivEnv(t, srv.URL, "1.0.0", "/upmgr/", "1", filepath.Dir(upProbeDest)),
			"FIXTURE_MANAGER_EXECUTABLE_PATH="+probe,
			"FIXTURE_HOOK_MARKER="+upMarker,
			"PROBE_VERSION=2.0.0",
		)
		upRes := runEquivFixture(t, upDest, upEnv, "upgrade", "cover100", "--yes", "--format", "json")
		if upRes.exitCode != 0 {
			t.Fatalf("upgrade <self> exit code = %d, want 0: stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
		}
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
		}
		if len(doc.Targets) != 1 || doc.Targets[0].Action != "manager_executed" {
			t.Fatalf("doc.Targets = %+v, want exactly one manager_executed target", doc.Targets)
		}
		upHookLines := readHookMarker(t, upMarker)

		if len(selfHookLines) != 1 {
			t.Errorf("self-update hook invocations = %v, want exactly 1", selfHookLines)
		}
		if len(upHookLines) != 1 {
			t.Errorf("upgrade <self> hook invocations = %v, want exactly 1", upHookLines)
		}
	})

	// task-22 third review S4: an explicitly-named dev (non-release) build
	// — the one case cli-install#req:upgrade-skips-non-release-builds
	// offers instead of skipping. self-update has no such concept at all
	// (a single-target tool never "skips" itself); its own Undetermined
	// verdict for a "dev" CurrentVersion is exactly what upgrade <self>
	// cover100 (explicit naming required — a dev build is never offered
	// implicitly) must also reach — including BOTH commands' own --check
	// exit code: selfupdate/cobracmd.runCheck calls opts.Errors.
	// UpdateAvailable for any verdict other than UpToDate/Ahead, which
	// includes Undetermined (its own doc comment: "covers both selfupdate.
	// UpdateAvailable and selfupdate.Undetermined"), and cliinstall/
	// cobracmd's own runUpgradeReport mirrors that exactly (its Undetermined
	// arm feeding UpgradeErrorMapper.UpgradesAvailable) — so fixtureErrors
	// maps both to exit 2, not 0.
	t.Run("explicitly named dev build", func(t *testing.T) {
		srv := equivReleaseServer(t, "v1.0.0")
		dest := placeEquivFixture(t, built, "devbuild/bin")
		env := equivEnv(t, srv.URL, "dev", "", "")

		selfRes := runEquivFixture(t, dest, env, "self-update", "--check", "--format", "json")
		if selfRes.exitCode != 2 {
			t.Fatalf("self-update --check exit code = %d, want 2 (undetermined maps to UpdateAvailable, per fixtureErrors): stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
		}
		var selfCheck struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal([]byte(selfRes.stdout), &selfCheck); err != nil {
			t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfRes.stdout)
		}
		if selfCheck.Verdict != "undetermined" {
			t.Fatalf("self-update verdict = %q, want undetermined", selfCheck.Verdict)
		}

		upRes := runEquivFixture(t, dest, env, "upgrade", "cover100", "--check", "--format", "json")
		if upRes.exitCode != 2 {
			t.Fatalf("upgrade <self> --check exit code = %d, want 2 (same reason, same mapped code): stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
		}
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
		}
		if len(doc.Targets) != 1 || doc.Targets[0].Verdict != "undetermined" {
			t.Fatalf("doc.Targets = %+v, want exactly one undetermined target", doc.Targets)
		}
		if !doc.Targets[0].NonReleaseBuild {
			t.Errorf("NonReleaseBuild = false, want true for an explicitly-named dev build")
		}

		// Both sides also agree once actually offered (--dry-run --yes):
		// self-update's own Undetermined path still proceeds to plan a
		// replacement (there is no "current" to already equal), and so
		// does upgrade <self> for the same explicitly-named non-release
		// build (cli-install#req:upgrade-skips-non-release-builds).
		selfDry := runEquivFixture(t, dest, env, "self-update", "--dry-run", "--yes", "--format", "json")
		if selfDry.exitCode != 0 {
			t.Fatalf("self-update --dry-run exit code = %d, want 0: stdout=%s stderr=%s", selfDry.exitCode, selfDry.stdout, selfDry.stderr)
		}
		var selfOutcome selfUpdateOutcomeJSON
		if err := json.Unmarshal([]byte(selfDry.stdout), &selfOutcome); err != nil {
			t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfDry.stdout)
		}
		if selfOutcome.Action != "planned" {
			t.Errorf("self-update action = %q, want planned", selfOutcome.Action)
		}

		upDry := runEquivFixture(t, dest, env, "upgrade", "cover100", "--dry-run", "--yes", "--format", "json")
		if upDry.exitCode != 0 {
			t.Fatalf("upgrade <self> --dry-run exit code = %d, want 0: stdout=%s stderr=%s", upDry.exitCode, upDry.stdout, upDry.stderr)
		}
		var upDoc upgradeDocForTest
		if err := json.Unmarshal([]byte(upDry.stdout), &upDoc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upDry.stdout)
		}
		if len(upDoc.Targets) != 1 || upDoc.Targets[0].Action != "dry_run" {
			t.Errorf("doc.Targets = %+v, want exactly one dry_run target", upDoc.Targets)
		}
	})
}

// readHookMarker reads FIXTURE_HOOK_MARKER's file and returns its non-empty
// lines — empty (not an error) when the hook was never invoked, since the
// fixture never creates the file until its first write.
func readHookMarker(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read hook marker %s: %v", path, err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// runScenario places built at subdir, runs both commands with the given
// current version against a release server rooted at endpoint, and asserts
// they agree on Current/Latest and on the expected action pair.
func runScenario(t *testing.T, built, subdir, endpoint, current, marker, executable string, wantSelfAction, wantUpgradeAction string) {
	t.Helper()
	dest := placeEquivFixture(t, built, subdir)
	env := equivEnv(t, endpoint, current, marker, executable)

	selfRes := runEquivFixture(t, dest, env, "self-update", "--dry-run", "--yes", "--format", "json")
	if selfRes.exitCode != 0 {
		t.Fatalf("self-update exit code = %d, want 0: stdout=%s stderr=%s", selfRes.exitCode, selfRes.stdout, selfRes.stderr)
	}
	var selfOutcome selfUpdateOutcomeJSON
	if err := json.Unmarshal([]byte(selfRes.stdout), &selfOutcome); err != nil {
		t.Fatalf("decode self-update JSON: %v (raw %s)", err, selfRes.stdout)
	}
	if selfOutcome.Action != wantSelfAction {
		t.Errorf("self-update action = %q, want %q", selfOutcome.Action, wantSelfAction)
	}

	upRes := runEquivFixture(t, dest, env, "upgrade", "cover100", "--dry-run", "--yes", "--format", "json")
	if upRes.exitCode != 0 {
		t.Fatalf("upgrade <self> exit code = %d, want 0: stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
	}
	var doc upgradeDocForTest
	if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
		t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
	}
	if len(doc.Targets) != 1 {
		t.Fatalf("doc.Targets = %+v, want exactly one (the host)", doc.Targets)
	}
	target := doc.Targets[0]
	if target.Action != wantUpgradeAction {
		t.Errorf("upgrade <self> action = %q, want %q", target.Action, wantUpgradeAction)
	}

	// The facts BOTH sides saw must agree exactly — same running binary,
	// same release server, same current version — regardless of each
	// command's own outcome vocabulary.
	if selfOutcome.Current != target.Current {
		t.Errorf("current mismatch: self-update=%q upgrade<self>=%q", selfOutcome.Current, target.Current)
	}
	selfLatest := selfOutcome.Latest
	if selfLatest == "" {
		selfLatest = selfOutcome.Target // ActionPlanned/ActionRedirected carry it as target/latest depending on branch
	}
	if selfLatest != "" && target.Latest != "" && selfLatest != target.Latest {
		t.Errorf("latest mismatch: self-update=%q upgrade<self>=%q", selfLatest, target.Latest)
	}
	if selfOutcome.Manager != "" && selfOutcome.Manager != target.Manager {
		t.Errorf("manager mismatch: self-update=%q upgrade<self>=%q", selfOutcome.Manager, target.Manager)
	}
	if selfOutcome.Command != "" && selfOutcome.Command != target.Command {
		t.Errorf("command mismatch: self-update=%q upgrade<self>=%q", selfOutcome.Command, target.Command)
	}
}
