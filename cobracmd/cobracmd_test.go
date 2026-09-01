package cobracmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/strongo/selfupdate"
)

// exitCoder is the convention a host CLI's top-level runner checks to turn
// an error into a process exit code — the same shape both fixture
// ErrorMappers below use, deliberately with DIFFERENT numbers for the same
// situation, to prove the contracts stay independent.
type exitCoder interface{ ExitCode() int }

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) ExitCode() int { return e.code }
func (e exitError) Unwrap() error { return e.err }

// wbStyleErrors models a three-code contract (0/1/2) that folds "update
// available" into its general findings code (1), never a dedicated one.
type wbStyleErrors struct{ updateAvailableCalls *int }

func (m wbStyleErrors) Failure(err error) error {
	code := 1
	if selfupdate.KindOf(err) == selfupdate.KindPermission {
		code = 2
	}
	return exitError{code: code, err: err}
}

func (m wbStyleErrors) UpdateAvailable(res selfupdate.CheckResult) error {
	if m.updateAvailableCalls != nil {
		*m.updateAvailableCalls++
	}
	return exitError{code: 1, err: errors.New("update available")} // general findings code, not dedicated
}

// specscoreStyleErrors models a contract with a DEDICATED exit code (10)
// reserved specifically for "update available", distinct from its general
// failure code (4).
type specscoreStyleErrors struct{ updateAvailableCalls *int }

func (m specscoreStyleErrors) Failure(err error) error {
	return exitError{code: 4, err: err}
}

func (m specscoreStyleErrors) UpdateAvailable(res selfupdate.CheckResult) error {
	if m.updateAvailableCalls != nil {
		*m.updateAvailableCalls++
	}
	return exitError{code: 10, err: errors.New("update available")}
}

func testConfig() selfupdate.Config {
	return selfupdate.Config{BinaryName: "tool", Repository: "acme/tool", CurrentVersion: "1.0.0"}
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

// --- flags ---

func TestNew_DefaultsUseAndShort(t *testing.T) {
	cmd := New(testConfig(), CommandOptions{})
	if cmd.Name() != "self-update" {
		t.Errorf("Name() = %q, want self-update", cmd.Name())
	}
	if cmd.Short == "" {
		t.Error("Short is empty, want a default description")
	}
}

func TestNew_CustomUseShortAliases(t *testing.T) {
	cmd := New(testConfig(), CommandOptions{Use: "upgrade", Short: "custom", Aliases: []string{"up"}})
	if cmd.Name() != "upgrade" {
		t.Errorf("Name() = %q, want upgrade", cmd.Name())
	}
	if cmd.Short != "custom" {
		t.Errorf("Short = %q, want custom", cmd.Short)
	}
	if !cmd.HasAlias("up") {
		t.Error("missing alias 'up'")
	}
}

func TestNew_RegistersExpectedFlags(t *testing.T) {
	cmd := New(testConfig(), CommandOptions{})
	for _, name := range []string{"check", "yes", "version", "allow-downgrade", "dry-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	if f := cmd.Flags().Lookup("yes"); f.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want y", f.Shorthand)
	}
	if cmd.Flags().Lookup("format") != nil {
		t.Error("--format registered without JSONFormat: true")
	}
}

func TestNew_JSONFormatRegistersFormatFlag(t *testing.T) {
	cmd := New(testConfig(), CommandOptions{JSONFormat: true})
	f := cmd.Flags().Lookup("format")
	if f == nil {
		t.Fatal("missing --format flag with JSONFormat: true")
	}
	if f.DefValue != "text" {
		t.Errorf("--format default = %q, want text", f.DefValue)
	}
}

func TestNew_RejectsExtraArgs(t *testing.T) {
	cmd := New(testConfig(), CommandOptions{})
	withStubCheck(t, selfupdate.CheckResult{Verdict: selfupdate.UpToDate}, nil)
	_, _, err := runCmd(t, cmd, "extra-positional", "--check")
	if err == nil {
		t.Fatal("expected error for extra positional argument")
	}
}

