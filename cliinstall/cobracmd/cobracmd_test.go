package cobracmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- fixtures --------------------------------------------------------------

// exitCoder is the convention a host CLI's top-level runner checks to turn
// an error into a process exit code, mirroring selfupdate/cobracmd's own
// test fixture exactly.
type exitCoder interface{ ExitCode() int }

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) ExitCode() int { return e.code }
func (e exitError) Unwrap() error { return e.err }

// wbStyleErrors folds every new cli-install kind into its general findings
// code (1); it never uses a dedicated code for anything.
type wbStyleErrors struct{}

func (wbStyleErrors) Failure(err error) error {
	return exitError{code: 1, err: err}
}

// specscoreStyleErrors reserves a dedicated invalid-arguments code (2) for
// KindUnknownTarget and a *UsageError, and a distinct invalid-state code (4)
// for everything else — proving two ErrorMappers can disagree about the
// SAME underlying failure (cli-install#req:host-owned-exit-codes).
type specscoreStyleErrors struct{}

func (specscoreStyleErrors) Failure(err error) error {
	var usage *UsageError
	if errors.As(err, &usage) {
		return exitError{code: 2, err: err}
	}
	if selfupdate.KindOf(err) == selfupdate.KindUnknownTarget {
		return exitError{code: 2, err: err}
	}
	return exitError{code: 4, err: err}
}

func runCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// fakeInstallEnv builds an offline InstallEnv, mirroring the shape
// cliinstall's own tests use (cli-install#req:no-network-in-tests).
func fakeInstallEnv(pathDirs []string, hostDir string, executables map[string]bool, run func(context.Context, string, []string) ([]byte, error)) cliinstall.InstallEnv {
	return cliinstall.InstallEnv{
		Env: cliinstall.Env{
			PathDirs:     func() []string { return pathDirs },
			HostDir:      func() (string, error) { return hostDir, nil },
			IsExecutable: func(p string) bool { return executables[p] },
			EvalSymlinks: func(p string) (string, error) { return p, nil },
			Run:          run,
		},
		UserHomeDir: func() (string, error) { return "/home/alex", nil },
		Getenv:      func(string) string { return "" },
		MkdirAll:    func(string, os.FileMode) error { return nil },
	}
}

func noProc(context.Context, string, []string) ([]byte, error) {
	return nil, errors.New("no such process")
}

// panicOnNilErrors is the regression fixture for the nil-mapFailure bug two
// consumers hit building their own ErrorMapper: Failure documents that it
// receives a non-nil error, so a mapper is entitled to assume that and,
// like this one, panic otherwise. mapFailure itself is what must never call
// it with nil.
type panicOnNilErrors struct{}

func (panicOnNilErrors) Failure(err error) error {
	if err == nil {
		panic("ErrorMapper.Failure called with a nil error")
	}
	return err
}

func newInstallCmd(t *testing.T, opts CommandOptions) *cobra.Command {
	t.Helper()
	if opts.Interactive == nil {
		opts.Interactive = func() bool { return false }
	}
	return New(opts)
}

// --- New: flags and panics --------------------------------------------------

func TestNew_PanicsOnUnknownHost(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("New did not panic for a host id absent from the compiled catalog")
		}
	}()
	New(CommandOptions{HostID: "nosuchhost"})
}

func TestNew_DefaultsUseAndShort(t *testing.T) {
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug"})
	if !strings.HasPrefix(cmd.Use, "install") {
		t.Errorf("Use = %q, want it to start with install", cmd.Use)
	}
	if cmd.Short == "" {
		t.Error("Short is empty, want a default description")
	}
}

func TestNew_CustomUseShortAliases(t *testing.T) {
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Use: "add [name...]", Short: "custom", Aliases: []string{"get"}})
	if cmd.Use != "add [name...]" {
		t.Errorf("Use = %q", cmd.Use)
	}
	if cmd.Short != "custom" {
		t.Errorf("Short = %q", cmd.Short)
	}
	if !cmd.HasAlias("get") {
		t.Error("missing alias 'get'")
	}
}

func TestNew_RegistersExpectedFlags(t *testing.T) {
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug"})
	for _, name := range []string{"all", "yes", "dry-run", "dir", "format"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	if f := cmd.Flags().Lookup("yes"); f.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want y", f.Shorthand)
	}
	if f := cmd.Flags().Lookup("format"); f.DefValue != "text" {
		t.Errorf("--format default = %q, want text", f.DefValue)
	}
}

