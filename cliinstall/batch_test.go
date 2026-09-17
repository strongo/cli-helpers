package cliinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/strongo/buildinfo"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- shared batch test fixtures ------------------------------------------

// fakeAbsDir returns an absolute directory on the REAL host OS this test
// process is actually running on, built from elems — never touching real
// disk, since it only ever backs a fake Env.PathDirs/HostDir func (a test
// that touches real disk uses t.TempDir() directly instead, which is
// already host-OS-native).
//
// searchDirs' own filepath.IsAbs filter runs under the actual runtime.GOOS
// the test process executes on, NEVER under a test's own goosName override
// (the package-level var some tests force to a different value specifically
// to exercise that OS's naming policy in isolation — see e.g.
// TestProbe_WindowsExecutableSuffix). A hardcoded POSIX literal like
// "/usr/bin" is not absolute on Windows (filepath.IsAbs requires a drive
// letter or UNC root there), so on a REAL Windows test run searchDirs
// silently dropped every such fake PATH/HostDir entry, and every target
// behind it went unseen — this is what made most of this package's own
// test suite report "not installed" (or panic on the resulting nil result)
// the first time task-22's S5 ran it on real Windows CI.
func fakeAbsDir(elems ...string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(`C:\fakeroot`, filepath.Join(elems...))
	}
	return "/" + filepath.Join(elems...)
}

// fakeAbsExe joins dir and name the same way probeOne itself locates an
// executable: the ".exe" suffix decision follows goosName (the package's
// own test seam over runtime.GOOS — see status.go's own doc comment on
// it), exactly like probeOne's own filename construction, while the join
// itself uses the REAL stdlib filepath.Join, exactly like probeOne's own
// candidate construction (which is never goosName-parameterized — see
// fakeAbsDir's own doc comment for why that must track the real host
// GOOS). Most callers never override goosName, so the two coincide; a
// caller that does (to exercise a specific OS's naming policy in
// isolation, e.g. an ".exe" suffix test running for real on Linux CI)
// still gets exactly what probeOne would search for.
func fakeAbsExe(dir, name string) string {
	if goosName == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

// batchEnv builds an InstallEnv from plain maps/funcs, mirroring status_
// test.go's fakeEnv but for the extra install-only dependencies
// (cli-install#req:no-network-in-tests).
func batchEnv(pathDirs []string, hostDir string, executables map[string]bool, run func(context.Context, string, []string) ([]byte, error), runManaged selfupdate.ManagedCommandRunner) InstallEnv {
	return InstallEnv{
		Env: Env{
			PathDirs:     func() []string { return pathDirs },
			HostDir:      func() (string, error) { return hostDir, nil },
			IsExecutable: func(p string) bool { return executables[p] },
			EvalSymlinks: func(p string) (string, error) { return p, nil },
			Run:          run,
		},
		UserHomeDir: func() (string, error) { return "/home/alex", nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    func(string, os.FileMode) error { return nil },
		RunManaged:  runManaged,
	}
}

// multiJSONRun answers "version --json" for any probed path by looking up
// that path's base filename (with any platform executable suffix trimmed
// — probeOne appends ".exe" on windows, but versions' own keys are the
// bare catalog id, e.g. "ovdb" not "ovdb.exe") in versions, naming itself
// after that base — so one Run func can back several distinct targets
// probed at different paths in the same test.
func multiJSONRun(versions map[string]string) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, path string, args []string) ([]byte, error) {
		if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
			base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if v, ok := versions[base]; ok {
				b, _ := json.Marshal(buildinfo.VersionJSON{Name: base, Version: v})
				return b, nil
			}
		}
		return nil, errors.New("unrecognized")
	}
}

func noRunManaged(context.Context, string, []string) error {
	return errors.New("RunManaged must not be called")
}

// --- Plan ------------------------------------------------------------------

func TestPlan_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Plan did not panic for a host id absent from the catalog")
		}
	}()
	_, _ = Plan(context.Background(), nil, Options{HostID: "nosuchhost"})
}

