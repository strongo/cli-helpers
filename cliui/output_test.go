package cliui

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/strongo/selfupdate"
)

func testConfig() selfupdate.Config {
	return selfupdate.Config{BinaryName: "tool", Repository: "acme/tool", CurrentVersion: "1.0.0"}
}

// errWriter is an io.Writer stub whose Write always fails, used to exercise
// the error-return branch of the JSON writers.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// --- WriteOutcome ---

func TestWriteOutcome_ManagedRedirect(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade tool")
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{
		Action:    selfupdate.ActionRedirected,
		Detection: selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr},
	})
	if !strings.Contains(out.String(), "Homebrew") || !strings.Contains(out.String(), "brew upgrade tool") {
		t.Errorf("stdout %q does not name the manager and its upgrade command", out.String())
	}
}

func TestWriteOutcome_ManagerExecuted(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool").WithExecutableUpgrade("brew", "upgrade", "--cask", "tool")
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{
		Action:    selfupdate.ActionManagerExecuted,
		Detection: selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr},
	})
	if !strings.Contains(out.String(), "Homebrew") || !strings.Contains(out.String(), "completed") {
		t.Errorf("stdout %q does not report the completed manager update", out.String())
	}
}

// A redirected outcome without a Manager (a contradiction the caller should
// never actually produce) prints nothing rather than a half-sentence.
func TestWriteOutcome_ManagedRedirectWithoutManagerPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{Action: selfupdate.ActionRedirected})
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty for a redirected outcome without a Manager", out.String())
	}
}

func TestWriteOutcome_AlreadyCurrent(t *testing.T) {
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{
		Action: selfupdate.ActionAlreadyCurrent,
		Result: selfupdate.CheckResult{Current: "1.0.0"},
	})
	if !strings.Contains(out.String(), "up to date") || !strings.Contains(out.String(), "1.0.0") {
		t.Errorf("stdout %q does not report up to date", out.String())
	}
}

func TestWriteOutcome_Aborted(t *testing.T) {
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{Action: selfupdate.ActionAborted})
	if !strings.Contains(strings.ToLower(out.String()), "aborted") {
		t.Errorf("stdout %q does not report the abort", out.String())
	}
}

func TestWriteOutcome_PlannedUpdate(t *testing.T) {
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{
		Action:     selfupdate.ActionPlanned,
		Result:     selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0"},
		Target:     "1.1.0",
		PlannedURL: "https://github.com/acme/tool/releases/download/v1.1.0/tool_1.1.0_linux_amd64.tar.gz",
	})
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("stdout %q does not say 'dry run'", out.String())
	}
	if !strings.Contains(out.String(), "tool_1.1.0_linux_amd64.tar.gz") {
		t.Errorf("stdout %q does not contain the planned asset URL", out.String())
	}
}

func TestWriteOutcome_PlannedManagerCommand(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool").WithExecutableUpgrade("brew", "upgrade", "--cask", "tool")
	outcome := selfupdate.Outcome{
		Action:         selfupdate.ActionPlanned,
		Detection:      selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr},
		PlannedCommand: "brew upgrade --cask tool",
	}
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), outcome)
	if !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), "brew upgrade --cask tool") {
		t.Errorf("stdout %q does not report the planned manager command", out.String())
	}

	out.Reset()
	if err := WriteOutcomeJSON(&out, outcome); err != nil {
		t.Fatalf("unexpected JSON error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("outcome JSON does not parse: %v\n%s", err, out.String())
	}
	if got["command"] != "brew upgrade --cask tool" {
		t.Errorf("command = %v, want planned manager command", got["command"])
	}
}

func TestWriteOutcome_PlannedDowngrade(t *testing.T) {
	var out bytes.Buffer
	WriteOutcome(&out, &bytes.Buffer{}, testConfig(), selfupdate.Outcome{
		Action:     selfupdate.ActionPlanned,
		Result:     selfupdate.CheckResult{Current: "1.1.0", Latest: "1.0.0"},
		Target:     "1.0.0",
		Downgrade:  true,
		PlannedURL: "https://github.com/acme/tool/releases/download/v1.0.0/tool_1.0.0_linux_amd64.tar.gz",
	})
	if !strings.Contains(out.String(), "dry run: would downgrade") {
		t.Errorf("stdout %q does not say 'dry run: would downgrade'", out.String())
	}
}

