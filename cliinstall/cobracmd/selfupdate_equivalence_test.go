package cobracmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	out := filepath.Join(t.TempDir(), "built-"+equivBinaryName())
	cmd := exec.Command("go", "build", "-o", out, "./testdata/selfupdateequiv")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build testdata/selfupdateequiv fixture: %v\n%s", err, output)
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
	path := os.Getenv("PATH")
	for _, d := range extraPathDirs {
		path = d + string(os.PathListSeparator) + path
	}
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
	Targets []struct {
		Name         string   `json:"name"`
		Action       string   `json:"action"`
		Current      string   `json:"current"`
		Latest       string   `json:"latest"`
		Verdict      string   `json:"verdict"`
		Manager      string   `json:"manager"`
		Command      string   `json:"command"`
		ResolvedPath string   `json:"resolved_path"`
		OtherPaths   []string `json:"other_paths"`
		Warnings     []string `json:"warnings"`
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

	// Ambiguous is a DOCUMENTED DIVERGENCE, not an equivalence: self-update's
	// UpdateAt returns a hard *selfupdate.Failure{Kind: KindAmbiguous} for
	// ANY ambiguous classification (self-update#req:ambiguous-safe-default),
	// so `self-update --yes` on an ambiguous host fails the process. cliinstall's
	// PlanUpgrade instead resolves ambiguous+update-available straight to the
	// terminal, non-failing UpgradeOutcomeRefused BEFORE ever calling UpdateAt
	// at all (cli-install#req:upgrade-per-target-policy's own table: "installed,
	// ambiguous → refused... with manual-update guidance" is a descriptive
	// per-target outcome, not a batch failure — see UpgradeBatchResult.Failed's
	// own doc comment: "a refused... target is a descriptive state, not
	// something that went wrong this run"). `upgrade <self>` therefore exits 0
	// where `self-update` exits non-zero for the IDENTICAL install-method
	// classification and the IDENTICAL underlying version facts — verified
	// here rather than silently assumed equivalent, and worth flagging to
	// task-22's coordinator: cli-install#ac:self-update-equals-upgrade-self's
	// literal text ("produces the same... failure kind") is not met for this
	// one classification, by task-21's own already-landed design, which this
	// task's file scope (cliinstall/cliui, cliinstall/cobracmd — never
	// cliinstall/upgrade.go itself) does not include changing.
	t.Run("ambiguous (documented divergence, not equivalence)", func(t *testing.T) {
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
		if upRes.exitCode != 0 {
			t.Fatalf("upgrade <self> exit code = %d, want 0 (a refused target is descriptive, not a failure): stdout=%s stderr=%s", upRes.exitCode, upRes.stdout, upRes.stderr)
		}
		var doc upgradeDocForTest
		if err := json.Unmarshal([]byte(upRes.stdout), &doc); err != nil {
			t.Fatalf("decode upgrade JSON: %v (raw %s)", err, upRes.stdout)
		}
		if len(doc.Targets) != 1 || doc.Targets[0].Action != "refused" {
			t.Fatalf("doc.Targets = %+v, want exactly one 'refused' target", doc.Targets)
		}
		if doc.Targets[0].Current != "1.0.0" || doc.Targets[0].Latest != "1.1.0" {
			t.Errorf("upgrade <self> Current/Latest = %q/%q, want 1.0.0/1.1.0 (same facts self-update saw)", doc.Targets[0].Current, doc.Targets[0].Latest)
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