// S1: an unknown name refuses the WHOLE batch, before any valid target is
// even probed — never a per-target failure that lets the rest proceed.
func TestPlan_UnknownNameRefusesWholeBatchBeforeProbing(t *testing.T) {
	probed := false
	run := func(context.Context, string, []string) ([]byte, error) {
		probed = true
		return nil, errors.New("must not be called")
	}
	env := batchEnv(nil, "/usr/bin", nil, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Plan(context.Background(), []string{"nosuchcli", "ovdb"}, opts)
	if err == nil {
		t.Fatal("Plan error = nil, want a batch-level unknown-target failure")
	}
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v, want KindUnknownTarget", selfupdate.KindOf(err))
	}
	if !strings.Contains(err.Error(), "nosuchcli") {
		t.Errorf("error %q does not name the unknown target", err.Error())
	}
	if len(result.Results) != 0 {
		t.Errorf("result.Results = %v, want empty: nothing should have been probed", result.Results)
	}
	if probed {
		t.Error("a valid target's probe ran despite an unknown name in the same batch")
	}
}

func TestPlan_MultipleUnknownNamesAllListed(t *testing.T) {
	env := batchEnv(nil, "/usr/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, nil }, noRunManaged)
	opts := Options{HostID: "datatug", Env: env}

	_, err := Plan(context.Background(), []string{"nosuch1", "nosuch2"}, opts)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"nosuch1", "nosuch2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestPlan_HostDirErrorTreatedAsEmpty(t *testing.T) {
	// opts.Env.HostDir() failing (e.g. os.Executable() itself errored)
	// must not be fatal to the whole batch — it is treated as "no host
	// directory," the same as searchDirs itself already does for status
	// probing, and planning falls through (empty hostDir classifies
	// Ambiguous) to the per-user bin directory check. PathDirs
	// deliberately omits that directory so planning fails deterministically
	// — with no host dir, no PATH entry and no --dir, there is nothing left
	// to resolve, which is exactly what this test needs: proof the missing
	// HostDir() didn't panic or otherwise abort the batch, without any
	// network call.
	env := batchEnv(nil, "", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	env.HostDir = func() (string, error) { return "", errors.New("os.Executable failed") }
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindNoInstallDir {
		t.Errorf("Results[0] = %+v, want KindNoInstallDir", r)
	}
}

func TestPlan_AlreadyInstalledIsNotReinstalled(t *testing.T) {
	bin1 := fakeAbsDir("bin1")
	executables := map[string]bool{fakeAbsExe(bin1, "ovdb"): true}
	run := multiJSONRun(map[string]string{"ovdb": "1.2.3"})
	env := batchEnv([]string{bin1}, fakeAbsDir("usr", "bin"), executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeAlreadyInstalled || r.Version != "1.2.3" || r.UpdateHint != "ovdb self-update" {
		t.Errorf("Results[0] = %+v", r)
	}
}

func TestPlan_DedupesNames(t *testing.T) {
	executables := map[string]bool{"/bin1/ovdb": true}
	run := multiJSONRun(map[string]string{"ovdb": "1.2.3"})
	env := batchEnv([]string{"/bin1"}, "/usr/bin", executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env}

	result, err := Plan(context.Background(), []string{"ovdb", "ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1 (de-duplicated)", len(result.Results))
	}
}

func TestPlan_UnrecognizedAtDestinationFails(t *testing.T) {
	hostDir := fakeAbsDir("home", "alex", "go", "bin") // Manual, not denylisted
	// installFilePath is the SAME function Plan itself uses to build the
	// planned destination path — computing the expected value through it
	// (rather than a hand-typed parallel literal) keeps this assertion
	// correct under any goosName/host-OS combination, including the
	// ".exe" suffix windows adds. fakeAbsExe instead matches what
	// probeOne's own real filepath.Join search looks for (see its own
	// doc comment for why the two joiners can differ) — here, with
	// goosName left at its default, both happen to agree, but the
	// executables map key is built the way probeOne actually searches.
	destPath := installFilePath(goosName, hostDir, "ovdb")
	locatedPath := fakeAbsExe(hostDir, "ovdb")
	executables := map[string]bool{locatedPath: true}
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("somethingelse 9.9.9 (abc) 2026-01-01"), nil
	}
	env := batchEnv(nil, hostDir, executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindDestinationExists {
		t.Fatalf("Results[0] = %+v, want KindDestinationExists", r)
	}
	if r.Failure.Path != destPath {
		t.Errorf("Failure.Path = %q, want %q", r.Failure.Path, destPath)
	}
}

// S6: the collision check must catch a destination path that names the
// SAME file as a located copy once case-folded, on a case-insensitive-by-
// default platform (Windows/macOS), not just a byte-exact match — the real
// scenario is a case-insensitive filesystem where the host's own directory
// string and the literal PATH entry a copy was found under differ only in
// letter case (e.g. "/Users/alex/..." vs "/Users/Alex/...").
func TestPlan_UnrecognizedAtDestinationFails_CaseVariant(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "darwin"

	hostDir := fakeAbsDir("Users", "alex", "go", "bin")   // Manual, not denylisted; planning builds destPath from this
	pathEntry := fakeAbsDir("Users", "Alex", "go", "bin") // same directory, different case, found on PATH
	// fakeAbsExe (not installFilePath): this must match what probeOne's
	// own real filepath.Join search actually looks for, not Plan's
	// separately-implemented destination-path joiner (see fakeAbsExe's
	// own doc comment) — the test only asserts Outcome/Kind below, not an
	// exact Failure.Path string, so the two joiners' possibly-differing
	// separator styles never need to agree here.
	locatedPath := fakeAbsExe(pathEntry, "ovdb")
	executables := map[string]bool{locatedPath: true}
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("somethingelse 9.9.9 (abc) 2026-01-01"), nil
	}
	env := batchEnv([]string{pathEntry}, hostDir, executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindDestinationExists {
		t.Fatalf("Results[0] = %+v, want KindDestinationExists (case-insensitive match on darwin)", r)
	}
}

func TestPlan_UnrecognizedElsewhereWarns(t *testing.T) {
	// The unrecognized copy is on PATH at /bin1, earlier than the planned
	// destination /home/alex/go/bin (not on PATH at all) — REQ:
	// unrecognized-copy-not-trusted's shadowing case.
	hostDir := fakeAbsDir("home", "alex", "go", "bin")
	bin1 := fakeAbsDir("bin1")
	unrecognizedPath := fakeAbsExe(bin1, "ovdb")
	executables := map[string]bool{unrecognizedPath: true}
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("somethingelse 9.9.9 (abc) 2026-01-01"), nil
	}
	env := batchEnv([]string{bin1}, hostDir, executables, run, noRunManaged)
	srv := newReleaseServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, nil)
	opts := Options{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDryRun {
		t.Fatalf("Outcome = %v, want OutcomeDryRun (a plan, still pending); failure=%v", r.Outcome, r.Failure)
	}
	found := false
	for _, w := range r.Warnings {
		if w != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want the shadowing warning", r.Warnings)
	}
}

func TestPlan_HomebrewNeedsNoNetwork(t *testing.T) {
	// Homebrew casks are POSIX-only (wb/ovdb's own catalog entries declare
	// CaskOS: darwin/linux, never windows), so caskSupportsOS only chooses
	// MethodHomebrew there — pin goosName so this Homebrew-policy fixture
	// exercises that regardless of the REAL host OS running the test (same
	// pattern as TestPlanMethod_DirGivenAllowed).
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "darwin"

	hostDir := fakeAbsDir("opt", "homebrew", "Caskroom", "wb", "1.0.0")
	env := batchEnv(nil, hostDir, nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") }, noRunManaged)
	opts := Options{HostID: "wb", Env: env}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDryRun || r.Method != MethodHomebrew {
		t.Fatalf("Results[0] = %+v", r)
	}
	if len(r.CaskArgv) == 0 {
		t.Errorf("CaskArgv = %v, want the planned brew argv", r.CaskArgv)
	}
}

// B1: a direct target's plan carries the exact resolved Tag/Version/
// AssetURL, resolved through PlanInstall — never a guessed tag.
func TestPlan_DirectResolvesReleaseOnce(t *testing.T) {
	srv := newReleaseServer(t, `[{"tag_name":"v1.2.3","prerelease":false,"draft":false}]`, nil)
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("not installed") }, noRunManaged)
	calls := 0
	opts := Options{
		HostID: "datatug", Env: env,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			calls++
			cfg.ReleasesAPIURL = srv.URL + "/releases"
			return cfg
		},
	}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDryRun || r.Version != "1.2.3" || r.Tag != "v1.2.3" {
		t.Fatalf("Results[0] = %+v", r)
	}
	if r.AssetURL == "" {
		t.Error("AssetURL is empty, want the resolved asset URL")
	}
	if calls != 1 {
		t.Errorf("ConfigureRelease called %d times, want exactly 1 (resolve once)", calls)
	}
}

