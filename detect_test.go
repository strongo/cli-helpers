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
