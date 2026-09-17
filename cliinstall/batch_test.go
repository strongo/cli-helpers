package cliinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/strongo/buildinfo"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- shared batch test fixtures ------------------------------------------

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
// that path's base filename in versions, naming itself after that base —
// so one Run func can back several distinct targets probed at different
// paths in the same test.
func multiJSONRun(versions map[string]string) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, path string, args []string) ([]byte, error) {
		if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
			base := filepath.Base(path)
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

// --- Install --------------------------------------------------------------

func TestInstall_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Install did not panic for a host id absent from the catalog")
		}
	}()
	_, _ = Install(context.Background(), nil, Options{HostID: "nosuchhost"})
}

func TestInstall_UnknownTargetDoesNotStopOthers(t *testing.T) {
	// "ovdb" also fails, but for a controlled planning reason (no PATH
	// entry for the per-user bin dir, host dir denylisted) — this proves
	// an unknown name and a distinct real failure are BOTH reported,
	// independently, with no network access at all.
	env := batchEnv(nil, "/usr/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Install(context.Background(), []string{"nosuchcli", "ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(result.Results))
	}
	if result.Results[0].Target != "nosuchcli" || result.Results[0].Outcome != OutcomeFailed || result.Results[0].Failure.Kind != selfupdate.KindUnknownTarget {
		t.Errorf("Results[0] = %+v", result.Results[0])
	}
	if result.Results[1].Target != "ovdb" || result.Results[1].Outcome != OutcomeFailed || result.Results[1].Failure.Kind != selfupdate.KindNoInstallDir {
		t.Errorf("Results[1] = %+v", result.Results[1])
	}
	if !result.Failed() {
		t.Error("BatchResult.Failed() = false, want true")
	}
}

func TestInstall_HostDirErrorTreatedAsEmpty(t *testing.T) {
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

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindNoInstallDir {
		t.Errorf("Results[0] = %+v, want KindNoInstallDir", r)
	}
}

