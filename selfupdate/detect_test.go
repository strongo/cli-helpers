package selfupdate

import (
	"errors"
	"testing"
)

// testManagers mirrors a realistic multi-manager consumer config, used
// throughout Classify/DetectSelf tests.
func testManagers() []Manager {
	return []Manager{
		Homebrew("brew upgrade --cask wb"),
		Scoop("scoop update wb"),
		WinGet("winget upgrade Strongo.WB"),
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		execPath    string
		wantMethod  InstallMethod
		wantManager string // "" means nil
	}{
		{"manual: /usr/local/bin", "/usr/local/bin/wb", Manual, ""},
		{"manual: go install target under go/bin", "/home/u/go/bin/wb", Manual, ""},
		{"manual: home bin", "/home/u/bin/wb", Manual, ""},
		{"homebrew apple silicon cellar", "/opt/homebrew/Cellar/wb/0.6.0/bin/wb", Managed, "Homebrew"},
		{"homebrew intel cellar", "/usr/local/Cellar/wb/0.6.0/bin/wb", Managed, "Homebrew"},
		{"linuxbrew prefix", "/home/linuxbrew/.linuxbrew/bin/wb", Managed, "Homebrew"},
		{"homebrew cask apple silicon", "/opt/homebrew/Caskroom/wb/0.6.0/wb", Managed, "Homebrew"},
		{"homebrew cask intel", "/usr/local/Caskroom/wb/0.6.0/wb", Managed, "Homebrew"},
		{"scoop apps", `C:\Users\u\scoop\apps\wb\current\wb.exe`, Managed, "Scoop"},
		{"scoop shims", `C:\Users\u\scoop\shims\wb.exe`, Managed, "Scoop"},
		{"winget packages", `C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\Strongo.WB_abc\wb.exe`, Managed, "WinGet"},
		{"winget links", `C:\Users\u\AppData\Local\Microsoft\WinGet\Links\wb.exe`, Managed, "WinGet"},
		{"ambiguous unrecognized path", "/tmp/random/wb", Ambiguous, ""},
		{"ambiguous root", "/wb", Ambiguous, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.execPath, testManagers())
			if got.Method != tt.wantMethod {
				t.Errorf("Classify(%q).Method = %v, want %v", tt.execPath, got.Method, tt.wantMethod)
			}
			if tt.wantManager == "" {
				if got.Manager != nil {
					t.Errorf("Classify(%q).Manager = %v, want nil", tt.execPath, got.Manager)
				}
			} else {
				if got.Manager == nil || got.Manager.Name != tt.wantManager {
					t.Errorf("Classify(%q).Manager = %v, want %q", tt.execPath, got.Manager, tt.wantManager)
				}
			}
			if got.Path != tt.execPath {
				t.Errorf("Classify(%q).Path = %q, want the original path unchanged", tt.execPath, got.Path)
			}
		})
	}
}

// No managers configured: a path that would otherwise be a managed cask
// install is never Managed, and — since it doesn't end in a `bin` directory
// either — falls through to Ambiguous rather than Manual.
func TestClassify_NoManagersConfigured(t *testing.T) {
	got := Classify("/opt/homebrew/Caskroom/wb/0.6.0/wb", nil)
	if got.Method != Ambiguous {
		t.Errorf("Classify with no managers = %v, want Ambiguous (nothing configured to match)", got.Method)
	}
}

// Marker matching is case-insensitive and separator-agnostic both ways.
func TestClassify_CaseAndSeparatorInsensitive(t *testing.T) {
	got := Classify(`C:\USERS\U\SCOOP\APPS\wb\wb.exe`, testManagers())
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "Scoop" {
		t.Errorf("Classify(uppercase scoop path) = %+v, want Managed/Scoop", got)
	}
}

func TestClassifyNeverAmbiguousForClearCases(t *testing.T) {
	clear := []string{
		"/usr/local/bin/wb",
		"/home/u/go/bin/wb",
		"/opt/homebrew/Cellar/wb/0.6.0/bin/wb",
		`C:\Users\u\scoop\apps\wb\current\wb.exe`,
		`C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\Strongo.WB_abc\wb.exe`,
	}
	for _, p := range clear {
		if got := Classify(p, testManagers()); got.Method == Ambiguous {
			t.Errorf("Classify(%q) returned Ambiguous for a clearly-classified path", p)
		}
	}
}

// --- DetectSelf ---

func TestDetectSelf_Success(t *testing.T) {
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	osExecutable = func() (string, error) { return "/opt/homebrew/Cellar/wb/1/bin/wb", nil }
	evalSymlinksFunc = func(p string) (string, error) { return p, nil }

	cfg := Config{Managers: testManagers()}
	got, err := cfg.DetectSelf()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "Homebrew" {
		t.Fatalf("DetectSelf = %+v, want Managed/Homebrew", got)
	}
}