func TestPlan_DirectUnsupportedPlatform(t *testing.T) {
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("not installed") }, noRunManaged)
	opts := Options{
		HostID: "datatug", Env: env,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			cfg.SupportedPlatforms = []selfupdate.Platform{{GOOS: "plan9", GOARCH: "amd64"}}
			cfg.ReleasesAPIURL = "http://127.0.0.1:1/releases" // never dialed
			return cfg
		},
	}

	result, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindUnsupportedPlatform {
		t.Fatalf("Results[0] = %+v, want KindUnsupportedPlatform", r)
	}
}

// --- Execute -----------------------------------------------------------

func TestExecute_ConfirmationGate_NoCallbackConfiguredRefuses(t *testing.T) {
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	srv := newReleaseServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, nil)
	opts := Options{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)}

	plan, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	result, err := Execute(context.Background(), plan, opts) // Yes false, Confirm nil
	if err == nil {
		t.Fatal("Execute error = nil, want a non-interactive refusal")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
	// S2: the refusal must NOT be an empty BatchResult — every target from
	// the plan is still reported, now as a failure.
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1 (populated even on refusal)", len(result.Results))
	}
	r := result.Results[0]
	if r.Target != "ovdb" || r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindNonInteractive {
		t.Errorf("Results[0] = %+v", r)
	}
	if !result.Failed() {
		t.Error("BatchResult.Failed() = false, want true")
	}
}