func TestWriteOutcome_UpdatedWithoutWarning(t *testing.T) {
	var out, errOut bytes.Buffer
	WriteOutcome(&out, &errOut, testConfig(), selfupdate.Outcome{Action: selfupdate.ActionUpdated, Target: "1.1.0"})
	if !strings.Contains(out.String(), "updated to 1.1.0") {
		t.Errorf("stdout %q does not report the update", out.String())
	}
	if errOut.String() != "" {
		t.Errorf("stderr = %q, want empty without a post-swap warning", errOut.String())
	}
}

// A post-swap warning is surfaced on errOut without failing the command.
func TestWriteOutcome_UpdatedWithWarning(t *testing.T) {
	var out, errOut bytes.Buffer
	WriteOutcome(&out, &errOut, testConfig(), selfupdate.Outcome{
		Action:          selfupdate.ActionUpdated,
		Target:          "1.1.0",
		PostSwapWarning: errors.New("version probe mismatch"),
	})
	if !strings.Contains(errOut.String(), "version probe mismatch") {
		t.Errorf("stderr %q does not contain the post-swap warning", errOut.String())
	}
}

// --- WriteOutcomeJSON ---

func TestWriteOutcomeJSON_AllActions(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade tool")
	cases := []struct {
		name    string
		outcome selfupdate.Outcome
		wantAct string
	}{
		{"redirected", selfupdate.Outcome{Action: selfupdate.ActionRedirected, Detection: selfupdate.Detection{Manager: &mgr}}, "redirected"},
		{"redirected_no_manager", selfupdate.Outcome{Action: selfupdate.ActionRedirected}, "redirected"},
		{"already_current", selfupdate.Outcome{Action: selfupdate.ActionAlreadyCurrent, Result: selfupdate.CheckResult{Current: "1.0.0"}}, "already_current"},
		{"aborted", selfupdate.Outcome{Action: selfupdate.ActionAborted, Target: "1.1.0"}, "aborted"},
		{"updated", selfupdate.Outcome{Action: selfupdate.ActionUpdated, Target: "1.1.0"}, "updated"},
		{"manager_executed", selfupdate.Outcome{Action: selfupdate.ActionManagerExecuted, Detection: selfupdate.Detection{Manager: &mgr}}, "manager_executed"},
		{"planned", selfupdate.Outcome{Action: selfupdate.ActionPlanned, Target: "1.1.0", PlannedURL: "https://example.test/asset"}, "planned"},
		{"updated_with_downgrade_and_warning", selfupdate.Outcome{
			Action: selfupdate.ActionUpdated, Target: "0.9.0", Downgrade: true,
			PostSwapWarning: errors.New("mismatch"),
		}, "updated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := WriteOutcomeJSON(&out, c.outcome); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if jsonErr := json.Unmarshal(out.Bytes(), &got); jsonErr != nil {
				t.Fatalf("output is not valid JSON: %v\noutput: %s", jsonErr, out.String())
			}
			if got["action"] != c.wantAct {
				t.Errorf("action = %v, want %q", got["action"], c.wantAct)
			}
		})
	}
}

func TestWriteOutcomeJSON_WriteError(t *testing.T) {
	err := WriteOutcomeJSON(errWriter{err: errors.New("write fail")}, selfupdate.Outcome{Action: selfupdate.ActionUpdated})
	if err == nil {
		t.Fatal("expected the writer's error to propagate, got nil")
	}
}

// --- WriteCheck ---

func TestWriteCheck_UpToDate(t *testing.T) {
	var out bytes.Buffer
	WriteCheck(&out, testConfig(), selfupdate.CheckResult{Current: "1.0.0", Latest: "1.0.0", Verdict: selfupdate.UpToDate})
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("stdout %q does not report up to date", out.String())
	}
}

func TestWriteCheck_Undetermined(t *testing.T) {
	var out bytes.Buffer
	WriteCheck(&out, testConfig(), selfupdate.CheckResult{Current: "dev", Latest: "1.1.0", Verdict: selfupdate.Undetermined})
	if !strings.Contains(strings.ToLower(out.String()), "undetermined") {
		t.Errorf("stdout %q does not mention undetermined", out.String())
	}
}

func TestWriteCheck_UpdateAvailable(t *testing.T) {
	var out bytes.Buffer
	WriteCheck(&out, testConfig(), selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable})
	if !strings.Contains(out.String(), "1.0.0") || !strings.Contains(out.String(), "1.1.0") {
		t.Errorf("stdout %q does not report the available update", out.String())
	}
}

// --- WriteCheckJSON ---