func TestDetectSelf_ExecutableError(t *testing.T) {
	origExe := osExecutable
	t.Cleanup(func() { osExecutable = origExe })

	osExecutable = func() (string, error) { return "", errors.New("boom") }
	cfg := Config{Managers: testManagers()}
	if _, err := cfg.DetectSelf(); err == nil {
		t.Fatal("expected error when os.Executable fails, got nil")
	}
}

// A symlinked shim must resolve to its real Caskroom location before
// classification (REQ: detect-managed): resolving through the symlink is
// what makes a cask install classify as Managed instead of whatever
// directory the shim itself lives in.
func TestDetectSelf_ResolvesSymlinkBeforeClassifying(t *testing.T) {
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	osExecutable = func() (string, error) { return "/usr/local/bin/wb", nil } // shim: looks Manual
	evalSymlinksFunc = func(string) (string, error) {
		return "/opt/homebrew/Caskroom/wb/1.0.0/wb", nil // real target: Managed
	}

	cfg := Config{Managers: testManagers()}
	got, err := cfg.DetectSelf()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "Homebrew" {
		t.Fatalf("DetectSelf via symlink = %+v, want Managed/Homebrew", got)
	}
	if got.Path != "/opt/homebrew/Caskroom/wb/1.0.0/wb" {
		t.Errorf("DetectSelf.Path = %q, want the resolved path", got.Path)
	}
}

func TestDetectSelf_SymlinkFallback(t *testing.T) {
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })

	osExecutable = func() (string, error) { return "/home/u/go/bin/wb", nil }
	evalSymlinksFunc = func(string) (string, error) { return "", errors.New("no symlink") }

	cfg := Config{Managers: testManagers()}
	got, err := cfg.DetectSelf()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Falls back to the raw exe path, which classifies as Manual (go/bin).
	if got.Method != Manual {
		t.Fatalf("DetectSelf fallback = %+v, want Manual", got)
	}
}

// --- System package directories (REQ: system-package-dirs-are-managed) ---

// withHostOS overrides goosName/getenvFunc for the duration of the test,
// restoring both on cleanup — the same pattern replace_test.go already uses
// for goosName alone, extended with getenvFunc so a Windows-shaped host can
// be exercised from any real host running the test.
func withHostOS(t *testing.T, goos string, env map[string]string) {
	t.Helper()
	origGoos, origGetenv := goosName, getenvFunc
	t.Cleanup(func() { goosName, getenvFunc = origGoos, origGetenv })
	goosName = goos
	getenvFunc = func(k string) string { return env[k] }
}

func TestClassify_SystemPackageDir_Linux(t *testing.T) {
	withHostOS(t, "linux", nil)
	for _, path := range []string{
		"/usr/bin/ingitdb", "/usr/sbin/ingitdb", "/usr/lib/ingitdb", "/usr/lib64/ingitdb",
		"/usr/libexec/ingitdb", "/usr/share/ingitdb", "/bin/ingitdb", "/sbin/ingitdb",
		"/lib/ingitdb", "/lib64/ingitdb", "/nix/store/abc123-ingitdb/bin/ingitdb",
		"/run/current-system/sw/bin/ingitdb",
	} {
		got := Classify(path, nil)
		if got.Method != Managed || got.Manager == nil || got.Manager.Name != systemPackageManagerName {
			t.Errorf("Classify(%q) = %+v, want Managed/%q", path, got, systemPackageManagerName)
		}
		if got.Manager.CanExecuteUpgrade() {
			t.Errorf("Classify(%q).Manager unexpectedly executes an upgrade", path)
		}
	}
}

func TestClassify_SystemPackageDir_Darwin(t *testing.T) {
	withHostOS(t, "darwin", nil)
	for _, path := range []string{
		"/usr/bin/ingitdb", "/usr/sbin/ingitdb", "/usr/libexec/ingitdb",
		"/bin/ingitdb", "/sbin/ingitdb", "/System/ingitdb",
		// nix-darwin.
		"/nix/store/abc123-ingitdb/bin/ingitdb", "/run/current-system/sw/bin/ingitdb",
	} {
		got := Classify(path, nil)
		if got.Method != Managed || got.Manager == nil || got.Manager.Name != systemPackageManagerName {
			t.Errorf("Classify(%q) = %+v, want Managed/%q", path, got, systemPackageManagerName)
		}
	}
	// darwin's own list excludes /usr/local (Homebrew's own Intel prefix,
	// already recognized through Manager.PathMarkers) and /usr/lib64 and
	// /lib (Linux-only spellings with no macOS equivalent).
	for _, path := range []string{"/usr/local/bin/ingitdb", "/usr/lib64/ingitdb", "/lib/ingitdb"} {
		if got := Classify(path, nil); got.Method == Managed {
			t.Errorf("Classify(%q) = %+v, want NOT Managed on darwin", path, got)
		}
	}
}