func TestExecute_ConfirmationGate_CallbackErrorPropagates(t *testing.T) {
	sentinel := &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errors.New("no tty")}
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	srv := newReleaseServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, nil)
	opts := Options{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)}
	plan, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	opts.Confirm = func([]Result) (bool, error) { return false, sentinel }
	result, err := Execute(context.Background(), plan, opts)
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("Execute error = %v, want the Confirm callback's own error propagated", err)
	}
	if len(result.Results) != 1 || result.Results[0].Failure != sentinel {
		t.Errorf("Results = %+v, want the sentinel failure carried through", result.Results)
	}
}

// A Confirm callback may return an error that is NOT already a
// *selfupdate.Failure (a plain error). Execute must still wrap it as a
// KindNonInteractive failure per target, rather than losing its type.
func TestExecute_ConfirmationGate_PlainCallbackErrorIsWrapped(t *testing.T) {
	plain := errors.New("stdin closed")
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	srv := newReleaseServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, nil)
	opts := Options{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)}
	plan, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	opts.Confirm = func([]Result) (bool, error) { return false, plain }
	result, err := Execute(context.Background(), plan, opts)
	if !errors.Is(err, plain) {
		t.Errorf("Execute error = %v, want it to wrap the plain callback error", err)
	}
	if len(result.Results) != 1 || result.Results[0].Failure == nil || result.Results[0].Failure.Kind != selfupdate.KindNonInteractive {
		t.Errorf("Results = %+v, want a wrapped KindNonInteractive failure", result.Results)
	}
}