// --- test seams ---

func withStubCheck(t *testing.T, result selfupdate.CheckResult, err error) {
	t.Helper()
	prev := checkFunc
	checkFunc = func(selfupdate.Config, context.Context) (selfupdate.CheckResult, error) { return result, err }
	t.Cleanup(func() { checkFunc = prev })
}

func withStubUpdate(t *testing.T, capture *selfupdate.Options, outcome selfupdate.Outcome, err error) {
	t.Helper()
	prev := updateFunc
	updateFunc = func(_ selfupdate.Config, _ context.Context, opts selfupdate.Options) (selfupdate.Outcome, error) {
		if capture != nil {
			*capture = opts
		}
		return outcome, err
	}
	t.Cleanup(func() { updateFunc = prev })
}

// checkFunc and updateFunc default to calling cfg.Check/cfg.Update directly;
// every other test in this file stubs them via withStubCheck/withStubUpdate,
// which never exercises those default closures themselves. These two tests
// deliberately do NOT stub them, so the real default bodies run — against a
// local httptest.Server (never the real network) for Check, and against a
// Config whose release endpoint is a loopback address nothing listens on
// (so Update fails fast, before any write) for Update.

func TestCheckFunc_RealDefaultCallsConfigCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`))
	}))
	t.Cleanup(srv.Close)

	cfg := selfupdate.Config{
		BinaryName: "tool", Repository: "acme/tool", CurrentVersion: "1.0.0",
		ReleasesAPIURL: srv.URL, HTTPClient: srv.Client(),
	}
	cmd := New(cfg, CommandOptions{})
	out, _, err := runCmd(t, cmd, "--check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("stdout %q does not report up to date", out)
	}
}

func TestUpdateFunc_RealDefaultCallsConfigUpdate(t *testing.T) {
	cfg := selfupdate.Config{
		BinaryName: "tool", Repository: "acme/tool", CurrentVersion: "1.0.0",
		ReleasesAPIURL: "http://127.0.0.1:1", // nothing listens here: fails fast, no real network
		HTTPClient:     http.DefaultClient,
	}
	cmd := New(cfg, CommandOptions{Interactive: func() bool { return false }})
	_, _, err := runCmd(t, cmd, "--yes")
	if err == nil {
		t.Fatal("expected an error (ambiguous test-binary install or an unreachable release endpoint), got nil")
	}
}

// --- --check: two-contract coexistence (AC: two-cli-contracts-coexist) ---

func TestCheck_UpToDate_TextAndExitCode(t *testing.T) {
	withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.0.0", Verdict: selfupdate.UpToDate}, nil)
	cmd := New(testConfig(), CommandOptions{Errors: wbStyleErrors{}})

	out, _, err := runCmd(t, cmd, "--check")
	if err != nil {
		t.Fatalf("up-to-date --check returned error (want nil/exit 0): %v", err)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("stdout %q does not report up to date", out)
	}
}

// The two ErrorMappers give DIFFERENT exit codes for the exact same
// "update available" verdict — proving each consumer keeps its own
// contract when built from the same package (AC: two-cli-contracts-coexist).
func TestCheck_UpdateAvailable_DivergentExitCodes(t *testing.T) {
	result := selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable}

	t.Run("wb-style folds into general findings code", func(t *testing.T) {
		withStubCheck(t, result, nil)
		var calls int
		cmd := New(testConfig(), CommandOptions{Errors: wbStyleErrors{updateAvailableCalls: &calls}})

		out, _, err := runCmd(t, cmd, "--check")
		if err == nil {
			t.Fatal("expected non-nil error for an available update")
		}
		ec, ok := err.(exitCoder)
		if !ok {
			t.Fatalf("error %T does not expose ExitCode()", err)
		}
		if ec.ExitCode() != 1 {
			t.Errorf("wb-style exit code = %d, want 1 (general findings)", ec.ExitCode())
		}
		if calls != 1 {
			t.Errorf("UpdateAvailable called %d times, want 1", calls)
		}
		if !strings.Contains(out, "1.0.0") || !strings.Contains(out, "1.1.0") {
			t.Errorf("stdout %q does not report the available update", out)
		}
	})

	t.Run("specscore-style reserves a dedicated code", func(t *testing.T) {
		withStubCheck(t, result, nil)
		var calls int
		cmd := New(testConfig(), CommandOptions{Errors: specscoreStyleErrors{updateAvailableCalls: &calls}})

		_, _, err := runCmd(t, cmd, "--check")
		if err == nil {
			t.Fatal("expected non-nil error for an available update")
		}
		ec, ok := err.(exitCoder)
		if !ok {
			t.Fatalf("error %T does not expose ExitCode()", err)
		}
		if ec.ExitCode() != 10 {
			t.Errorf("specscore-style exit code = %d, want 10 (dedicated)", ec.ExitCode())
		}
		if calls != 1 {
			t.Errorf("UpdateAvailable called %d times, want 1", calls)
		}
	})
}

// Undetermined is also "not up to date" and must route through
// UpdateAvailable too (both contracts want it).
func TestCheck_Undetermined_RoutesThroughUpdateAvailable(t *testing.T) {
	withStubCheck(t, selfupdate.CheckResult{Current: "dev", Latest: "1.1.0", Verdict: selfupdate.Undetermined}, nil)
	var calls int
	cmd := New(testConfig(), CommandOptions{Errors: specscoreStyleErrors{updateAvailableCalls: &calls}})

	out, _, err := runCmd(t, cmd, "--check")
	if err == nil {
		t.Fatal("expected non-nil error for an undetermined verdict")
	}
	if calls != 1 {
		t.Errorf("UpdateAvailable called %d times, want 1", calls)
	}
	if !strings.Contains(strings.ToLower(out), "undetermined") {
		t.Errorf("stdout %q does not mention undetermined", out)
	}
}

// A release-lookup failure routes through Failure, not UpdateAvailable, and
// each mapper's own code survives.
func TestCheck_Failure_MapperReceivesIt(t *testing.T) {
	lookupErr := &selfupdate.Failure{Kind: selfupdate.KindReleaseLookup, Err: errors.New("network down")}
	withStubCheck(t, selfupdate.CheckResult{}, lookupErr)

	cmd := New(testConfig(), CommandOptions{Errors: specscoreStyleErrors{}})
	_, _, err := runCmd(t, cmd, "--check")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ec, ok := err.(exitCoder)
	if !ok || ec.ExitCode() != 4 {
		t.Errorf("error = %v, want specscore-style general failure code 4", err)
	}
}

// errWriter is an io.Writer stub whose Write always fails, used to exercise
// the error-return branches that only trigger when writing to the host's
// own io.Writer fails (e.g. a closed pipe or a full disk on the real CLI's
// stdout).
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// runCheck's --format json branch propagates a failing writer's error
// instead of swallowing it.
func TestRunCheck_JSONEncodeWriteError(t *testing.T) {
	withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.0.0", Verdict: selfupdate.UpToDate}, nil)
	cmd := New(testConfig(), CommandOptions{JSONFormat: true})
	cmd.SetOut(errWriter{err: errors.New("write fail")})
	cmd.SetArgs([]string{"--check", "--format", "json"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected the writer's error to propagate, got nil")
	}
}

func TestCheck_JSONFormat(t *testing.T) {
	withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable}, nil)
	cmd := New(testConfig(), CommandOptions{JSONFormat: true})

	out, _, err := runCmd(t, cmd, "--check", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Current, Latest, Verdict string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if got.Current != "1.0.0" || got.Latest != "1.1.0" || got.Verdict != "update_available" {
		t.Errorf("decoded = %+v", got)
	}
}

// A nil Errors mapper: --check with an available update just returns the
// underlying error unchanged (no ExitCode()), rather than panicking.
func TestCheck_NilErrorMapper(t *testing.T) {
	withStubCheck(t, selfupdate.CheckResult{Verdict: selfupdate.UpdateAvailable}, nil)
	cmd := New(testConfig(), CommandOptions{})
	_, _, err := runCmd(t, cmd, "--check")
	if err != nil {
		t.Errorf("nil Errors mapper must not synthesize an error: got %v", err)
	}
}

// --- update: flag -> Options wiring ---

func TestUpdate_FlagsMapToOptions(t *testing.T) {
	var captured selfupdate.Options
	withStubUpdate(t, &captured, selfupdate.Outcome{Action: selfupdate.ActionUpdated, Target: "1.2.3"}, nil)

	cmd := New(testConfig(), CommandOptions{})
	_, _, err := runCmd(t, cmd, "--version", "1.2.3", "--allow-downgrade", "--dry-run", "--yes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.PinnedVersion != "1.2.3" {
		t.Errorf("PinnedVersion = %q, want 1.2.3", captured.PinnedVersion)
	}
	if !captured.AllowDowngrade {
		t.Error("AllowDowngrade = false, want true")
	}
	if !captured.DryRun {
		t.Error("DryRun = false, want true")
	}
	if captured.Confirm == nil {
		t.Fatal("Confirm is nil; the command must always supply a confirm callback")
	}
	if captured.RunManaged == nil {
		t.Fatal("RunManaged is nil; the Cobra adapter must supply the streaming argv runner")
	}
	if captured.VerifyManaged == nil {
		t.Fatal("VerifyManaged is nil; the Cobra adapter must supply the post-update probe")
	}
}

// --yes causes Confirm to proceed immediately without invoking Interactive.
func TestUpdate_YesSkipsInteractiveCheck(t *testing.T) {
	var captured selfupdate.Options
	withStubUpdate(t, &captured, selfupdate.Outcome{Action: selfupdate.ActionUpdated, Target: "1.1.0"}, nil)

	interactiveCalled := false
	cmd := New(testConfig(), CommandOptions{Interactive: func() bool { interactiveCalled = true; return false }})
	_, _, err := runCmd(t, cmd, "--yes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	proceed, cerr := captured.Confirm("1.0.0 → 1.1.0")
	if cerr != nil || !proceed {
		t.Fatalf("Confirm with --yes = (%v, %v), want (true, nil)", proceed, cerr)
	}
	if interactiveCalled {
		t.Error("Interactive() was called despite --yes; must be skipped")
	}
}

// REQ: non-interactive-refusal — without --yes and without a TTY, Confirm
// must refuse via a *selfupdate.Failure{Kind: KindNonInteractive}, which
// Update (real or stubbed) would propagate as the command's error.
func TestUpdate_NonInteractiveRefusal(t *testing.T) {
	var captured selfupdate.Options
	withStubUpdate(t, &captured, selfupdate.Outcome{}, nil)

	cmd := New(testConfig(), CommandOptions{Interactive: func() bool { return false }})
	_, _, err := runCmd(t, cmd) // no --yes
	if err != nil {
		t.Fatalf("unexpected error from the stubbed Update: %v", err)
	}
	proceed, cerr := captured.Confirm("1.0.0 → 1.1.0")
	if proceed {
		t.Error("Confirm proceeded without --yes and without a TTY")
	}
	if selfupdate.KindOf(cerr) != selfupdate.KindNonInteractive {
		t.Errorf("KindOf(Confirm error) = %v, want KindNonInteractive", selfupdate.KindOf(cerr))
	}
}

// With a TTY and no --yes, Confirm reads a line from stdin and proceeds
// only on "y"/"yes".
func TestUpdate_InteractivePromptReadsStdin(t *testing.T) {
	var captured selfupdate.Options
	withStubUpdate(t, &captured, selfupdate.Outcome{}, nil)

	cmd := New(testConfig(), CommandOptions{Interactive: func() bool { return true }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("y\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	proceed, cerr := captured.Confirm("1.0.0 → 1.1.0")
	if cerr != nil || !proceed {
		t.Fatalf("Confirm with 'y' input = (%v, %v), want (true, nil)", proceed, cerr)
	}
	if !strings.Contains(out.String(), "Proceed?") {
		t.Errorf("stdout %q does not contain a confirmation prompt", out.String())
	}
}

// --- update: outcome formatting ---

func TestUpdate_ManagedRedirectText(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade tool")
	withStubUpdate(t, nil, selfupdate.Outcome{
		Action:    selfupdate.ActionRedirected,
		Detection: selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr},
	}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Homebrew") || !strings.Contains(out, "brew upgrade tool") {
		t.Errorf("stdout %q does not name the manager and its upgrade command", out)
	}
}

func TestUpdate_AmbiguousPrintsGuidanceAndMapsFailure(t *testing.T) {
	ambErr := &selfupdate.Failure{Kind: selfupdate.KindAmbiguous, Path: "/opt/x/tool", Err: errors.New("ambiguous")}
	withStubUpdate(t, nil, selfupdate.Outcome{}, ambErr)

	cmd := New(testConfig(), CommandOptions{Errors: wbStyleErrors{}})
	out, _, err := runCmd(t, cmd)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ec, ok := err.(exitCoder)
	if !ok || ec.ExitCode() != 1 {
		t.Errorf("error = %v, want wb-style code 1", err)
	}
	if !strings.Contains(strings.ToLower(out), "ambiguous") {
		t.Errorf("stdout %q does not print ambiguous-install guidance", out)
	}
	if !strings.Contains(out, "acme/tool") {
		t.Errorf("stdout %q does not reference the repository for manual download", out)
	}
}

// printAmbiguousGuidance's per-manager suffix loop only runs when
// cfg.Managers is non-empty — TestUpdate_AmbiguousPrintsGuidanceAndMapsFailure
// above uses a Manager-less testConfig(), so it never reaches that loop body.
func TestUpdate_AmbiguousPrintsGuidanceWithManagers(t *testing.T) {
	ambErr := &selfupdate.Failure{Kind: selfupdate.KindAmbiguous, Path: "/opt/x/tool", Err: errors.New("ambiguous")}
	withStubUpdate(t, nil, selfupdate.Outcome{}, ambErr)

	cfg := testConfig()
	cfg.Managers = []selfupdate.Manager{
		selfupdate.Homebrew("brew upgrade tool"),
		selfupdate.Scoop("scoop update tool"),
	}
	cmd := New(cfg, CommandOptions{})
	out, _, err := runCmd(t, cmd)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(out, "brew upgrade tool") || !strings.Contains(out, "scoop update tool") {
		t.Errorf("stdout %q does not list every configured manager's upgrade command", out)
	}
}

func TestUpdate_AlreadyCurrentText(t *testing.T) {
	withStubUpdate(t, nil, selfupdate.Outcome{
		Action: selfupdate.ActionAlreadyCurrent,
		Result: selfupdate.CheckResult{Current: "1.0.0", Verdict: selfupdate.UpToDate},
	}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "up to date") || !strings.Contains(out, "1.0.0") {
		t.Errorf("stdout %q does not report up to date", out)
	}
}

func TestUpdate_AbortedText(t *testing.T) {
	withStubUpdate(t, nil, selfupdate.Outcome{Action: selfupdate.ActionAborted}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "aborted") {
		t.Errorf("stdout %q does not report the abort", out)
	}
}

func TestUpdate_PlannedDryRunText(t *testing.T) {
	withStubUpdate(t, nil, selfupdate.Outcome{
		Action:     selfupdate.ActionPlanned,
		Result:     selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0"},
		Target:     "1.1.0",
		PlannedURL: "https://github.com/acme/tool/releases/download/v1.1.0/tool_1.1.0_linux_amd64.tar.gz",
	}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("stdout %q does not say 'dry run'", out)
	}
	if !strings.Contains(out, "tool_1.1.0_linux_amd64.tar.gz") {
		t.Errorf("stdout %q does not contain the planned asset URL", out)
	}
}

// The ActionPlanned case's verb switches to "downgrade" when Outcome.
// Downgrade is set — TestUpdate_PlannedDryRunText above only covers the
// (default) "update" verb.
func TestUpdate_PlannedDryRunDowngradeText(t *testing.T) {
	withStubUpdate(t, nil, selfupdate.Outcome{
		Action:     selfupdate.ActionPlanned,
		Result:     selfupdate.CheckResult{Current: "1.1.0", Latest: "1.0.0"},
		Target:     "1.0.0",
		Downgrade:  true,
		PlannedURL: "https://github.com/acme/tool/releases/download/v1.0.0/tool_1.0.0_linux_amd64.tar.gz",
	}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "dry run: would downgrade") {
		t.Errorf("stdout %q does not say 'dry run: would downgrade'", out)
	}
}

func TestUpdate_UpdatedTextAndJSON(t *testing.T) {
	withStubUpdate(t, nil, selfupdate.Outcome{
		Action: selfupdate.ActionUpdated,
		Result: selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0"},
		Target: "1.1.0",
	}, nil)

	cmd := New(testConfig(), CommandOptions{JSONFormat: true})
	out, _, err := runCmd(t, cmd, "--yes", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Action, Target string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if got.Action != "updated" || got.Target != "1.1.0" {
		t.Errorf("decoded = %+v", got)
	}
}

// A post-swap warning is surfaced on stderr without failing the command.
func TestUpdate_PostSwapWarningOnStderr(t *testing.T) {
	withStubUpdate(t, nil, selfupdate.Outcome{
		Action:          selfupdate.ActionUpdated,
		Target:          "1.1.0",
		PostSwapWarning: errors.New("version probe mismatch"),
	}, nil)

	cmd := New(testConfig(), CommandOptions{})
	_, errOut, err := runCmd(t, cmd, "--yes")
	if err != nil {
		t.Fatalf("a post-swap warning must not fail the command: %v", err)
	}
	if !strings.Contains(errOut, "version probe mismatch") {
		t.Errorf("stderr %q does not contain the post-swap warning", errOut)
	}
}

// A non-ambiguous failure (e.g. checksum mismatch) is mapped without the
// ambiguous-guidance text.
func TestUpdate_NonAmbiguousFailureNoGuidance(t *testing.T) {
	checksumErr := &selfupdate.Failure{Kind: selfupdate.KindChecksum, Err: errors.New("mismatch")}
	withStubUpdate(t, nil, selfupdate.Outcome{}, checksumErr)

	cmd := New(testConfig(), CommandOptions{Errors: specscoreStyleErrors{}})
	out, _, err := runCmd(t, cmd, "--yes")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ec, ok := err.(exitCoder)
	if !ok || ec.ExitCode() != 4 {
		t.Errorf("error = %v, want specscore-style general failure code 4", err)
	}
	if strings.Contains(strings.ToLower(out), "ambiguous") {
		t.Errorf("stdout %q wrongly prints ambiguous guidance for a checksum failure", out)
	}
}

// A nil Errors mapper on the update path returns the underlying error
// unchanged (mapFailure's other branch, mirrored from TestCheck_NilErrorMapper).
func TestUpdate_NilErrorMapperFailurePassthrough(t *testing.T) {
	original := &selfupdate.Failure{Kind: selfupdate.KindDownload, Err: errors.New("boom")}
	withStubUpdate(t, nil, selfupdate.Outcome{}, original)

	cmd := New(testConfig(), CommandOptions{})
	_, _, err := runCmd(t, cmd, "--yes")
	if err != error(original) {
		t.Errorf("nil Errors mapper must return the underlying error unchanged, got %v", err)
	}
}

// The terminal-check seam that used to live here (defaultIsInteractive /
// terminalCheck, including the /dev/null-is-not-a-terminal regression test)
// moved to cliui.IsTerminal along with the rest of the confirmation logic —
// see cliui/confirm_test.go. CommandOptions.Interactive is now passed
// straight through to cliui.Confirm, which owns the nil-default.

// When the terminal probe says interactive but the read yields nothing,
// nobody was actually asked. That is a non-interactive refusal — non-zero
// exit — and never a user declining, which would exit 0.
func TestUpdate_EmptyStdinRefusesInsteadOfAborting(t *testing.T) {
	var captured selfupdate.Options
	withStubUpdate(t, &captured, selfupdate.Outcome{}, nil)

	cmd := New(testConfig(), CommandOptions{Interactive: func() bool { return true }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(""))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	proceed, cerr := captured.Confirm("1.0.0 → 1.1.0")
	if proceed {
		t.Error("Confirm proceeded on empty stdin")
	}
	if selfupdate.KindOf(cerr) != selfupdate.KindNonInteractive {
		t.Errorf("Confirm on empty stdin returned %v (kind %v), want a KindNonInteractive failure",
			cerr, selfupdate.KindOf(cerr))
	}
}

// A user who is asked and answers "n" is a different outcome: (false, nil),
// which the core reports as ActionAborted and the host exits 0 on.
func TestUpdate_ExplicitNoIsAnAbortNotAFailure(t *testing.T) {
	var captured selfupdate.Options
	withStubUpdate(t, &captured, selfupdate.Outcome{}, nil)

	cmd := New(testConfig(), CommandOptions{Interactive: func() bool { return true }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("n\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	proceed, cerr := captured.Confirm("1.0.0 → 1.1.0")
	if proceed || cerr != nil {
		t.Errorf("Confirm with 'n' = (%v, %v), want (false, nil)", proceed, cerr)
	}
}

// JSON formatting for every Action, including the redirected/aborted/
// already-current shapes that TestUpdate_UpdatedTextAndJSON doesn't cover,
// and the downgrade/warning fields.
func TestWriteOutcomeJSON_AllActions(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade tool")
	cases := []struct {
		name    string
		outcome selfupdate.Outcome
		wantAct string
	}{
		{"redirected", selfupdate.Outcome{Action: selfupdate.ActionRedirected, Detection: selfupdate.Detection{Manager: &mgr}}, "redirected"},
		{"already_current", selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent, Result: selfupdate.CheckResult{Current: "1.0.0"}}, "already_current"},
		{"aborted", selfupdate.Outcome{Action: selfupdate.ActionAborted, Target: "1.1.0"}, "aborted"},
		{"planned", selfupdate.Outcome{Action: selfupdate.ActionPlanned, Target: "1.1.0", PlannedURL: "https://example.test/asset"}, "planned"},
		{"updated_with_downgrade_and_warning", selfupdate.Outcome{
			Action: selfupdate.ActionUpdated, Target: "0.9.0", Downgrade: true,
			PostSwapWarning: errors.New("mismatch"),
		}, "updated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withStubUpdate(t, nil, c.outcome, nil)
			cmd := New(testConfig(), CommandOptions{JSONFormat: true})
			out, _, err := runCmd(t, cmd, "--yes", "--format", "json")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if jsonErr := json.Unmarshal([]byte(out), &got); jsonErr != nil {
				t.Fatalf("output is not valid JSON: %v\noutput: %s", jsonErr, out)
			}
			if got["action"] != c.wantAct {
				t.Errorf("action = %v, want %q", got["action"], c.wantAct)
			}
		})
	}
}

// withStubDetect pins the install classification so --check's next-step
// guidance is exercisable without the test binary's own path deciding it.
func withStubDetect(t *testing.T, detection selfupdate.Detection, err error) {
	t.Helper()
	prev := detectFunc
	detectFunc = func(selfupdate.Config) (selfupdate.Detection, error) { return detection, err }
	t.Cleanup(func() { detectFunc = prev })
}

// "An update exists" is only half an answer: what the user does next differs
// entirely between a managed install (go through the manager) and a manual
// one (run this command). A check that withholds it forces the user to run
// the real thing to find out.
func TestCheck_StatesTheNextStepPerInstallMethod(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool")
	cases := []struct {
		name      string
		detection selfupdate.Detection
		detectErr error
		want      []string
		absent    []string
	}{
		{
			name:      "managed names the manager command",
			detection: selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr},
			want:      []string{"update available: 1.0.0 → 1.1.0", "Homebrew", "brew upgrade --cask tool"},
		},
		{
			name:      "manual names this very command",
			detection: selfupdate.Detection{Method: selfupdate.Manual},
			want:      []string{"To upgrade, run: self-update"},
		},
		{
			name:      "ambiguous prints the refusal guidance",
			detection: selfupdate.Detection{Method: selfupdate.Ambiguous},
			want:      []string{"ambiguous"},
		},
		{
			// Detection is a path classification, not a network call — but if
			// it fails, the version comparison is still worth reporting, so
			// the check must not fail with it.
			name:      "a detection failure still reports the comparison",
			detectErr: errors.New("cannot resolve executable"),
			want:      []string{"update available: 1.0.0 → 1.1.0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable}, nil)
			withStubDetect(t, tc.detection, tc.detectErr)

			cmd := New(testConfig(), CommandOptions{})
			out, _, err := runCmd(t, cmd, "--check")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("check output missing %q:\n%s", want, out)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("check output unexpectedly contains %q:\n%s", absent, out)
				}
			}
		})
	}
}

// An up-to-date binary needs no next step — there is nothing to do.
func TestCheck_UpToDateHasNoNextStep(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool")
	withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.0.0", Verdict: selfupdate.UpToDate}, nil)
	withStubDetect(t, selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd, "--check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "brew upgrade") {
		t.Errorf("an up-to-date check told the user to upgrade:\n%s", out)
	}
}

// A managed classification that carries no manager is a contradiction; it
// must fall back to guidance rather than print a half-sentence.
func TestCheck_ManagedWithoutManagerFallsBackToGuidance(t *testing.T) {
	withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable}, nil)
	withStubDetect(t, selfupdate.Detection{Method: selfupdate.Managed}, nil)

	cmd := New(testConfig(), CommandOptions{})
	out, _, err := runCmd(t, cmd, "--check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ambiguous") {
		t.Errorf("managed-without-manager did not fall back to guidance:\n%s", out)
	}
}

// The JSON shape carries the same facts the text states, so a machine caller
// can decide the next step without parsing prose.
func TestCheck_JSONCarriesInstallMethod(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool")
	withStubCheck(t, selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable}, nil)
	withStubDetect(t, selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr}, nil)

	cmd := New(testConfig(), CommandOptions{JSONFormat: true})
	out, _, err := runCmd(t, cmd, "--check", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if uerr := json.Unmarshal([]byte(out), &got); uerr != nil {
		t.Fatalf("check JSON does not parse: %v\n%s", uerr, out)
	}
	for key, want := range map[string]string{
		"verdict":         "update_available",
		"install_method":  "managed",
		"manager":         "Homebrew",
		"upgrade_command": "brew upgrade --cask tool",
	} {
		if got[key] != want {
			t.Errorf("check JSON[%q] = %v, want %q\n%s", key, got[key], want, out)
		}
	}
}