func TestClassify_SystemPackageDir_Windows(t *testing.T) {
	withHostOS(t, "windows", map[string]string{
		"SystemRoot":        `C:\Windows`,
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
	})
	for _, path := range []string{
		`C:\Windows\System32\ingitdb.exe`,
		`C:\Program Files\ingitdb\ingitdb.exe`,
		`C:\Program Files (x86)\ingitdb\ingitdb.exe`,
	} {
		got := Classify(path, nil)
		if got.Method != Managed || got.Manager == nil || got.Manager.Name != systemPackageManagerName {
			t.Errorf("Classify(%q) = %+v, want Managed/%q", path, got, systemPackageManagerName)
		}
	}
}

// Boundary cases: a sibling directory that merely starts with the same
// characters as a system directory must never match it.
func TestClassify_SystemPackageDir_BoundaryCases(t *testing.T) {
	t.Run("posix: /usr/binx is not /usr/bin", func(t *testing.T) {
		withHostOS(t, "linux", nil)
		got := Classify("/usr/binx/ingitdb", nil)
		if got.Method == Managed {
			t.Errorf("Classify(/usr/binx/ingitdb) = %+v, want NOT Managed", got)
		}
	})
	t.Run("posix: /usr/local/bin is explicitly manual", func(t *testing.T) {
		withHostOS(t, "linux", nil)
		got := Classify("/usr/local/bin/ingitdb", nil)
		if got.Method != Manual {
			t.Errorf("Classify(/usr/local/bin/ingitdb) = %+v, want Manual", got)
		}
	})
	t.Run("posix: /opt/tool/bin is explicitly manual", func(t *testing.T) {
		withHostOS(t, "linux", nil)
		got := Classify("/opt/ingitdb/bin/ingitdb", nil)
		if got.Method != Manual {
			t.Errorf("Classify(/opt/ingitdb/bin/ingitdb) = %+v, want Manual", got)
		}
	})
	t.Run("posix: $HOME/.local/bin is explicitly manual", func(t *testing.T) {
		withHostOS(t, "linux", nil)
		got := Classify("/home/alex/.local/bin/ingitdb", nil)
		if got.Method != Manual {
			t.Errorf("Classify($HOME/.local/bin/ingitdb) = %+v, want Manual", got)
		}
	})
	t.Run("windows: ProgramFilesX is not Program Files", func(t *testing.T) {
		withHostOS(t, "windows", map[string]string{"ProgramFiles": `C:\Program Files`})
		got := Classify(`C:\ProgramFilesX\ingitdb\ingitdb.exe`, nil)
		if got.Method == Managed {
			t.Errorf(`Classify(C:\ProgramFilesX\...) = %+v, want NOT Managed`, got)
		}
	})
	t.Run("windows: Program Files subpath does match", func(t *testing.T) {
		withHostOS(t, "windows", map[string]string{"ProgramFiles": `C:\Program Files`})
		got := Classify(`C:\Program Files\x\ingitdb.exe`, nil)
		if got.Method != Managed {
			t.Errorf(`Classify(C:\Program Files\x\...) = %+v, want Managed`, got)
		}
	})
}

// DetectSelf's symlink resolution feeds Classify the RESOLVED path, so a
// shim outside a system directory that resolves into one is still caught,
// and a path that merely looks like a system directory but resolves
// elsewhere is correctly released from it.
func TestDetectSelf_SymlinkIntoSystemPackageDir(t *testing.T) {
	withHostOS(t, "linux", nil)
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })
	osExecutable = func() (string, error) { return "/usr/local/bin/ingitdb", nil } // looks manual
	evalSymlinksFunc = func(string) (string, error) { return "/usr/bin/ingitdb", nil }

	cfg := Config{}
	got, err := cfg.DetectSelf()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != systemPackageManagerName {
		t.Fatalf("DetectSelf via symlink into system dir = %+v, want Managed/%q", got, systemPackageManagerName)
	}
	if got.Path != "/usr/bin/ingitdb" {
		t.Errorf("DetectSelf.Path = %q, want the resolved path", got.Path)
	}
}

func TestDetectSelf_SymlinkOutOfSystemPackageDir(t *testing.T) {
	withHostOS(t, "linux", nil)
	origExe, origEval := osExecutable, evalSymlinksFunc
	t.Cleanup(func() { osExecutable, evalSymlinksFunc = origExe, origEval })
	osExecutable = func() (string, error) { return "/usr/bin/ingitdb", nil } // looks system
	evalSymlinksFunc = func(string) (string, error) { return "/opt/ingitdb/bin/ingitdb", nil }

	cfg := Config{}
	got, err := cfg.DetectSelf()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Method != Manual {
		t.Fatalf("DetectSelf via symlink out of system dir = %+v, want Manual (/opt is explicitly excluded)", got)
	}
}