// --- usage errors ------------------------------------------------------------

func TestRunE_InvalidFormat(t *testing.T) {
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Errors: wbStyleErrors{}})
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

func TestRunE_AllWithNames(t *testing.T) {
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug"})
	_, _, err := runCmd(t, cmd, "--all", "ovdb")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want *UsageError", err)
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error %q does not mention --all", err.Error())
	}
}

func TestRunE_NilErrorsReturnsUnwrapped(t *testing.T) {
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug"})
	_, _, err := runCmd(t, cmd, "--format", "bogus")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want *UsageError unwrapped (Errors was nil)", err)
	}
}

// --- resolveEnv ---------------------------------------------------------

func TestResolveEnv_DefaultsWhenUnset(t *testing.T) {
	env := resolveEnv(cliinstall.InstallEnv{})
	if env.PathDirs == nil || env.HostDir == nil || env.IsExecutable == nil || env.Run == nil {
		t.Errorf("resolveEnv with zero Env did not default to DefaultInstallEnv: %+v", env)
	}
}

func TestResolveEnv_KeepsCallerEnv(t *testing.T) {
	called := false
	custom := fakeInstallEnv(nil, "", nil, noProc)
	custom.PathDirs = func() []string { called = true; return nil }
	env := resolveEnv(custom)
	env.PathDirs()
	if !called {
		t.Error("resolveEnv did not keep the caller-supplied Env")
	}
}

// --- listing --------------------------------------------------------------

func TestList_NoNames_RelevantOnly(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env})

	out, errOut, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"ingitdb: not installed", "ovdb: not installed", "specscore: not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "wb:") || strings.Contains(out, "chatwright:") {
		t.Errorf("bare listing must not include non-relevant targets:\n%s", out)
	}
	if !strings.Contains(out, "Run 'datatug install <name>' to see details and install.") {
		t.Errorf("stdout missing the hint line:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("errOut = %q, want empty", errOut)
	}
}

func TestList_All_IncludesEveryOtherEntry(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env})

	out, _, err := runCmd(t, cmd, "--all")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "wb: not installed") {
		t.Errorf("--all must list every other catalog entry, missing wb:\n%s", out)
	}
	if !strings.Contains(out, "Not listed as relevant to datatug.") {
		t.Errorf("--all must note non-relevant entries:\n%s", out)
	}
	if strings.Contains(out, "datatug:") {
		t.Errorf("--all must exclude the host itself:\n%s", out)
	}
}

