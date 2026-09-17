package cobracmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// execName appends the real host OS's own executable suffix (".exe" on
// windows, none elsewhere) — cliinstall's own probeOne search (package
// cliinstall, unexported, not reachable from here) decides this the same
// way, via its own goosName test seam; this package can only ever see the
// REAL runtime.GOOS since it has no access to that seam, so these fixtures
// never override it either. A fixture that wrote its "old" binary at
// ".../bin/ovdb" while probeOne searched for ".../bin/ovdb.exe" on a real
// Windows CI run would never find it at all (task-22 fourth review).
func execName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// --- fixtures --------------------------------------------------------------

// wbUpgradeErrors folds every failure into a general findings code, like
// wbStyleErrors, and additionally implements UpgradeErrorMapper so tests can
// assert exactly which results UpgradesAvailable was called with.
type wbUpgradeErrors struct {
	upgradesAvailable *[]cliinstall.UpgradeResult
}

func (wbUpgradeErrors) Failure(err error) error { return exitError{code: 1, err: err} }
func (m wbUpgradeErrors) UpgradesAvailable(results []cliinstall.UpgradeResult) error {
	if m.upgradesAvailable != nil {
		*m.upgradesAvailable = results
	}
	return exitError{code: 3, err: errors.New("upgrades available")}
}

func newUpgradeCmd(t *testing.T, opts UpgradeCommandOptions) *cobra.Command {
	t.Helper()
	if opts.Interactive == nil {
		opts.Interactive = func() bool { return false }
	}
	return NewUpgrade(opts)
}

// --- flags, defaults, panics ------------------------------------------------

func TestNewUpgrade_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewUpgrade did not panic for a host id absent from the compiled catalog")
		}
	}()
	NewUpgrade(UpgradeCommandOptions{HostID: "nosuchhost"})
}

func TestNewUpgrade_DefaultsUseAndShort(t *testing.T) {
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug"})
	if !strings.HasPrefix(cmd.Use, "upgrade") {
		t.Errorf("Use = %q, want it to start with upgrade", cmd.Use)
	}
	if cmd.Short == "" {
		t.Error("Short is empty, want a default description")
	}
	if len(cmd.Aliases) != 0 {
		t.Errorf("Aliases = %v, want none by default (REQ: update-alias-policy)", cmd.Aliases)
	}
}

func TestNewUpgrade_CustomUseShortAliases(t *testing.T) {
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Use: "up [name...]", Short: "custom", Aliases: []string{"refresh"}})
	if cmd.Use != "up [name...]" {
		t.Errorf("Use = %q", cmd.Use)
	}
	if cmd.Short != "custom" {
		t.Errorf("Short = %q", cmd.Short)
	}
	if !cmd.HasAlias("refresh") {
		t.Error("missing alias 'refresh'")
	}
}

func TestNewUpgrade_RegistersExpectedFlags(t *testing.T) {
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug"})
	for _, name := range []string{"all", "check", "yes", "dry-run", "format"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	if cmd.Flags().Lookup("dir") != nil {
		t.Error("upgrade must not register --dir; it always acts on the located copy")
	}
	if f := cmd.Flags().Lookup("yes"); f.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want y", f.Shorthand)
	}
	if f := cmd.Flags().Lookup("format"); f.DefValue != "text" {
		t.Errorf("--format default = %q, want text", f.DefValue)
	}
}

// --- usage errors ------------------------------------------------------------

func TestUpgradeRunE_InvalidFormat(t *testing.T) {
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Errors: wbStyleErrors{}})
	_, _, err := runCmd(t, cmd, "--format", "yaml")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want *UsageError", err)
	}
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Errorf("exit code = %v, want 1 (via wbStyleErrors)", err)
	}
}

func TestUpgradeRunE_AllWithNames(t *testing.T) {
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug"})
	_, _, err := runCmd(t, cmd, "--all", "ovdb")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want *UsageError", err)
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error %q does not mention --all", err.Error())
	}
}

// --- bare report (REQ: upgrade-no-args-reports) ------------------------------

