package cliinstall

import (
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

// --- Method / Outcome String() ---------------------------------------------

func TestMethod_String(t *testing.T) {
	cases := map[Method]string{MethodDirect: "direct", MethodHomebrew: "homebrew", Method(99): "unknown"}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Method(%d).String() = %q, want %q", m, got, want)
		}
	}
}

func TestOutcome_String(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeInstalled:        "installed",
		OutcomeAlreadyInstalled: "already_installed",
		OutcomeRedirected:       "redirected",
		OutcomeDryRun:           "dry_run",
		OutcomeDeclined:         "declined",
		OutcomeFailed:           "failed",
		Outcome(99):             "unknown",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", o, got, want)
		}
	}
}

// --- BatchResult.Failed -----------------------------------------------------

func TestBatchResult_Failed(t *testing.T) {
	ok := BatchResult{Results: []Result{{Outcome: OutcomeInstalled}, {Outcome: OutcomeAlreadyInstalled}}}
	if ok.Failed() {
		t.Error("Failed() = true, want false when no result failed")
	}
	failed := BatchResult{Results: []Result{{Outcome: OutcomeInstalled}, {Outcome: OutcomeFailed}}}
	if !failed.Failed() {
		t.Error("Failed() = false, want true when a result failed")
	}
}

// --- DefaultInstallEnv -------------------------------------------------------

func TestDefaultInstallEnv_AllFieldsSet(t *testing.T) {
	env := DefaultInstallEnv()
	if env.PathDirs == nil || env.HostDir == nil || env.IsExecutable == nil || env.Run == nil {
		t.Fatal("DefaultInstallEnv() left an Env field nil")
	}
	if env.UserHomeDir == nil || env.Getenv == nil || env.MkdirAll == nil {
		t.Fatal("DefaultInstallEnv() left an install-specific field nil")
	}
	// RunManaged is deliberately left nil: it needs caller-owned I/O
	// streams this core layer must not assume
	// (cli-install#req:core-framework-neutral).
	if env.RunManaged != nil {
		t.Error("DefaultInstallEnv().RunManaged != nil, want nil (the caller must wire its own streams)")
	}
}

// --- caskArgv / shellCacheRefreshHint ---------------------------------------

func TestCaskArgv(t *testing.T) {
	if got := caskArgv(""); got != nil {
		t.Errorf("caskArgv(\"\") = %v, want nil", got)
	}
	want := []string{"brew", "install", "--cask", "acme/tap/thing"}
	got := caskArgv("acme/tap/thing")
	if len(got) != len(want) {
		t.Fatalf("caskArgv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("caskArgv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestShellCacheRefreshHint(t *testing.T) {
	if got := shellCacheRefreshHint("ovdb"); !strings.Contains(got, "hash -r") || !strings.Contains(got, "ovdb") {
		t.Errorf("shellCacheRefreshHint = %q, want it to name hash -r and the target", got)
	}
}

// --- shadowWarning / dirOf / indexOfDir -------------------------------------

func TestShadowWarning_NotUnrecognized(t *testing.T) {
	status := Status{State: Installed, OnPath: true, Path: "/bin1/ovdb"}
	if got := shadowWarning([]string{"/bin1"}, status, "/bin2"); got != "" {
		t.Errorf("shadowWarning = %q, want empty for a non-Unrecognized status", got)
	}
}

func TestShadowWarning_UnrecognizedNotOnPath(t *testing.T) {
	status := Status{State: Unrecognized, OnPath: false, Path: "/host/dir/ovdb"}
	if got := shadowWarning([]string{"/bin1"}, status, "/bin2"); got != "" {
		t.Errorf("shadowWarning = %q, want empty when the unrecognized copy is not on PATH", got)
	}
}

func TestShadowWarning_UnrecognizedDirNotFoundInPathDirs(t *testing.T) {
	// OnPath is true but its directory is absent from pathDirs, an
	// internally-inconsistent case that must not panic or false-positive.
	status := Status{State: Unrecognized, OnPath: true, Path: "/gone/ovdb"}
	if got := shadowWarning([]string{"/bin1"}, status, "/bin2"); got != "" {
		t.Errorf("shadowWarning = %q, want empty", got)
	}
}

func TestShadowWarning_EarlierOnPathWarns(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	status := Status{State: Unrecognized, OnPath: true, Path: "/bin1/ovdb"}
	got := shadowWarning([]string{"/bin1", "/bin2"}, status, "/bin2")
	if got == "" || !strings.Contains(got, "/bin1/ovdb") {
		t.Errorf("shadowWarning = %q, want a warning naming the shadowing copy", got)
	}
}

func TestShadowWarning_DestNotOnPathWarns(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	status := Status{State: Unrecognized, OnPath: true, Path: "/bin1/ovdb"}
	got := shadowWarning([]string{"/bin1"}, status, "/not/on/path")
	if got == "" {
		t.Error("shadowWarning = \"\", want a warning when the destination is not on PATH at all")
	}
}

func TestShadowWarning_DestEarlierNoWarning(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	status := Status{State: Unrecognized, OnPath: true, Path: "/bin2/ovdb"}
	got := shadowWarning([]string{"/bin1", "/bin2"}, status, "/bin1")
	if got != "" {
		t.Errorf("shadowWarning = %q, want empty when destination is earlier on PATH", got)
	}
}

func TestDirOf(t *testing.T) {
	if got := dirOf("/bin1/ovdb"); got != "/bin1" {
		t.Errorf("dirOf = %q", got)
	}
	if got := dirOf("ovdb"); got != "ovdb" {
		t.Errorf("dirOf(no slash) = %q, want unchanged", got)
	}
}

func TestIndexOfDir(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	dirs := []string{"/bin1", "/bin2"}
	if got := indexOfDir(dirs, "/bin2"); got != 1 {
		t.Errorf("indexOfDir = %d, want 1", got)
	}
	if got := indexOfDir(dirs, "/nowhere"); got != -1 {
		t.Errorf("indexOfDir = %d, want -1", got)
	}
}

// --- alreadyInstalledResult / unknownTargetFailure / dedupeNames -----------

func TestAlreadyInstalledResult(t *testing.T) {
	target := Entry{ID: "ovdb"}
	status := Status{Version: "1.2.3", Warnings: []string{"a warning"}}
	got := alreadyInstalledResult(target, status)
	if got.Target != "ovdb" || got.Outcome != OutcomeAlreadyInstalled || got.Version != "1.2.3" {
		t.Errorf("alreadyInstalledResult = %+v", got)
	}
	if got.UpdateHint != "ovdb self-update" {
		t.Errorf("UpdateHint = %q, want %q", got.UpdateHint, "ovdb self-update")
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "a warning" {
		t.Errorf("Warnings = %v, want status's own warnings carried through", got.Warnings)
	}
}

func TestUnknownTargetFailure(t *testing.T) {
	f := unknownTargetFailure("nosuchcli")
	if f.Kind != selfupdate.KindUnknownTarget {
		t.Errorf("Kind = %v, want KindUnknownTarget", f.Kind)
	}
	if !strings.Contains(f.Error(), "nosuchcli") {
		t.Errorf("Error() = %q, want it to name the unknown target", f.Error())
	}
	for _, id := range IDs() {
		if !strings.Contains(f.Error(), id) {
			t.Errorf("Error() = %q, want it to list valid id %q", f.Error(), id)
		}
	}
}

func TestDedupeNames(t *testing.T) {
	got := dedupeNames([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupeNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dedupeNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
