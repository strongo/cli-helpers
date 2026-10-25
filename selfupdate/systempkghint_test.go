package selfupdate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The library's own Windows text must cover a copy extracted from an archive,
// not only an installer: a consumer that ships a zip has neither an MSI nor an
// EXE (strongo/cli-helpers#42).
func TestSystemPackageManagerFor_WindowsDefaultCoversAnArchiveCopy(t *testing.T) {
	hint := systemPackageManagerFor("windows").UpgradeHint
	for _, want := range []string{"Windows Update", "installer", "archive"} {
		if !strings.Contains(hint, want) {
			t.Errorf("Windows default hint = %q, want it to contain %q", hint, want)
		}
	}
}

func windowsSystemDirs(t *testing.T) {
	t.Helper()
	withHostOS(t, "windows", map[string]string{"SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`})
}

// The consumer's hint replaces the library's text for a system-directory
// binary, for DetectSelf and Config.Classify alike, and changes nothing else
// about the detection.
func TestConfigSystemPackageHint_ReplacesTheDefaultText(t *testing.T) {
	windowsSystemDirs(t)
	const path = `C:\Program Files\wb\wb.exe`
	plain := Config{}.Classify(path)
	cfg := Config{SystemPackageHint: "a new download of the zip from the releases page"}
	got := cfg.Classify(path)
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != systemPackageManagerName || got.Path != path {
		t.Fatalf("Classify = %+v, want the same Managed/%q verdict", got, systemPackageManagerName)
	}
	if got.Manager.UpgradeHint != cfg.SystemPackageHint {
		t.Errorf("UpgradeHint = %q, want the consumer's", got.Manager.UpgradeHint)
	}
	if got.Manager.UpgradeCommand != "" || got.Manager.CanExecuteUpgrade() {
		t.Errorf("a custom hint must stay prose-only: %+v", got.Manager)
	}
	if plain.Manager == nil || plain.Manager.UpgradeHint != systemPackageManagerFor("windows").UpgradeHint {
		t.Errorf("zero Config changed the default hint: %+v", plain.Manager)
	}
	if pkg := Classify(path, nil); pkg.Manager.UpgradeHint != plain.Manager.UpgradeHint {
		t.Errorf("package-level Classify = %q, want the library default %q", pkg.Manager.UpgradeHint, plain.Manager.UpgradeHint)
	}

	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })
	osExecutable = func() (string, error) { return path, nil }
	evalSymlinksFunc = func(p string) (string, error) { return p, nil }
	detected, err := cfg.DetectSelf()
	if err != nil || detected.Manager == nil || detected.Manager.UpgradeHint != cfg.SystemPackageHint {
		t.Fatalf("DetectSelf = %+v, %v, want the consumer's hint", detected, err)
	}
}

// The function form sees the host's OS and the matched directory as
// SystemPackageDirs spells it, takes precedence over the string, and an empty
// result falls back to the string and then to the library's text.
func TestConfigSystemPackageHintFor_ReceivesOSAndDirectoryAndFallsBack(t *testing.T) {
	windowsSystemDirs(t)
	var gotOS, gotDir string
	cfg := Config{
		SystemPackageHint: "fallback text",
		SystemPackageHintFor: func(goos, dir string) string {
			gotOS, gotDir = goos, dir
			if strings.HasSuffix(dir, "Windows") {
				return ""
			}
			return "from " + dir
		},
	}
	if hint := cfg.Classify(`C:\Program Files\wb\wb.exe`).Manager.UpgradeHint; hint != `from C:\Program Files` {
		t.Errorf("hint = %q", hint)
	}
	if gotOS != "windows" || gotDir != `C:\Program Files` {
		t.Errorf("function got (%q, %q)", gotOS, gotDir)
	}
	if hint := cfg.Classify(`C:\Windows\System32\wb.exe`).Manager.UpgradeHint; hint != "fallback text" {
		t.Errorf("empty function result gave %q, want the string", hint)
	}
	cfg.SystemPackageHint = ""
	if hint := cfg.Classify(`C:\Windows\System32\wb.exe`).Manager.UpgradeHint; hint != systemPackageManagerFor("windows").UpgradeHint {
		t.Errorf("no consumer text gave %q, want the library default", hint)
	}
}

// A path outside a system directory never reaches the hint, and a catalog
// manager keeps precedence and its own hint.
func TestConfigSystemPackageHint_IgnoredOutsideSystemDirectories(t *testing.T) {
	withHostOS(t, "linux", nil)
	called := false
	cfg := Config{
		SystemPackageHintFor: func(string, string) string { called = true; return "x" },
		Managers:             []Manager{{Name: "Snap", PathMarkers: []string{"/snap/"}, UpgradeHint: "snap refresh"}},
	}
	if det := cfg.Classify("/usr/local/bin/wb"); det.Method != Manual {
		t.Errorf("manual path = %+v", det)
	}
	if det := cfg.Classify("/snap/bin/wb"); det.Manager == nil || det.Manager.UpgradeHint != "snap refresh" {
		t.Errorf("catalog manager = %+v", det)
	}
	if called {
		t.Error("the hint function ran for a path that is not in a system directory")
	}
	if det := cfg.Classify("/usr/bin/wb"); det.Manager == nil || det.Manager.UpgradeHint != "x" || !called {
		t.Errorf("system path = %+v called=%v", det, called)
	}
}

// Update's own redirect carries the consumer's hint, so the text a person sees
// is the consumer's, not the library's.
func TestUpdate_SystemPackageDirRedirectCarriesTheConsumersHint(t *testing.T) {
	h := newUpdateHarness(t, "sysroot/wb", "old binary")
	withHostOS(t, "windows", map[string]string{"SystemRoot": filepath.Dir(h.target)})
	h.cfg.SystemPackageHint = "a new download of the zip"
	h.setReleases(stableReleaseJSON("v1.1.0"))

	outcome, err := h.cfg.Update(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if outcome.Action != ActionRedirected || outcome.Detection.Manager == nil || outcome.Detection.Manager.UpgradeHint != "a new download of the zip" {
		t.Fatalf("Outcome = %+v, want a redirect with the consumer's hint", outcome)
	}
	if h.targetBytes() != "old binary" {
		t.Error("target file was modified for a system-directory install")
	}
}