// TestUpgradeReport_Bare_PrintsReportAndNextStep covers the truly bare
// invocation with NOTHING installed: REQ: upgrade-targets' own "--all" set
// is "every catalog id whose status is installed, plus the host" — with no
// catalog target actually installed, that set is the host alone, unlike
// install's own bare listing (which lists every RELEVANT target regardless
// of install state). A "dev" host version is also a non-release build
// (REQ: upgrade-skips-non-release-builds), so the one row this report shows
// is a skip, not a lookup — see the next test for a report that also
// includes an installed, looked-up target.
func TestUpgradeReport_Bare_PrintsReportAndNextStep(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, HostConfig: selfupdate.Config{BinaryName: "datatug", CurrentVersion: "dev"}})

	out, errOut, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("err = %v, stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "datatug  dev  skipped: not a release build") {
		t.Errorf("stdout missing the host's own skip line:\n%s", out)
	}
	if !strings.Contains(out, "Next: datatug upgrade --all") || !strings.Contains(out, "datatug upgrade <name>") {
		t.Errorf("stdout missing the next-step line:\n%s", out)
	}
}

// TestUpgradeReport_Bare_IncludesInstalledTargets proves the --all set DOES
// include a real installed, non-host target (not just the host), and that
// its transition line is looked up and shown even without --all or --check.
func TestUpgradeReport_Bare_IncludesInstalledTargets(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env,
		HostConfig:       selfupdate.Config{BinaryName: "datatug", CurrentVersion: "dev"},
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "ovdb  0.4.0 → 0.5.0") {
		t.Errorf("stdout missing ovdb's own transition line:\n%s", out)
	}
}

func TestUpgradeReport_Bare_NeverCallsUpgradesAvailable(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))

	var calls []cliinstall.UpgradeResult
	mapper := wbUpgradeErrors{upgradesAvailable: &calls}
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env, Errors: mapper,
		HostConfig:       selfupdate.Config{BinaryName: "datatug", CurrentVersion: "dev"},
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	_, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("err = %v, want nil: the bare report MUST exit successfully whether or not upgrades are available", err)
	}
	if calls != nil {
		t.Errorf("UpgradesAvailable was called with %v; the bare report MUST NOT call it at all", calls)
	}
}

func TestUpgradeReport_Bare_FailsOnLookupFailure(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	// A "dev" CurrentVersion would be a non-release build, skipped WITHOUT
	// a lookup at all (REQ: upgrade-skips-non-release-builds) — a real
	// release-shaped version is required here so the host is actually
	// looked up and can fail that lookup. ReleasesAPIURL points at an
	// unreachable local address so the failure is fast and fully offline
	// (cli-install#req:no-network-in-tests). DetectHost is forced Manual
	// (never Ambiguous, which would fold this lookup failure into a mere
	// warning on an already-Refused row — task-22 review B1) so the
	// failure this test cares about is the one that actually surfaces.
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env, Errors: wbStyleErrors{},
		HostConfig: selfupdate.Config{BinaryName: "datatug", CurrentVersion: "1.0.0", ReleasesAPIURL: "http://127.0.0.1:1/releases"},
		DetectHost: func() (selfupdate.Detection, error) {
			return selfupdate.Detection{Method: selfupdate.Manual, Path: "/host/bin/datatug"}, nil
		},
	})

	_, _, err := runCmd(t, cmd)
	if err == nil {
		t.Fatal("expected the release-lookup failure to fail the bare report")
	}
	if selfupdate.KindOf(err) != selfupdate.KindReleaseLookup {
		t.Errorf("KindOf(err) = %v, want KindReleaseLookup", selfupdate.KindOf(err))
	}
}

// --- --check (REQ: upgrade-check) --------------------------------------------

func TestUpgradeCheck_CallsUpgradesAvailableForAvailableTargets(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))

	var calls []cliinstall.UpgradeResult
	mapper := wbUpgradeErrors{upgradesAvailable: &calls}
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env, Errors: mapper,
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	out, _, err := runCmd(t, cmd, "ovdb", "--check")
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("err = %v, want the UpgradesAvailable exit code 3", err)
	}
	if len(calls) != 1 || calls[0].Target != "ovdb" {
		t.Fatalf("UpgradesAvailable calls = %+v, want exactly one for ovdb", calls)
	}
	if !strings.Contains(out, "ovdb  0.4.0 → 0.5.0") {
		t.Errorf("stdout missing the ovdb transition line:\n%s", out)
	}
	if strings.Contains(out, "Next:") {
		t.Errorf("--check must not print the bare report's next-step line:\n%s", out)
	}
}