func TestWriteCheckJSON_CarriesInstallMethod(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool")
	var out bytes.Buffer
	if err := WriteCheckJSON(&out, testConfig(),
		selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("check JSON does not parse: %v\n%s", err, out.String())
	}
	for key, want := range map[string]string{
		"current":         "1.0.0",
		"latest":          "1.1.0",
		"verdict":         "update_available",
		"install_method":  "managed",
		"manager":         "Homebrew",
		"upgrade_command": "brew upgrade --cask tool",
	} {
		if got[key] != want {
			t.Errorf("check JSON[%q] = %v, want %q\n%s", key, got[key], want, out.String())
		}
	}
}

func TestWriteCheckJSON_NoManagerOmitsManagerFields(t *testing.T) {
	var out bytes.Buffer
	if err := WriteCheckJSON(&out, testConfig(),
		selfupdate.CheckResult{Current: "1.0.0", Latest: "1.0.0", Verdict: selfupdate.UpToDate},
		selfupdate.Detection{Method: selfupdate.Manual}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("check JSON does not parse: %v\n%s", err, out.String())
	}
	if _, ok := got["manager"]; ok {
		t.Errorf("check JSON unexpectedly contains 'manager' with no Manager set:\n%s", out.String())
	}
}

func TestWriteCheckJSON_ExecutableManagerIsMachineReadable(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool").WithExecutableUpgrade("brew", "upgrade", "--cask", "tool")
	var out bytes.Buffer
	if err := WriteCheckJSON(&out, testConfig(),
		selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("check JSON does not parse: %v\n%s", err, out.String())
	}
	if got["managed_update_executable"] != true {
		t.Errorf("managed_update_executable = %v, want true\n%s", got["managed_update_executable"], out.String())
	}
}

func TestWriteCheckJSON_WriteError(t *testing.T) {
	err := WriteCheckJSON(errWriter{err: errors.New("write fail")}, testConfig(),
		selfupdate.CheckResult{Verdict: selfupdate.UpToDate}, selfupdate.Detection{})
	if err == nil {
		t.Fatal("expected the writer's error to propagate, got nil")
	}
}

// --- WriteNextStep ---

func TestWriteNextStep_PerInstallMethod(t *testing.T) {
	mgr := selfupdate.Homebrew("brew upgrade --cask tool")
	cases := []struct {
		name      string
		detection selfupdate.Detection
		want      []string
	}{
		{
			name:      "managed names the manager command",
			detection: selfupdate.Detection{Method: selfupdate.Managed, Manager: &mgr},
			want:      []string{"Homebrew", "brew upgrade --cask tool"},
		},
		{
			name: "executable manager points back to self-update",
			detection: func() selfupdate.Detection {
				executable := selfupdate.Homebrew("brew upgrade --cask tool").WithExecutableUpgrade("brew", "upgrade", "--cask", "tool")
				return selfupdate.Detection{Method: selfupdate.Managed, Manager: &executable}
			}(),
			want: []string{"through Homebrew", "tool self-update"},
		},
		{
			name:      "manual names this very command",
			detection: selfupdate.Detection{Method: selfupdate.Manual},
			want:      []string{"To upgrade, run: tool self-update"},
		},
		{
			name:      "ambiguous prints the refusal guidance",
			detection: selfupdate.Detection{Method: selfupdate.Ambiguous},
			want:      []string{"ambiguous"},
		},
		{
			name:      "managed without a manager falls back to guidance",
			detection: selfupdate.Detection{Method: selfupdate.Managed},
			want:      []string{"ambiguous"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			WriteNextStep(&out, testConfig(), tc.detection, "tool self-update")
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("next-step output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

// --- WriteAmbiguousGuidance ---

func TestWriteAmbiguousGuidance_NoManagers(t *testing.T) {
	var out bytes.Buffer
	WriteAmbiguousGuidance(&out, testConfig())
	if !strings.Contains(strings.ToLower(out.String()), "ambiguous") {
		t.Errorf("stdout %q does not print ambiguous-install guidance", out.String())
	}
	if !strings.Contains(out.String(), "acme/tool") {
		t.Errorf("stdout %q does not reference the repository for manual download", out.String())
	}
}

func TestWriteAmbiguousGuidance_ListsEveryManager(t *testing.T) {
	cfg := testConfig()
	cfg.Managers = []selfupdate.Manager{
		selfupdate.Homebrew("brew upgrade tool"),
		selfupdate.Scoop("scoop update tool"),
	}
	var out bytes.Buffer
	WriteAmbiguousGuidance(&out, cfg)
	if !strings.Contains(out.String(), "brew upgrade tool") || !strings.Contains(out.String(), "scoop update tool") {
		t.Errorf("stdout %q does not list every configured manager's upgrade command", out.String())
	}
}