func TestList_JSON(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env})

	out, _, err := runCmd(t, cmd, "--format", "json")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	var doc struct {
		Host    string `json:"host"`
		Targets []struct {
			Name string `json:"name"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if doc.Host != "datatug" || len(doc.Targets) != 3 {
		t.Errorf("doc = %+v", doc)
	}
}

// --- install: unknown target and ErrorMapper dispatch -----------------------

func TestInstall_UnknownTarget(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})

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

// This is the brief's explicit proof: the exact same underlying failure
// (install nosuchcli), routed through two different host ErrorMappers,
// yields two different errors/exit codes — the whole point of
// cli-install#req:host-owned-exit-codes.
func TestInstall_TwoErrorMappersYieldDifferentExitCodesForSameFailure(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)

	wbCmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	_, _, wbErr := runCmd(t, wbCmd, "nosuchcli", "--yes")

	specscoreCmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: specscoreStyleErrors{}})
	_, _, specscoreErr := runCmd(t, specscoreCmd, "nosuchcli", "--yes")

	var wbEC, specscoreEC exitCoder
	if !errors.As(wbErr, &wbEC) || !errors.As(specscoreErr, &specscoreEC) {
		t.Fatalf("errors are not exitCoder: wb=%v specscore=%v", wbErr, specscoreErr)
	}
	if wbEC.ExitCode() == specscoreEC.ExitCode() {
		t.Errorf("both mappers produced exit code %d for the same failure; wanted different codes (wb style folds into general code, specscore style reserves a dedicated one)", wbEC.ExitCode())
	}
	if wbEC.ExitCode() != 1 {
		t.Errorf("wbStyleErrors exit code = %d, want 1", wbEC.ExitCode())
	}
	if specscoreEC.ExitCode() != 2 {
		t.Errorf("specscoreStyleErrors exit code = %d, want 2", specscoreEC.ExitCode())
	}
}

func TestInstall_NoErrorsReturnsUnderlyingFailure(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env})
	_, _, err := runCmd(t, cmd, "nosuchcli", "--yes")
	if selfupdate.KindOf(err) != selfupdate.KindUnknownTarget {
		t.Errorf("KindOf(err) = %v, want KindUnknownTarget", selfupdate.KindOf(err))
	}
}

// --- install: non-interactive refusal ---------------------------------------

func TestInstall_NonInteractiveWithoutYesRefuses(t *testing.T) {
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	// "ovdb" is a valid, not-installed target planned as a direct install:
	// under Plan+confirm+Execute (task-5 review B1) planning ALWAYS
	// resolves its release exactly once, before Execute ever asks for
	// confirmation, so this needs a local release server just like any
	// other Plan call — task-5 review S5 found this exact test leaking a
	// real request to api.github.com because it omitted one.
	srv := releaseServer(t, "ovdb", "0.5.0")
	cmd := newInstallCmd(t, CommandOptions{
		HostID: "datatug", Env: env, Interactive: func() bool { return false },
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	// --dir gives planning a valid destination (a denylist-clean
	// directory), so this reaches the confirmation gate itself.
	_, _, err := runCmd(t, cmd, "ovdb", "--dir", t.TempDir())
	if selfupdate.KindOf(err) != selfupdate.KindNonInteractive {
		t.Fatalf("KindOf(err) = %v, want KindNonInteractive; err=%v", selfupdate.KindOf(err), err)
	}
}

// --- install: dry run walks the plan exactly once, no confirmation ----------

func TestInstall_DryRun_PrintsPlanOnceAndNoConfirmPrompt(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	interactiveCalled := false
	cmd := newInstallCmd(t, CommandOptions{
		HostID: "datatug", Env: env,
		Interactive:      func() bool { interactiveCalled = true; return true },
		ConfigureRelease: configureReleaseFromServer(srv),
	})

	out, _, err := runCmd(t, cmd, "ovdb", "--dry-run", "--dir", t.TempDir())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(out, "Plan: direct install"); n != 1 {
		t.Errorf("stdout has %d 'Plan:' lines, want exactly 1:\n%s", n, out)
	}
	if strings.Contains(out, "Install ovdb?") {
		t.Errorf("--dry-run must never prompt for confirmation:\n%s", out)
	}
	if interactiveCalled {
		t.Error("--dry-run must never consult Interactive; it never confirms")
	}
}

func TestInstall_DryRun_JSON_SingleDocument(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)})

	out, _, err := runCmd(t, cmd, "ovdb", "--dry-run", "--format", "json", "--dir", t.TempDir())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Errorf("stdout has %d extra newlines, want exactly one JSON document:\n%s", n, out)
	}
	var doc struct {
		Targets []struct {
			Outcome string `json:"outcome"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if len(doc.Targets) != 1 || doc.Targets[0].Outcome != "dry_run" {
		t.Errorf("doc = %+v", doc)
	}
}

// --- mapFailure never calls a configured ErrorMapper with a nil error ------

// TestInstall_DryRun_NeverCallsMapperWithNil is the regression test for the
// bug two consumers hit: a successful --dry-run reaches
// `mapFailure(opts.Errors, plan.Failure())`, and plan.Failure() is nil for a
// dry run with nothing failed — mapFailure must short-circuit before ever
// calling panicOnNilErrors.Failure(nil).
func TestInstall_DryRun_NeverCallsMapperWithNil(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: panicOnNilErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})

	_, _, err := runCmd(t, cmd, "ovdb", "--dry-run", "--dir", t.TempDir())
	if err != nil {
		t.Fatalf("err = %v, want nil (a successful dry run must never panic the ErrorMapper)", err)
	}
}