func TestUpgradeCheck_AheadAndUpToDateNeverCountAsAvailable(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	// ovdb already reports a version AHEAD of the release server's 0.5.0.
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "9.9.9"))

	var calls []cliinstall.UpgradeResult
	mapper := wbUpgradeErrors{upgradesAvailable: &calls}
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env, Errors: mapper,
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	_, _, err := runCmd(t, cmd, "ovdb", "--check")
	if err != nil {
		t.Fatalf("err = %v, want nil: an ahead target must never signal upgrades-available", err)
	}
	if calls != nil {
		t.Errorf("UpgradesAvailable was called with %v, want it never called", calls)
	}
}

// TestUpgradeCheck_SkipsTargetsWithNoLookup exercises the "continue" branch
// in runUpgradeReport's own available-collection loop: a target that was
// never looked up at all (Latest == "", here because it is not installed)
// must be skipped when building the UpgradesAvailable set, not just a
// looked-up-but-ahead/current one (already covered by
// TestUpgradeCheck_AheadAndUpToDateNeverCountAsAvailable).
func TestUpgradeCheck_SkipsTargetsWithNoLookup(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))

	var calls []cliinstall.UpgradeResult
	mapper := wbUpgradeErrors{upgradesAvailable: &calls}
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env, Errors: mapper,
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	// specscore is not installed in this env at all: its own row never
	// gets a release lookup (Latest stays "").
	_, _, err := runCmd(t, cmd, "ovdb", "specscore", "--check")
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("err = %v, want the UpgradesAvailable exit code 3", err)
	}
	if len(calls) != 1 || calls[0].Target != "ovdb" {
		t.Fatalf("UpgradesAvailable calls = %+v, want exactly one for ovdb (specscore was never looked up)", calls)
	}
}

// TestUpgradeCheck_PlainErrorMapperNeverCallsUpgradesAvailable proves a host
// that implements only ErrorMapper (not UpgradeErrorMapper) never panics or
// errors from the missing method — cli-install#req:upgrade-check's own
// "mirroring self-update's UpdateAvailable mapping" applies only to a host
// that opted in.
func TestUpgradeCheck_PlainErrorMapperNeverCallsUpgradesAvailable(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})

	_, _, err := runCmd(t, cmd, "ovdb", "--check")
	if err != nil {
		t.Fatalf("err = %v, want nil: a plain ErrorMapper has no upgrades-available signal to give", err)
	}
}

// TestUpgradeCheck_UnknownTarget_Text and _JSON exercise runUpgradeReport's
// own batch-level-refusal branch (an unknown name fails before any target
// is probed), text and JSON, mirroring TestUpgrade_UnknownTarget for the
// runUpgrade path.

func TestUpgradeCheck_UnknownTarget_Text(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})

	out, _, err := runCmd(t, cmd, "nosuchcli", "--check")
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Fatalf("KindOf(err) = %v, want KindUnknownTarget", selfupdate.KindOf(err))
	}
	if !strings.Contains(out, "nosuchcli") || !strings.Contains(out, "Refused:") {
		t.Errorf("stdout does not report the failure:\n%s", out)
	}
}

func TestUpgradeCheck_UnknownTarget_JSON(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})

	out, _, err := runCmd(t, cmd, "nosuchcli", "--check", "--format", "json")
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Fatalf("KindOf(err) = %v, want KindUnknownTarget", selfupdate.KindOf(err))
	}
	var doc struct {
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if !strings.Contains(doc.Error, "nosuchcli") {
		t.Errorf("doc.Error = %q, want it to name nosuchcli", doc.Error)
	}
}

func TestUpgradeCheck_UnknownTarget_JSON_WriteErrorIsMapped(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"nosuchcli", "--check", "--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

func TestUpgradeCheck_JSON_SingleDocument(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)})

	out, _, err := runCmd(t, cmd, "ovdb", "--check", "--format", "json")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Errorf("stdout has %d extra newlines, want exactly one JSON document:\n%s", n, out)
	}
	var doc struct {
		Targets []struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if len(doc.Targets) != 1 || doc.Targets[0].Name != "ovdb" || doc.Targets[0].Action != "dry_run" {
		t.Errorf("doc = %+v", doc)
	}
}

// --- unknown target and ErrorMapper dispatch ---------------------------------

func TestUpgrade_UnknownTarget(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})

	out, _, err := runCmd(t, cmd, "nosuchcli", "--yes")
	if err == nil {
		t.Fatal("expected an error for an unknown target")
	}
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v, want KindUnknownTarget", selfupdate.KindOf(err))
	}
	if !strings.Contains(out, "nosuchcli") || !strings.Contains(out, "Refused:") {
		t.Errorf("stdout does not report the failure:\n%s", out)
	}
}