// A catalog manager whose own PathMarkers match still takes precedence over
// the built-in system-directory check, even when its marker happens to fall
// inside what would otherwise be a system directory.
func TestClassify_CatalogManagerPrecedesSystemPackageDir(t *testing.T) {
	withHostOS(t, "linux", nil)
	overlapping := Manager{Name: "Custom", UpgradeCommand: "custom upgrade", PathMarkers: []string{"/usr/bin/"}}
	got := Classify("/usr/bin/ingitdb", []Manager{overlapping})
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "Custom" {
		t.Errorf("Classify(/usr/bin/ingitdb) with overlapping manager = %+v, want Managed/Custom", got)
	}

	// Realistic case: Snap's own "/snap/" marker, which never overlaps a
	// system directory, still takes priority over (in this case, does not
	// even reach) the built-in check.
	snap := Manager{Name: "Snap", UpgradeCommand: "snap refresh ingitdb", PathMarkers: []string{"/snap/"}}
	got = Classify("/snap/bin/ingitdb", []Manager{snap})
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "Snap" {
		t.Errorf("Classify(/snap/bin/ingitdb) = %+v, want Managed/Snap", got)
	}
}

// No managers configured at all: the built-in system-directory check still
// applies (REQ: system-package-dirs-are-managed — "regardless of the
// consumer's configured Managers").
func TestClassify_SystemPackageDir_AppliesWithNoManagersConfigured(t *testing.T) {
	withHostOS(t, "linux", nil)
	got := Classify("/usr/bin/ingitdb", nil)
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != systemPackageManagerName {
		t.Errorf("Classify(/usr/bin/ingitdb, nil managers) = %+v, want Managed/%q", got, systemPackageManagerName)
	}
}

// WinGet's machine-scope markers (added specifically so this case redirects
// to winget rather than the generic built-in system-package message) still
// take precedence over the built-in check, even though a machine-scope
// WinGet install genuinely sits under %ProgramFiles%, a system directory.
func TestClassify_WinGetMachineScopePrecedesSystemPackageDir(t *testing.T) {
	withHostOS(t, "windows", map[string]string{"ProgramFiles": `C:\Program Files`})
	got := Classify(`C:\Program Files\WinGet\Packages\Strongo.WB_abc\wb.exe`, testManagers())
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "WinGet" {
		t.Errorf("Classify(machine-scope WinGet path) = %+v, want Managed/WinGet", got)
	}
	got = Classify(`C:\Program Files\WinGet\Links\wb.exe`, testManagers())
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "WinGet" {
		t.Errorf("Classify(machine-scope WinGet links path) = %+v, want Managed/WinGet", got)
	}
}

// --- ClassifyManagers ---

// ClassifyManagers matches exactly what Classify's own manager-marker loop
// does, but never falls through to the system-directory check or the
// Manual/Ambiguous fallback: an unmatched path is always Ambiguous here,
// even one that Classify itself would call Manual or Managed via the
// built-in check.
func TestClassifyManagers(t *testing.T) {
	got := ClassifyManagers("/opt/homebrew/Cellar/wb/0.6.0/bin/wb", testManagers())
	if got.Method != Managed || got.Manager == nil || got.Manager.Name != "Homebrew" {
		t.Errorf("ClassifyManagers(homebrew path) = %+v, want Managed/Homebrew", got)
	}

	// A system-package-directory path with no manager marker: Classify
	// would call this Managed via the built-in check; ClassifyManagers
	// never applies that check, so it is Ambiguous, not Manual or Managed.
	got = ClassifyManagers("/usr/bin/ingitdb", nil)
	if got.Method != Ambiguous {
		t.Errorf("ClassifyManagers(/usr/bin/ingitdb, no managers) = %+v, want Ambiguous", got)
	}

	// A plausible manual path: ClassifyManagers still reports Ambiguous,
	// never Manual — that fallback belongs to Classify alone.
	got = ClassifyManagers("/usr/local/bin/wb", testManagers())
	if got.Method != Ambiguous {
		t.Errorf("ClassifyManagers(/usr/local/bin/wb) = %+v, want Ambiguous (never Manual)", got)
	}
}

// InstallMethod's token spelling is part of the machine-readable contract a
// consumer's JSON output exposes, so it is pinned here rather than left to
// whatever %v would print.
func TestInstallMethodString(t *testing.T) {
	for method, want := range map[InstallMethod]string{
		Managed:           "managed",
		Manual:            "manual",
		Ambiguous:         "ambiguous",
		InstallMethod(99): "unknown",
	} {
		if got := method.String(); got != want {
			t.Errorf("InstallMethod(%d).String() = %q, want %q", method, got, want)
		}
	}
}