// TestInstall_RealSuccess_NeverCallsMapperWithNil is the same regression for
// `mapFailure(opts.Errors, result.Failure())` after a real, successful
// Execute.
func TestInstall_RealSuccess_NeverCallsMapperWithNil(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "ovdb")
	if runtime.GOOS == "windows" {
		destPath += ".exe"
	}
	run := func(_ context.Context, path string, args []string) ([]byte, error) {
		if path != destPath || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
			return nil, errors.New("unexpected probe")
		}
		b, _ := json.Marshal(struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "ovdb", Version: "0.5.0"})
		return b, nil
	}
	env := cliinstall.InstallEnv{
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
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: panicOnNilErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})

	_, _, err := runCmd(t, cmd, "ovdb", "--yes", "--dir", destDir)
	if err != nil {
		t.Fatalf("err = %v, want nil (a successful install must never panic the ErrorMapper)", err)
	}
}

// --- write-error propagation ------------------------------------------------

// failingWriter always fails, exercising the JSON-encode-error branch of
// runList and runInstall — the one way cliui.WriteListJSON/WriteResultJSON
// themselves can return a non-nil error.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestList_JSON_WriteErrorIsMapped(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

func TestInstall_JSON_WriteErrorIsMapped(t *testing.T) {
	env := fakeInstallEnv(nil, "", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"nosuchcli", "--yes", "--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

// --- install: real end-to-end success (preview, confirm bypass, install) ---

// releaseServer serves one minimal release for id/version, naming the asset
// and checksums file exactly as id's real catalog entry declares (its own
// override, or the shared GoReleaser-shaped default) — REQ:
// catalog-identity-single-source means a fixture that assumed the default
// unconditionally would silently break for an entry like ovdb's, whose
// release publishes one flat "checksums.txt" instead.
func releaseServer(t *testing.T, id, version string) *httptest.Server {
	t.Helper()
	entry, ok := cliinstall.ByID(id)
	if !ok {
		t.Fatalf("catalog missing %s", id)
	}
	tag := "v" + version
	content := []byte("the installed binary")
	// equivArchiveFixture (selfupdate_equivalence_test.go, same package)
	// builds the archive FORMAT selfupdate.extractBinary actually reads
	// per GOOS — a real .zip on windows, a real .tar.gz elsewhere — not
	// just an asset NAME with the right extension on unreadable content.
	// Naming the asset "*.zip" while always packing a .tar.gz (this
	// fixture's own bug before it was ever run on Windows CI) made every
	// TestInstall_RealSuccess_* test fail there with "open zip archive:
	// zip: not a valid zip file".
	archive, archiveExt := equivArchiveFixture(t, id, content)
	ext := strings.TrimPrefix(archiveExt, ".")
	assetName := entry.AssetName
	if assetName == nil {
		assetName = func(binary, version, goos, goarch string) string {
			return fmt.Sprintf("%s_%s_%s_%s.%s", binary, version, goos, goarch, ext)
		}
	}
	checksumsName := entry.ChecksumsName
	if checksumsName == nil {
		checksumsName = func(binary, version string) string { return fmt.Sprintf("%s_%s_checksums.txt", binary, version) }
	}
	asset := assetName(id, version, runtime.GOOS, runtime.GOARCH)
	checksum := sha256HexFixture(archive)
	checksumsFile := checksumsName(id, version)
	checksumsBody := fmt.Sprintf("%s  %s\n", checksum, asset)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"tag_name":"` + tag + `","prerelease":false,"draft":false}]`))
		case "/" + tag + "/" + asset:
			_, _ = w.Write(archive)
		case "/" + tag + "/" + checksumsFile:
			_, _ = w.Write([]byte(checksumsBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func configureReleaseFromServer(srv *httptest.Server) func(cliinstall.Entry, selfupdate.Config) selfupdate.Config {
	return func(_ cliinstall.Entry, cfg selfupdate.Config) selfupdate.Config {
		cfg.ReleasesAPIURL = srv.URL + "/releases"
		cfg.DownloadURL = func(_, tag, asset string) string { return srv.URL + "/" + tag + "/" + asset }
		cfg.HTTPClient = srv.Client()
		return cfg
	}
}

func TestInstall_RealSuccess_PreviewThenResult(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "ovdb")
	if runtime.GOOS == "windows" {
		destPath += ".exe"
	}

	run := func(_ context.Context, path string, args []string) ([]byte, error) {
		if path != destPath || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
			return nil, errors.New("unexpected probe")
		}
		b, _ := json.Marshal(struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "ovdb", Version: "0.5.0"})
		return b, nil
	}
	env := cliinstall.InstallEnv{
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

	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)})

	out, errOut, err := runCmd(t, cmd, "ovdb", "--yes", "--dir", destDir)
	if err != nil {
		t.Fatalf("err = %v, stdout=%s stderr=%s", err, out, errOut)
	}
	if n := strings.Count(out, "Plan: direct install"); n != 1 {
		t.Errorf("stdout has %d 'Plan:' lines (preview), want exactly 1:\n%s", n, out)
	}
	// The final report is WriteOutcome's TERSE form — no repeated
	// description/details block, just id + outcome (task-5 review B1).
	if !strings.Contains(out, "ovdb: installed v0.5.0 at "+destPath) {
		t.Errorf("stdout missing the final install result:\n%s", out)
	}
	if n := strings.Count(out, "OpenVaultDB command-line interface"); n != 1 {
		t.Errorf("stdout repeats ovdb's description %d times, want exactly 1 (preview only, not the final report)", n)
	}
	content, rerr := os.ReadFile(destPath)
	if rerr != nil {
		t.Fatalf("installed file not found: %v", rerr)
	}
	if string(content) != "the installed binary" {
		t.Errorf("installed content = %q", content)
	}
}

// TestInstall_RealSuccess_JSON exercises the SAME real end-to-end install
// as TestInstall_RealSuccess_PreviewThenResult, but in --format json: the
// pre-execute preview goes to stderr, and stdout carries exactly one final
// JSON document with the executed outcome.
func TestInstall_RealSuccess_JSON(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "ovdb")
	if runtime.GOOS == "windows" {
		destPath += ".exe"
	}

	run := func(_ context.Context, path string, args []string) ([]byte, error) {
		if path != destPath || len(args) != 2 || args[0] != "version" || args[1] != "--json" {
			return nil, errors.New("unexpected probe")
		}
		b, _ := json.Marshal(struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "ovdb", Version: "0.5.0"})
		return b, nil
	}
	env := cliinstall.InstallEnv{
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
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, ConfigureRelease: configureReleaseFromServer(srv)})

	out, errOut, err := runCmd(t, cmd, "ovdb", "--yes", "--dir", destDir, "--format", "json")
	if err != nil {
		t.Fatalf("err = %v, stdout=%s stderr=%s", err, out, errOut)
	}
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 0 {
		t.Errorf("stdout has %d extra newlines, want exactly one JSON document:\n%s", n, out)
	}
	var doc struct {
		Targets []struct {
			Outcome string `json:"outcome"`
			Version string `json:"planned_version"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v (raw %s)", jerr, out)
	}
	if len(doc.Targets) != 1 || doc.Targets[0].Outcome != "installed" {
		t.Errorf("doc = %+v", doc)
	}
	if !strings.Contains(errOut, "Plan: direct install") {
		t.Errorf("stderr missing the pre-execute preview:\n%s", errOut)
	}
}