func TestUpgrade_TwoErrorMappersYieldDifferentExitCodesForSameFailure(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)

	wbCmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	_, _, wbErr := runCmd(t, wbCmd, "nosuchcli", "--yes")

	specscoreCmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: specscoreStyleErrors{}})
	_, _, specscoreErr := runCmd(t, specscoreCmd, "nosuchcli", "--yes")

	var wbEC, specscoreEC exitCoder
	if !errors.As(wbErr, &wbEC) || !errors.As(specscoreErr, &specscoreEC) {
		t.Fatalf("errors are not exitCoder: wb=%v specscore=%v", wbErr, specscoreErr)
	}
	if wbEC.ExitCode() == specscoreEC.ExitCode() {
		t.Errorf("both mappers produced exit code %d for the same failure", wbEC.ExitCode())
	}
}

// --- non-interactive refusal --------------------------------------------------

func TestUpgrade_NonInteractiveWithoutYesRefuses(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env, Interactive: func() bool { return false },
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	_, _, err := runCmd(t, cmd, "ovdb")
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Fatalf("KindOf(err) = %v, want KindNonInteractive; err=%v", selfupdate.KindOf(err), err)
	}
}

// --- dry run walks the plan exactly once, no confirmation --------------------

func TestUpgrade_DryRun_PrintsReportOnceAndNoConfirmPrompt(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	interactiveCalled := false
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{
		HostID: "datatug", Env: env,
		Interactive:      func() bool { interactiveCalled = true; return true },
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	out, _, err := runCmd(t, cmd, "ovdb", "--dry-run")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(out, "0.4.0 → 0.5.0"); n != 1 {
		t.Errorf("stdout has %d transition lines, want exactly 1:\n%s", n, out)
	}
	if strings.Contains(out, "Upgrade ovdb?") {
		t.Errorf("--dry-run must never prompt for confirmation:\n%s", out)
	}
	if interactiveCalled {
		t.Error("--dry-run must never consult Interactive; it never confirms")
	}
	content, rerr := os.ReadFile(destPath)
	if rerr != nil || string(content) != "old" {
		t.Errorf("--dry-run modified the destination: %q, err=%v", content, rerr)
	}
}

func TestUpgrade_DryRun_JSON_SingleDocument(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)})

	out, _, err := runCmd(t, cmd, "ovdb", "--dry-run", "--format", "json")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Errorf("stdout has %d extra newlines, want exactly one JSON document:\n%s", n, out)
	}
	var doc struct {
		Targets []struct {
			Action string `json:"action"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if len(doc.Targets) != 1 || doc.Targets[0].Action != "dry_run" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestUpgrade_DryRun_JSON_WriteErrorIsMapped(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"ovdb", "--dry-run", "--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

// --- real end-to-end success (preview, confirm bypass, upgrade) -------------

// ovdbUpgradeFixture places an "old" ovdb binary at a manual-looking
// destination and wires a probe run func that reports 0.4.0 the first time
// (pre-upgrade) and 0.5.0 thereafter (post-swap verification), mirroring
// cliinstall's own manualUpgradeFixture (package cliinstall, unexported,
// not reachable from this package) closely enough for this test's purposes.
func ovdbUpgradeFixture(t *testing.T) (destPath string, env cliinstall.InstallEnv, configureRelease func(cliinstall.Entry, selfupdate.Config) selfupdate.Config) {
	t.Helper()
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(dest, []byte("old-binary"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	srv := releaseServer(t, "ovdb", "0.5.0")
	probed := 0
	run := func(_ context.Context, path string, args []string) ([]byte, error) {
		if path != dest || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
			return nil, errors.New("unexpected probe")
		}
		probed++
		version := "0.4.0"
		if probed > 1 {
			version = "0.5.0"
		}
		b, _ := json.Marshal(struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "ovdb", Version: version})
		return b, nil
	}
	e := cliinstall.InstallEnv{
		Env: cliinstall.Env{
			PathDirs:     func() []string { return []string{destDir} },
			HostDir:      func() (string, error) { return "", errors.New("no host dir") },
			IsExecutable: func(p string) bool { info, err := os.Stat(p); return err == nil && !info.IsDir() },
			EvalSymlinks: func(p string) (string, error) { return p, nil },
			Run:          run,
		},
		UserHomeDir: func() (string, error) { return "/home/alex", nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    os.MkdirAll,
	}
	return dest, e, configureReleaseFromServer(srv)
}

func TestUpgrade_RealSuccess_PreviewThenResult(t *testing.T) {
	destPath, env, configureRelease := ovdbUpgradeFixture(t)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureRelease})

	out, errOut, err := runCmd(t, cmd, "ovdb", "--yes")
	if err != nil {
		t.Fatalf("err = %v, stdout=%s stderr=%s", err, out, errOut)
	}
	if n := strings.Count(out, "0.4.0 → 0.5.0"); n != 1 {
		t.Errorf("stdout has %d preview transition lines, want exactly 1:\n%s", n, out)
	}
	if !strings.Contains(out, "upgraded 0.4.0 → v0.5.0 at "+destPath) {
		t.Errorf("stdout missing the final upgrade result:\n%s", out)
	}
	content, rerr := os.ReadFile(destPath)
	if rerr != nil || string(content) != "the installed binary" {
		t.Errorf("installed content = %q, err = %v", content, rerr)
	}
}

func TestUpgrade_RealSuccess_JSON(t *testing.T) {
	_, env, configureRelease := ovdbUpgradeFixture(t)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureRelease})

	out, errOut, err := runCmd(t, cmd, "ovdb", "--yes", "--format", "json")
	if err != nil {
		t.Fatalf("err = %v, stdout=%s stderr=%s", err, out, errOut)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Errorf("stdout has %d extra newlines, want exactly one JSON document:\n%s", n, out)
	}
	var doc struct {
		Targets []struct {
			Action string `json:"action"`
			Tag    string `json:"tag"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if len(doc.Targets) != 1 || doc.Targets[0].Action != "upgraded" || doc.Targets[0].Tag != "v0.5.0" {
		t.Errorf("doc = %+v", doc)
	}
	if !strings.Contains(errOut, "0.4.0 → 0.5.0") {
		t.Errorf("stderr missing the pre-execute preview:\n%s", errOut)
	}
}

// TestUpgrade_RealSuccess_JSON_FinalWriteErrorIsMapped exercises the final
// WriteUpgradeReportJSON error branch after a real, successful
// ExecuteUpgrade — as opposed to the dry-run or unknown-target JSON
// write-error branches already covered — mirroring install's own
// TestInstall_RealSuccess_JSON_FinalWriteErrorIsMapped.
func TestUpgrade_RealSuccess_JSON_FinalWriteErrorIsMapped(t *testing.T) {
	_, env, configureRelease := ovdbUpgradeFixture(t)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}, ConfigureRelease: configureRelease})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"ovdb", "--yes", "--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