func TestExecute_ConfirmationGate_NamesOnlyPendingTargets(t *testing.T) {
	// "ovdb" is already installed (excluded from confirmation); "datatug"
	// is not (included) — REQ: confirmation-gate: "Targets that are
	// already installed... are excluded from the question."
	bin1 := fakeAbsDir("bin1")
	executables := map[string]bool{fakeAbsExe(bin1, "ovdb"): true}
	run := multiJSONRun(map[string]string{"ovdb": "1.0.0"})
	env := batchEnv([]string{bin1}, fakeAbsDir("home", "alex", "go", "bin"), executables, run, noRunManaged)
	srv := newReleaseServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, nil)
	opts := Options{HostID: "wb", Env: env, ConfigureRelease: configureReleaseFromServer(srv)}
	plan, err := Plan(context.Background(), []string{"ovdb", "datatug"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	var confirmedNames []string
	opts.Confirm = func(planned []Result) (bool, error) {
		for _, p := range planned {
			confirmedNames = append(confirmedNames, p.Target)
		}
		return false, nil
	}
	_, err = Execute(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if len(confirmedNames) != 1 || confirmedNames[0] != "datatug" {
		t.Errorf("Confirm was asked about %v, want only [datatug]", confirmedNames)
	}
}

func TestExecute_DeclinedKeepsStatusAndDestination(t *testing.T) {
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("not installed") }, noRunManaged)
	opts := Options{
		HostID: "datatug", Env: env,
		ConfigureRelease: func(_ Entry, cfg selfupdate.Config) selfupdate.Config {
			cfg.ReleasesAPIURL = newReleaseServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`, nil).URL + "/releases"
			return cfg
		},
	}
	plan, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	opts.Confirm = func([]Result) (bool, error) { return false, nil }
	result, err := Execute(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDeclined {
		t.Fatalf("Outcome = %v, want OutcomeDeclined", r.Outcome)
	}
	// M10: a declined result must still carry Status and Destination.
	if r.Destination == "" {
		t.Error("Destination is empty on a declined result")
	}
}

func TestExecute_HomebrewPrintOnlyRedirectsWithoutRunning(t *testing.T) {
	// Same reasoning as TestPlan_HomebrewNeedsNoNetwork: pin goosName so
	// this Homebrew-policy fixture works regardless of the real host OS.
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "darwin"

	hostDir := fakeAbsDir("opt", "homebrew", "Caskroom", "wb", "1.0.0")
	env := batchEnv(nil, hostDir, nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") }, noRunManaged)
	opts := Options{HostID: "wb", Env: env}
	plan, err := Plan(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	opts.Yes = true
	opts.HomebrewPrintOnly = true
	result, err := Execute(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeRedirected {
		t.Fatalf("Outcome = %v, want OutcomeRedirected", r.Outcome)
	}
}

// TestExecute_AcceptedBatchInstallsIndependently mirrors cli-install#ac:
// batch-reports-every-target: two Homebrew-classified targets, one whose
// managed command succeeds and one whose fails — both get a result, in
// order, and the earlier failure never stops the later target.
func TestExecute_AcceptedBatchInstallsIndependently(t *testing.T) {
	// Same reasoning as TestPlan_HomebrewNeedsNoNetwork: pin goosName so
	// this Homebrew-policy fixture works regardless of the real host OS.
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "darwin"

	executables := map[string]bool{}
	homebrewBin := fakeAbsDir("opt", "homebrew", "bin")
	run := multiJSONRun(map[string]string{"ovdb": "2.0.0"})
	runManaged := func(_ context.Context, exe string, args []string) error {
		token := args[len(args)-1]
		if token == "openvaultdb/tap/ovdb" {
			executables[fakeAbsExe(homebrewBin, "ovdb")] = true
			return nil
		}
		return errors.New("brew: cask not found")
	}
	env := batchEnv([]string{homebrewBin}, fakeAbsDir("opt", "homebrew", "Caskroom", "wb", "1.0.0"), executables, run, runManaged)
	opts := Options{HostID: "wb", Env: env}
	plan, err := Plan(context.Background(), []string{"ovdb", "datatug"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	opts.Yes = true
	result, err := Execute(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(result.Results))
	}
	ovdbResult := result.Results[0]
	if ovdbResult.Target != "ovdb" || ovdbResult.Outcome != OutcomeInstalled || ovdbResult.Version != "2.0.0" {
		t.Errorf("Results[0] = %+v", ovdbResult)
	}
	datatugResult := result.Results[1]
	if datatugResult.Target != "datatug" || datatugResult.Outcome != OutcomeFailed || datatugResult.Failure == nil || datatugResult.Failure.Kind != selfupdate.KindManagedCommand {
		t.Errorf("Results[1] = %+v", datatugResult)
	}
	if !result.Failed() {
		t.Error("BatchResult.Failed() = false, want true (datatug failed)")
	}
}

// TestExecute_DirectBatchOneFailsOthersSucceed follows the plan's own
// worked example: `install a b c` where b fails to download, run with
// --yes — a and c install, b's typed failure is reported, and the overall
// command fails.
func TestExecute_DirectBatchOneFailsOthersSucceed(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	binContent := []byte("payload")
	// ovdb's real catalog entry overrides ChecksumsName to a flat
	// "checksums.txt" (see catalog_ovdb.go) and leaves AssetName at the
	// shared GoReleaser-shaped default — Plan() looks the target up in
	// the real compiled catalog, so this fixture must match that entry
	// exactly, not a hand-picked naming.
	archive, ext := makeArchive(t, "ovdb", binContent)
	okAsset := fmt.Sprintf("ovdb_%s_%s_%s.%s", version, runtime.GOOS, runtime.GOARCH, ext)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), okAsset)

	okServer := newReleaseServer(t, `[{"tag_name":"`+tag+`","prerelease":false,"draft":false}]`, map[string][]byte{
		"/" + tag + "/" + okAsset:    archive,
		"/" + tag + "/checksums.txt": []byte(checksums),
	})
	failServer := newReleaseServer(t, `[{"tag_name":"v0.1.0","prerelease":false,"draft":false}]`, nil) // no asset published: download fails

	destDir := t.TempDir()
	run := multiJSONRun(map[string]string{"ovdb": version})
	env := batchEnv([]string{destDir}, fakeAbsDir("nonexistent-host-dir"), nil, run, noRunManaged)
	// IsExecutable must reflect a real install landing in destDir so the
	// post-install verification probe (and the batch's own PATH-based
	// executable check) sees it.
	env.IsExecutable = func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	opts := Options{
		HostID: "datatug", Env: env, Dir: destDir,
		ConfigureRelease: func(target Entry, cfg selfupdate.Config) selfupdate.Config {
			srv := okServer
			if target.ID == "ingitdb" {
				srv = failServer
			}
			cfg.ReleasesAPIURL = srv.URL + "/releases"
			cfg.DownloadURL = perTagDownloadURL(srv.URL)
			cfg.HTTPClient = srv.Client()
			return cfg
		},
	}

	plan, err := Plan(context.Background(), []string{"ovdb", "ingitdb"}, opts)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	opts.Yes = true
	result, err := Execute(context.Background(), plan, opts)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(result.Results))
	}
	if result.Results[0].Target != "ovdb" || result.Results[0].Outcome != OutcomeInstalled {
		t.Errorf("Results[0] = %+v", result.Results[0])
	}
	if result.Results[1].Target != "ingitdb" || result.Results[1].Outcome != OutcomeFailed || result.Results[1].Failure == nil {
		t.Errorf("Results[1] = %+v", result.Results[1])
	}
	if !result.Failed() {
		t.Error("BatchResult.Failed() = false, want true")
	}

	// S3: BatchFailure exposes every failed target, not just the first.
	batchErr := result.Failure()
	var bf *BatchFailure
	if !errors.As(batchErr, &bf) {
		t.Fatalf("result.Failure() = %v, want a *BatchFailure", batchErr)
	}
	if len(bf.Failures) != 1 || bf.Failures[0] != result.Results[1].Failure {
		t.Errorf("BatchFailure.Failures = %v, want exactly ingitdb's own Failure", bf.Failures)
	}
}

// --- Install (Plan + confirm + Execute convenience) ------------------------

func TestInstall_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Install did not panic for a host id absent from the catalog")
		}
	}()
	_, _ = Install(context.Background(), nil, Options{HostID: "nosuchhost"})
}

func TestInstall_DryRunNeverCallsExecute(t *testing.T) {
	// Same reasoning as TestPlan_HomebrewNeedsNoNetwork: pin goosName so
	// this Homebrew-policy fixture works regardless of the real host OS.
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "darwin"

	hostDir := fakeAbsDir("opt", "homebrew", "Caskroom", "wb", "1.0.0")
	env := batchEnv(nil, hostDir, nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") }, noRunManaged)
	opts := Options{HostID: "wb", Env: env, DryRun: true} // no Confirm set: panics if Execute is ever reached

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDryRun || r.Method != MethodHomebrew {
		t.Fatalf("Results[0] = %+v", r)
	}
}

func TestInstall_UnknownNameNeverReachesExecute(t *testing.T) {
	env := batchEnv(nil, "/usr/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, nil }, noRunManaged)
	opts := Options{HostID: "datatug", Env: env} // no Confirm/Yes: Execute would refuse if ever reached

	result, err := Install(context.Background(), []string{"nosuchcli"}, opts)
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v, want KindUnknownTarget", selfupdate.KindOf(err))
	}
	if len(result.Results) != 0 {
		t.Errorf("result.Results = %v, want empty", result.Results)
	}
}

func TestInstall_RealRunExecutesAfterPlan(t *testing.T) {
	bin1 := fakeAbsDir("bin1")
	executables := map[string]bool{fakeAbsExe(bin1, "ovdb"): true}
	run := multiJSONRun(map[string]string{"ovdb": "1.0.0"})
	env := batchEnv([]string{bin1}, fakeAbsDir("usr", "bin"), executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	if result.Results[0].Outcome != OutcomeAlreadyInstalled {
		t.Errorf("Results[0] = %+v", result.Results[0])
	}
}