func TestInstall_AlreadyInstalledIsNotReinstalled(t *testing.T) {
	executables := map[string]bool{"/bin1/ovdb": true}
	run := multiJSONRun(map[string]string{"ovdb": "1.2.3"})
	env := batchEnv([]string{"/bin1"}, "/usr/bin", executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeAlreadyInstalled || r.Version != "1.2.3" || r.UpdateHint != "ovdb self-update" {
		t.Errorf("Results[0] = %+v", r)
	}
}

func TestInstall_DedupesNames(t *testing.T) {
	executables := map[string]bool{"/bin1/ovdb": true}
	run := multiJSONRun(map[string]string{"ovdb": "1.2.3"})
	env := batchEnv([]string{"/bin1"}, "/usr/bin", executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Install(context.Background(), []string{"ovdb", "ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1 (de-duplicated)", len(result.Results))
	}
}

func TestInstall_UnrecognizedAtDestinationFails(t *testing.T) {
	hostDir := "/home/alex/go/bin" // Manual, not denylisted
	destPath := "/home/alex/go/bin/ovdb"
	executables := map[string]bool{destPath: true}
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("somethingelse 9.9.9 (abc) 2026-01-01"), nil
	}
	env := batchEnv(nil, hostDir, executables, run, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Yes: true}

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeFailed || r.Failure == nil || r.Failure.Kind != selfupdate.KindDestinationExists {
		t.Fatalf("Results[0] = %+v, want KindDestinationExists", r)
	}
	if r.Failure.Path != destPath {
		t.Errorf("Failure.Path = %q, want %q", r.Failure.Path, destPath)
	}
}

func TestInstall_UnrecognizedElsewhereWarnsAndDeclines(t *testing.T) {
	// The unrecognized copy is on PATH at /bin1, earlier than the planned
	// destination /home/alex/go/bin (not on PATH at all) — REQ:
	// unrecognized-copy-not-trusted's shadowing case.
	hostDir := "/home/alex/go/bin"
	unrecognizedPath := "/bin1/ovdb"
	executables := map[string]bool{unrecognizedPath: true}
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("somethingelse 9.9.9 (abc) 2026-01-01"), nil
	}
	env := batchEnv([]string{"/bin1"}, hostDir, executables, run, noRunManaged)
	confirmCalled := false
	opts := Options{HostID: "datatug", Env: env, Confirm: func(names []string) (bool, error) {
		confirmCalled = true
		return false, nil
	}}

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	if !confirmCalled {
		t.Fatal("Confirm was not called")
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDeclined {
		t.Fatalf("Outcome = %v, want OutcomeDeclined", r.Outcome)
	}
	found := false
	for _, w := range r.Warnings {
		if w != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want the shadowing warning even though declined", r.Warnings)
	}
	if result.Failed() {
		t.Error("BatchResult.Failed() = true, want false: a decline is not a failure")
	}
}

func TestInstall_DryRunSkipsConfirmAndNetwork_Homebrew(t *testing.T) {
	hostDir := "/opt/homebrew/Caskroom/wb/1.0.0"
	env := batchEnv(nil, hostDir, nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") }, noRunManaged)
	opts := Options{HostID: "wb", Env: env, DryRun: true} // no Confirm set: panics if ever called

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeDryRun || r.Method != MethodHomebrew {
		t.Fatalf("Results[0] = %+v", r)
	}
	if len(r.CaskArgv) == 0 {
		t.Errorf("CaskArgv = %v, want the planned brew argv", r.CaskArgv)
	}
}

func TestInstall_HomebrewPrintOnlyRedirectsWithoutConfirmOrRun(t *testing.T) {
	hostDir := "/opt/homebrew/Caskroom/wb/1.0.0"
	env := batchEnv(nil, hostDir, nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") }, noRunManaged)
	opts := Options{HostID: "wb", Env: env, HomebrewPrintOnly: true} // no Confirm set

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	r := result.Results[0]
	if r.Outcome != OutcomeRedirected {
		t.Fatalf("Outcome = %v, want OutcomeRedirected", r.Outcome)
	}
}

func TestInstall_ConfirmationGate_NoCallbackConfiguredRefuses(t *testing.T) {
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	opts := Options{HostID: "datatug", Env: env} // Yes false, Confirm nil

	result, err := Install(context.Background(), []string{"ovdb"}, opts)
	if err == nil {
		t.Fatal("Install error = nil, want a non-interactive refusal before any target result")
	}
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(err) = %v, want KindNonInteractive", selfupdate.KindOf(err))
	}
	if len(result.Results) != 0 {
		t.Errorf("result = %+v, want zero value on a batch-level refusal", result)
	}
}

func TestInstall_ConfirmationGate_CallbackErrorPropagates(t *testing.T) {
	sentinel := &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errors.New("no tty")}
	env := batchEnv(nil, "/home/alex/go/bin", nil, func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no proc") }, noRunManaged)
	opts := Options{HostID: "datatug", Env: env, Confirm: func([]string) (bool, error) { return false, sentinel }}

	_, err := Install(context.Background(), []string{"ovdb"}, opts)
	if !errors.Is(err, sentinel) && err != error(sentinel) {
		if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
			t.Errorf("Install error = %v, want the Confirm callback's own error propagated", err)
		}
	}
}

func TestInstall_ConfirmationGate_NamesOnlyPendingTargets(t *testing.T) {
	// "ovdb" is already installed (excluded from confirmation); "datatug"
	// is not (included) — REQ: confirmation-gate: "Targets that are
	// already installed... are excluded from the question."
	executables := map[string]bool{"/bin1/ovdb": true}
	run := multiJSONRun(map[string]string{"ovdb": "1.0.0"})
	env := batchEnv([]string{"/bin1"}, "/home/alex/go/bin", executables, run, noRunManaged)
	var confirmedNames []string
	opts := Options{HostID: "wb", Env: env, Confirm: func(names []string) (bool, error) {
		confirmedNames = names
		return false, nil
	}}

	_, err := Install(context.Background(), []string{"ovdb", "datatug"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	if len(confirmedNames) != 1 || confirmedNames[0] != "datatug" {
		t.Errorf("Confirm was asked about %v, want only [datatug]", confirmedNames)
	}
}

// TestInstall_AcceptedBatchInstallsIndependently mirrors cli-install#ac:
// batch-reports-every-target: two Homebrew-classified targets, one whose
// managed command succeeds and one whose fails — both get a result, in
// order, and the earlier failure never stops the later target.
func TestInstall_AcceptedBatchInstallsIndependently(t *testing.T) {
	executables := map[string]bool{}
	run := multiJSONRun(map[string]string{"ovdb": "2.0.0"})
	runManaged := func(_ context.Context, exe string, args []string) error {
		token := args[len(args)-1]
		if token == "openvaultdb/tap/ovdb" {
			executables["/opt/homebrew/bin/ovdb"] = true
			return nil
		}
		return errors.New("brew: cask not found")
	}
	env := batchEnv([]string{"/opt/homebrew/bin"}, "/opt/homebrew/Caskroom/wb/1.0.0", executables, run, runManaged)
	opts := Options{HostID: "wb", Env: env, Yes: true}

	result, err := Install(context.Background(), []string{"ovdb", "datatug"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
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

// TestInstall_DirectBatchOneFailsOthersSucceed follows the plan's own
// worked example: `install a b c` where b fails to download, run with
// --yes — a and c install, b's typed failure is reported, and the overall
// command fails.
func TestInstall_DirectBatchOneFailsOthersSucceed(t *testing.T) {
	version := "1.2.3"
	tag := "v1.2.3"
	binContent := []byte("payload")
	// ovdb's real catalog entry overrides ChecksumsName to a flat
	// "checksums.txt" (see catalog_ovdb.go) and leaves AssetName at the
	// shared GoReleaser-shaped default — Install() looks the target up in
	// the real compiled catalog, so this fixture must match that entry
	// exactly, not a hand-picked naming.
	okAsset := fmt.Sprintf("ovdb_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	archive := makeTarGz(t, "ovdb", binContent)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), okAsset)

	okServer := newReleaseServer(t, `[{"tag_name":"`+tag+`","prerelease":false,"draft":false}]`, map[string][]byte{
		"/" + tag + "/" + okAsset:    archive,
		"/" + tag + "/checksums.txt": []byte(checksums),
	})
	failServer := newReleaseServer(t, `[{"tag_name":"v0.1.0","prerelease":false,"draft":false}]`, nil) // no asset published: download fails

	destDir := t.TempDir()
	run := multiJSONRun(map[string]string{"ovdb": version})
	env := batchEnv([]string{destDir}, "/nonexistent-host-dir", nil, run, noRunManaged)
	// IsExecutable must reflect a real install landing in destDir so the
	// post-install verification probe (and the batch's own PATH-based
	// executable check) sees it.
	env.IsExecutable = func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	opts := Options{
		HostID: "datatug", Env: env, Yes: true, Dir: destDir,
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

	result, err := Install(context.Background(), []string{"ovdb", "ingitdb"}, opts)
	if err != nil {
		t.Fatalf("Install error = %v", err)
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
}

// --- declinedDestination -----------------------------------------------

func TestDeclinedDestination(t *testing.T) {
	direct := pendingTarget{method: MethodDirect, destDir: "/home/alex/.local/bin", entry: Entry{ID: "ovdb"}}
	if got := declinedDestination(direct); got != "/home/alex/.local/bin/ovdb" {
		t.Errorf("declinedDestination(direct) = %q", got)
	}
	homebrew := pendingTarget{method: MethodHomebrew, entry: Entry{ID: "ovdb"}}
	if got := declinedDestination(homebrew); got != "" {
		t.Errorf("declinedDestination(homebrew) = %q, want empty", got)
	}
}