// --- mapFailure never calls a configured ErrorMapper with a nil error ------

func TestUpgrade_DryRun_NeverCallsMapperWithNil(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(destDir, execName("ovdb"))
	if err := os.WriteFile(destPath, []byte("old"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	env := fakeInstallEnv([]string{destDir}, "/host", map[string]bool{destPath: true}, jsonProbe(destPath, "ovdb", "0.4.0"))
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: panicOnNilErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})

	_, _, err := runCmd(t, cmd, "ovdb", "--dry-run")
	if err != nil {
		t.Fatalf("err = %v, want nil (a successful dry run must never panic the ErrorMapper)", err)
	}
}

func TestUpgrade_RealSuccess_NeverCallsMapperWithNil(t *testing.T) {
	_, env, configureRelease := ovdbUpgradeFixture(t)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: panicOnNilErrors{}, ConfigureRelease: configureRelease})

	_, _, err := runCmd(t, cmd, "ovdb", "--yes")
	if err != nil {
		t.Fatalf("err = %v, want nil (a successful upgrade must never panic the ErrorMapper)", err)
	}
}

// --- write-error propagation ------------------------------------------------

func TestUpgradeReport_JSON_WriteErrorIsMapped(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

func TestUpgrade_JSON_WriteErrorIsMapped(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newUpgradeCmd(t, UpgradeCommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"nosuchcli", "--yes", "--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

// --- shared helper -----------------------------------------------------------

// jsonProbe answers "version --json" for exactly one path with version, and
// fails identification for every other path, mirroring cliinstall's own
// jsonRunFor (package cliinstall, unexported).
func jsonProbe(path, id, version string) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, p string, args []string) ([]byte, error) {
		if p == path && len(args) == 2 && args[0] == "version" && args[1] == "--json" {
			b, _ := json.Marshal(struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}{Name: id, Version: version})
			return b, nil
		}
		return nil, errors.New("unrecognized")
	}
}