func TestInstall_DryRun_JSON_WriteErrorIsMapped(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	env := fakeInstallEnv(nil, "/host", nil, noProc)
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})
	cmd.SetOut(failingWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"ovdb", "--dry-run", "--format", "json", "--dir", t.TempDir()})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

// TestInstall_RealSuccess_JSON_FinalWriteErrorIsMapped exercises the final
// WriteResultJSON error branch after a real Execute (as opposed to the
// dry-run or unknown-target JSON write-error branches already covered).
func TestInstall_RealSuccess_JSON_FinalWriteErrorIsMapped(t *testing.T) {
	srv := releaseServer(t, "ovdb", "0.5.0")
	destDir := t.TempDir()
	run := func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("not installed") }
	env := cliinstall.InstallEnv{
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
	cmd := newInstallCmd(t, CommandOptions{HostID: "datatug", Env: env, Errors: wbStyleErrors{}, ConfigureRelease: configureReleaseFromServer(srv)})
	cmd.SetOut(failingWriter{})
	errOut := &bytes.Buffer{}
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"ovdb", "--yes", "--dir", destDir, "--format", "json"})

	err := cmd.Execute()
	var ec exitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("err = %v, want a mapped wbStyleErrors failure", err)
	}
}

func makeTarGzFixture(t *testing.T, binName string, content []byte) []byte {
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

func sha256HexFixture(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
