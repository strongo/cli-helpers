package cliinstall

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate"
)

// --- isAbsPath / joinPath / installFilePath -----------------------------

func TestIsAbsPath(t *testing.T) {
	cases := []struct {
		goos string
		path string
		want bool
	}{
		{"linux", "/usr/bin", true},
		{"linux", "usr/bin", false},
		{"linux", "", false},
		{"darwin", "/opt/homebrew/bin", true},
		{"windows", `C:\Users\alex`, true},
		{"windows", "C:/Users/alex", true},
		{"windows", `\\server\share`, true},
		{"windows", "//server/share", true},
		{"windows", `Users\alex`, false},
		{"windows", "C", false},
		// M12: a bare drive letter with no separator ("C:foo", "C:") names
		// a path relative to that drive's own current directory, not an
		// absolute one.
		{"windows", "C:foo", false},
		{"windows", "C:", false},
	}
	for _, c := range cases {
		if got := isAbsPath(c.goos, c.path); got != c.want {
			t.Errorf("isAbsPath(%q, %q) = %v, want %v", c.goos, c.path, got, c.want)
		}
	}
}

func TestJoinPath(t *testing.T) {
	if got := joinPath("linux", "/home/alex", ".local", "bin"); got != "/home/alex/.local/bin" {
		t.Errorf("joinPath posix = %q", got)
	}
	if got := joinPath("windows", `C:\Users\alex\AppData\Local\`, "Programs", "strongo", "bin"); got != `C:\Users\alex\AppData\Local\Programs\strongo\bin` {
		t.Errorf("joinPath windows = %q", got)
	}
	if got := joinPath("linux", "/a/", "", "/b/"); got != "/a/b" {
		t.Errorf("joinPath drops empty parts and duplicate separators = %q", got)
	}
}

func TestInstallFilePath(t *testing.T) {
	if got := installFilePath("linux", "/home/alex/.local/bin", "ovdb"); got != "/home/alex/.local/bin/ovdb" {
		t.Errorf("installFilePath posix = %q", got)
	}
	if got := installFilePath("windows", `C:\bin`, "ovdb"); got != `C:\bin\ovdb.exe` {
		t.Errorf("installFilePath windows = %q", got)
	}
}

// --- resolveDir -----------------------------------------------------------

func TestResolveDir_AbsoluteSymlinkResolved(t *testing.T) {
	got := resolveDir("/opt/homebrew/bin", "linux", func(p string) (string, error) {
		if p == "/opt/homebrew/bin" {
			return "/opt/homebrew/Cellar/actual", nil
		}
		return p, nil
	})
	if got != "/opt/homebrew/Cellar/actual" {
		t.Errorf("resolveDir = %q", got)
	}
}

func TestResolveDir_NilEvalSymlinksReturnsAbsolute(t *testing.T) {
	if got := resolveDir("/some/dir", "linux", nil); got != "/some/dir" {
		t.Errorf("resolveDir = %q", got)
	}
}

func TestResolveDir_EvalSymlinksErrorFallsBackToUnresolved(t *testing.T) {
	got := resolveDir("/some/dir", "linux", func(string) (string, error) {
		return "", errors.New("boom")
	})
	if got != "/some/dir" {
		t.Errorf("resolveDir = %q, want fallback to unresolved absolute path", got)
	}
}

func TestResolveDir_RelativeJoinsWorkingDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	// getwdFunc (unlike goos) is never injected here — it always returns
	// the REAL process cwd, shaped for the REAL host OS. Forcing goos to
	// "linux" while running for real on Windows CI made resolveDir's own
	// joinPath(goos, cwd, "sub/dir") join with "/" while `want` (built via
	// the real, host-native filepath.Join) used "\", so they never
	// matched — this test is about cwd-relative resolution on WHATEVER
	// host actually runs it, so goos must track runtime.GOOS.
	got := resolveDir("sub/dir", runtime.GOOS, nil)
	want := filepath.Join(tmp, "sub", "dir")
	if got != want {
		t.Errorf("resolveDir = %q, want %q", got, want)
	}
}

func TestResolveDir_RelativeGetwdFailureLeavesPathAsGiven(t *testing.T) {
	orig := getwdFunc
	t.Cleanup(func() { getwdFunc = orig })
	getwdFunc = func() (string, error) { return "", errors.New("no cwd") }

	got := resolveDir("sub/dir", "linux", nil)
	if got != "sub/dir" {
		t.Errorf("resolveDir = %q, want unchanged relative path when getwd fails", got)
	}
}

func TestResolveDir_WindowsAbsoluteNeverCallsGetwd(t *testing.T) {
	orig := getwdFunc
	t.Cleanup(func() { getwdFunc = orig })
	getwdFunc = func() (string, error) { t.Fatal("getwd should not be called for an absolute path"); return "", nil }

	got := resolveDir(`C:\Program Files\Foo`, "windows", nil)
	if got != `C:\Program Files\Foo` {
		t.Errorf("resolveDir = %q", got)
	}
}

// --- cleanPath / samePath ---------------------------------------------------

func TestCleanPath(t *testing.T) {
	cases := []struct {
		name, in, goos, want string
	}{
		{"empty", "", "linux", ""},
		{"posix already clean", "/home/alex/bin", "linux", "/home/alex/bin"},
		{"posix dot segments", "/home/alex/./bin", "linux", "/home/alex/bin"},
		{"posix dotdot collapses", "/home/alex/tmp/../bin", "linux", "/home/alex/bin"},
		{"posix dotdot past root stays at root", "/../../bin", "linux", "/bin"},
		{"posix relative", "sub/./dir/../other", "linux", "sub/other"},
		{"windows drive root", `C:\Program Files\..\Go\bin`, "windows", `C:\Go\bin`},
		{"windows forward slashes", `C:/Go/./bin`, "windows", `C:\Go\bin`},
		{"windows UNC root", `\\host\share\..\other`, "windows", `\\host\other`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanPath(c.in, c.goos); got != c.want {
				t.Errorf("cleanPath(%q, %q) = %q, want %q", c.in, c.goos, got, c.want)
			}
		})
	}
}

func TestSamePath(t *testing.T) {
	if !samePath("/home/alex/bin/ovdb", "/home/alex/./bin/ovdb", "linux") {
		t.Error("samePath(linux) = false for two spellings of the same path")
	}
	if samePath("/home/alex/bin/ovdb", "/home/alex/bin/ingitdb", "linux") {
		t.Error("samePath(linux) = true for different files")
	}
	if !samePath("/Users/Alex/bin/ovdb", "/users/alex/bin/ovdb", "darwin") {
		t.Error("samePath(darwin) = false for a case variant, want case-insensitive match")
	}
	if samePath("/Users/Alex/bin/ovdb", "/users/alex/bin/ovdb", "linux") {
		t.Error("samePath(linux) = true for a case variant, want byte-exact comparison")
	}
	if !samePath(`C:\Go\bin\ovdb.exe`, `c:/go/bin/OVDB.exe`, "windows") {
		t.Error("samePath(windows) = false for a case- and slash-style variant")
	}
}

// --- normalizeSlashes -----------------------------------------------------

func TestNormalizeSlashes(t *testing.T) {
	if got := normalizeSlashes(`C:\Program Files\Foo`); got != "c:/program files/foo" {
		t.Errorf("normalizeSlashes = %q", got)
	}
}

// --- deniedRoots / destinationDenylistFailure ------------------------------

func TestDeniedRoots_Posix(t *testing.T) {
	getenv := func(k string) string {
		if k == "GOROOT" {
			return "/usr/local/go"
		}
		return ""
	}
	got := deniedRoots("linux", getenv)
	wantContains := []string{"/usr", "/bin", "/sbin", "/lib", "/opt/homebrew", "/home/linuxbrew/.linuxbrew", "/snap", "/nix", "/usr/local/go"}
	for _, w := range wantContains {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deniedRoots(linux) = %v, missing %q", got, w)
		}
	}
}

func TestDeniedRoots_PosixNoGoroot(t *testing.T) {
	got := deniedRoots("linux", func(string) string { return "" })
	for _, g := range got {
		if g == "" {
			t.Errorf("deniedRoots must not include an empty GOROOT: %v", got)
		}
	}
}

// TestDeniedRoots_GorootFallback proves M2's fix: an unset $GOROOT falls
// back to runtime.GOROOT() when goos is the real running host's own OS.
func TestDeniedRoots_GorootFallback(t *testing.T) {
	orig := goroot
	t.Cleanup(func() { goroot = orig })
	goroot = func() string { return "/opt/go-toolchain" }

	got := deniedRoots(runtime.GOOS, func(string) string { return "" })
	found := false
	for _, g := range got {
		if g == "/opt/go-toolchain" {
			found = true
		}
	}
	if !found {
		t.Errorf("deniedRoots(%s) = %v, want it to include the runtime.GOROOT() fallback", runtime.GOOS, got)
	}
}

// A goos this package was merely asked to evaluate (not the real running
// host's own OS) must never pick up THIS process's own GOROOT: a foreign
// goos being planned for has no relationship to the toolchain that built
// the current test binary.
func TestDeniedRoots_GorootFallbackSkippedForForeignGoos(t *testing.T) {
	orig := goroot
	t.Cleanup(func() { goroot = orig })
	goroot = func() string { return "/opt/go-toolchain" }

	foreign := "windows"
	if runtime.GOOS == "windows" {
		foreign = "linux"
	}
	got := deniedRoots(foreign, func(string) string { return "" })
	for _, g := range got {
		if g == "/opt/go-toolchain" {
			t.Errorf("deniedRoots(%s) = %v, must not include this process's own GOROOT for a foreign goos", foreign, got)
		}
	}
}

func TestDeniedRoots_WindowsOnlySetVars(t *testing.T) {
	// GOROOT is set explicitly so this test's exact-length assertion holds
	// regardless of platform: deniedRoots' runtime.GOROOT() fallback (M2)
	// applies only when goos equals the REAL running host's OS, which is
	// windows on an actual Windows CI job — this test must not depend on
	// which platform runs it.
	getenv := func(k string) string {
		switch k {
		case "ProgramData":
			return `C:\ProgramData`
		case "SystemRoot":
			return `C:\Windows`
		case "GOROOT":
			return `C:\Go`
		default:
			return ""
		}
	}
	got := deniedRoots("windows", getenv)
	want := []string{`C:\ProgramData`, `C:\Windows`, `C:\Go`}
	if len(got) != len(want) {
		t.Fatalf("deniedRoots(windows) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("deniedRoots(windows)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDestinationDenylistFailure_ExactRoot(t *testing.T) {
	f := destinationDenylistFailure("/usr", "linux", func(string) string { return "" })
	if f == nil || f.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("destinationDenylistFailure(/usr) = %v, want KindNoInstallDir", f)
	}
}

func TestDestinationDenylistFailure_Subdirectory(t *testing.T) {
	f := destinationDenylistFailure("/usr/local/bin", "linux", func(string) string { return "" })
	if f == nil || f.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("destinationDenylistFailure(/usr/local/bin) = %v, want KindNoInstallDir", f)
	}
}

func TestDestinationDenylistFailure_ManagerMarker(t *testing.T) {
	// /snap is also a static root, so use a manager-only marker: Scoop's
	// "/scoop/apps/" is declared by ingitdb's catalog entry and by nothing
	// in the static root list.
	f := destinationDenylistFailure(`C:\Users\alex\scoop\apps\ingitdb\current`, "windows", func(k string) string {
		return "" // no Windows static roots configured, isolating the manager-marker branch
	})
	if f == nil || f.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("destinationDenylistFailure(scoop path) = %v, want KindNoInstallDir", f)
	}
}

func TestDestinationDenylistFailure_Allowed(t *testing.T) {
	f := destinationDenylistFailure("/home/alex/.local/bin", "linux", func(string) string { return "" })
	if f != nil {
		t.Errorf("destinationDenylistFailure(allowed dir) = %v, want nil", f)
	}
}

// deniedRoots reuses selfupdate.SystemPackageDirs rather than duplicating
// its entries (destination.go's own doc comment on deniedRoots explains
// why the two lists differ where they do). "/lib64" and "/usr/libexec" are
// NOT in this library's own hand-written extras — they only appear here
// because SystemPackageDirs contributes them, so their presence proves the
// reuse actually happened rather than deniedRoots merely keeping its old,
// separately-hand-written list.
func TestDeniedRoots_ReusesSystemPackageDirs(t *testing.T) {
	got := deniedRoots("linux", func(string) string { return "" })
	for _, want := range []string{"/lib64", "/usr/libexec", "/usr/lib64", "/nix/store", "/run/current-system"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deniedRoots(linux) = %v, missing %q from selfupdate.SystemPackageDirs", got, want)
		}
	}
}

// The install destination denylist still refuses every directory
// SystemPackageDirs names, even ones this library never separately
// hand-listed before this reuse (self-update#req:system-package-dirs-are-
// managed's own rationale applies to "MUST NOT plan a write here" exactly
// as much as it does to "MUST NOT self-update an existing copy here").
func TestDestinationDenylistFailure_ReusedSystemPackageDir(t *testing.T) {
	f := destinationDenylistFailure("/lib64/ingitdb", "linux", func(string) string { return "" })
	if f == nil || f.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("destinationDenylistFailure(/lib64/ingitdb) = %v, want KindNoInstallDir", f)
	}
}

// Windows: %SystemRoot%/%ProgramFiles%/%ProgramFiles(x86)% come from
// SystemPackageDirs; %ProgramData% remains this library's own extra root
// (see deniedRoots' doc comment) even though SystemPackageDirs does not
// list it.
func TestDeniedRoots_WindowsReusesSystemPackageDirsPlusProgramData(t *testing.T) {
	getenv := func(k string) string {
		switch k {
		case "ProgramData":
			return `C:\ProgramData`
		case "ProgramFiles":
			return `C:\Program Files`
		case "ProgramFiles(x86)":
			return `C:\Program Files (x86)`
		case "SystemRoot":
			return `C:\Windows`
		default:
			return ""
		}
	}
	got := deniedRoots("windows", getenv)
	want := []string{`C:\ProgramData`, `C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`}
	if len(got) != len(want) {
		t.Fatalf("deniedRoots(windows) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("deniedRoots(windows)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- perUserBinDir ----------------------------------------------------------

func TestPerUserBinDir_Posix(t *testing.T) {
	got, err := perUserBinDir("linux", func() (string, error) { return "/home/alex", nil }, func(string) string { return "" })
	if err != nil {
		t.Fatalf("perUserBinDir error = %v", err)
	}
	if got != "/home/alex/.local/bin" {
		t.Errorf("perUserBinDir = %q", got)
	}
}

func TestPerUserBinDir_PosixHomeError(t *testing.T) {
	_, err := perUserBinDir("linux", func() (string, error) { return "", errors.New("no home") }, func(string) string { return "" })
	if err == nil {
		t.Fatal("perUserBinDir error = nil, want an error when UserHomeDir fails")
	}
}

func TestPerUserBinDir_Windows(t *testing.T) {
	got, err := perUserBinDir("windows", nil, func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\alex\AppData\Local`
		}
		return ""
	})
	if err != nil {
		t.Fatalf("perUserBinDir error = %v", err)
	}
	if want := `C:\Users\alex\AppData\Local\Programs\strongo\bin`; got != want {
		t.Errorf("perUserBinDir = %q, want %q", got, want)
	}
}

func TestPerUserBinDir_WindowsLocalAppDataUnset(t *testing.T) {
	_, err := perUserBinDir("windows", nil, func(string) string { return "" })
	if err == nil {
		t.Fatal("perUserBinDir error = nil, want an error when %LOCALAPPDATA% is unset")
	}
}

// --- cleanForCompare / dirOnPath -------------------------------------------

func TestCleanForCompare(t *testing.T) {
	if got := cleanForCompare("/home/alex/.local/bin/", "linux"); got != "/home/alex/.local/bin" {
		t.Errorf("cleanForCompare posix = %q", got)
	}
	if got := cleanForCompare(`C:/Users/Alex/AppData/Local/Programs/strongo/bin`, "windows"); got != `c:\users\alex\appdata\local\programs\strongo\bin` {
		t.Errorf("cleanForCompare windows = %q", got)
	}
}

func TestDirOnPath(t *testing.T) {
	dirs := []string{"/usr/bin", "/home/alex/.local/bin/"}
	if !dirOnPath(dirs, "/home/alex/.local/bin", "linux") {
		t.Error("dirOnPath = false, want true")
	}
	if dirOnPath(dirs, "/nowhere", "linux") {
		t.Error("dirOnPath = true, want false")
	}
}

func TestDirOnPath_WindowsCaseInsensitive(t *testing.T) {
	dirs := []string{`c:\users\alex\appdata\local\programs\strongo\bin`}
	if !dirOnPath(dirs, `C:\Users\Alex\AppData\Local\Programs\strongo\bin`, "windows") {
		t.Error("dirOnPath = false, want case-insensitive match on windows")
	}
}

// --- caskSupportsOS ---------------------------------------------------------

func TestCaskSupportsOS(t *testing.T) {
	e := Entry{CaskOS: []string{"darwin", "linux"}}
	if !caskSupportsOS(e, "darwin") {
		t.Error("caskSupportsOS(darwin) = false, want true")
	}
	if caskSupportsOS(e, "windows") {
		t.Error("caskSupportsOS(windows) = true, want false")
	}
	if caskSupportsOS(Entry{}, "linux") {
		t.Error("caskSupportsOS with no CaskOS = true, want false")
	}
}

// --- planMethod -------------------------------------------------------------

func installTestOpts(pathDirs []string, homeDir string, homeErr error, getenv func(string) string) Options {
	return Options{
		Env: InstallEnv{
			Env: Env{
				PathDirs:     func() []string { return pathDirs },
				EvalSymlinks: func(p string) (string, error) { return p, nil },
			},
			UserHomeDir: func() (string, error) { return homeDir, homeErr },
			Getenv:      getenv,
		},
	}
}

func TestPlanMethod_DirGivenAllowed(t *testing.T) {
	// planMethod reads the package's own goosName seam directly (it takes
	// no goos parameter), and this test's "/home/alex/bin" literal is a
	// POSIX policy fixture: pin goosName to a POSIX value so isAbsPath and
	// the denylist check exercise that policy regardless of the REAL host
	// OS actually running the test (see TestProbe_WindowsExecutableSuffix
	// for the same pattern) — "/home/alex/bin" is not windows-absolute at
	// all, so left at its default on a real Windows CI run this test's own
	// premise (an allowed, absolute --dir) would never hold.
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "host"}
	opts := installTestOpts(nil, "", nil, func(string) string { return "" })
	opts.Dir = "/home/alex/bin"

	method, destDir, caskToken, createIfMissing, failure := planMethod(host, "", Entry{ID: "ovdb"}, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect || destDir != "/home/alex/bin" || caskToken != "" || createIfMissing {
		t.Errorf("planMethod = (%v, %q, %q, %v)", method, destDir, caskToken, createIfMissing)
	}
}

func TestPlanMethod_DirGivenDenylistedNoFallback(t *testing.T) {
	// Same reasoning as TestPlanMethod_DirGivenAllowed: "/usr/local/bin" is
	// a POSIX denylist fixture, so goosName is pinned to POSIX regardless
	// of the real host OS.
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "host"}
	opts := installTestOpts(nil, "", nil, func(string) string { return "" })
	opts.Dir = "/usr/local/bin"

	_, _, _, _, failure := planMethod(host, "", Entry{ID: "ovdb"}, opts)
	if failure == nil || failure.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("planMethod failure = %v, want KindNoInstallDir", failure)
	}
}

func TestPlanMethod_HomebrewHostCaskSupportsOS(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb", Managers: []selfupdate.Manager{selfupdate.HomebrewCask("wb")}}
	target := Entry{ID: "ovdb", CaskToken: "openvaultdb/tap/ovdb", CaskOS: []string{"darwin", "linux"}}
	opts := installTestOpts(nil, "", nil, func(string) string { return "" })

	method, destDir, caskToken, createIfMissing, failure := planMethod(host, "/opt/homebrew/Caskroom/wb/1.0.0", target, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodHomebrew || caskToken != "openvaultdb/tap/ovdb" || destDir != "" || createIfMissing {
		t.Errorf("planMethod = (%v, %q, %q, %v)", method, destDir, caskToken, createIfMissing)
	}
}

func TestPlanMethod_HomebrewHostCaskDoesNotSupportOSFallsToPerUser(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb", Managers: []selfupdate.Manager{selfupdate.HomebrewCask("wb")}}
	target := Entry{ID: "ovdb", CaskToken: "openvaultdb/tap/ovdb", CaskOS: []string{"windows"}}
	opts := installTestOpts([]string{"/home/alex/.local/bin"}, "/home/alex", nil, func(string) string { return "" })

	method, destDir, caskToken, createIfMissing, failure := planMethod(host, "/opt/homebrew/Caskroom/wb/1.0.0", target, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect || destDir != "/home/alex/.local/bin" || caskToken != "" || !createIfMissing {
		t.Errorf("planMethod = (%v, %q, %q, %v)", method, destDir, caskToken, createIfMissing)
	}
}

func TestPlanMethod_HomebrewHostTargetHasNoCaskFallsToPerUser(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb", Managers: []selfupdate.Manager{selfupdate.HomebrewCask("wb")}}
	target := Entry{ID: "cover100"}
	opts := installTestOpts([]string{"/home/alex/.local/bin"}, "/home/alex", nil, func(string) string { return "" })

	method, _, _, _, failure := planMethod(host, "/opt/homebrew/Caskroom/wb/1.0.0", target, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect {
		t.Errorf("method = %v, want MethodDirect (per-user bin dir)", method)
	}
}

func TestPlanMethod_ManualHostAllowedDirectory(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb"}
	opts := installTestOpts(nil, "", nil, func(string) string { return "" })

	method, destDir, _, createIfMissing, failure := planMethod(host, "/home/alex/go/bin", Entry{ID: "ovdb"}, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect || destDir != "/home/alex/go/bin" || createIfMissing {
		t.Errorf("planMethod = (%v, %q, %v)", method, destDir, createIfMissing)
	}
}

func TestPlanMethod_ManualHostDenylistedFallsToPerUser(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb"}
	opts := installTestOpts([]string{"/home/alex/.local/bin"}, "/home/alex", nil, func(string) string { return "" })

	method, destDir, _, createIfMissing, failure := planMethod(host, "/usr/bin", Entry{ID: "ovdb"}, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect || destDir != "/home/alex/.local/bin" || !createIfMissing {
		t.Errorf("planMethod = (%v, %q, %v)", method, destDir, createIfMissing)
	}
}

func TestPlanMethod_ManualHostDenylistedPerUserNotOnPathFails(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb"}
	opts := installTestOpts(nil, "/home/alex", nil, func(string) string { return "" })

	_, _, _, _, failure := planMethod(host, "/usr/bin", Entry{ID: "ovdb"}, opts)
	if failure == nil || failure.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("planMethod failure = %v, want KindNoInstallDir", failure)
	}
}

func TestPlanMethod_AmbiguousHostFallsToPerUser(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb"}
	opts := installTestOpts([]string{"/home/alex/.local/bin"}, "/home/alex", nil, func(string) string { return "" })

	method, destDir, _, createIfMissing, failure := planMethod(host, "/opt/weird/place", Entry{ID: "ovdb"}, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect || destDir != "/home/alex/.local/bin" || !createIfMissing {
		t.Errorf("planMethod = (%v, %q, %v)", method, destDir, createIfMissing)
	}
}

func TestPlanMethod_EmptyHostDirTreatedAsAmbiguous(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb"}
	opts := installTestOpts([]string{"/home/alex/.local/bin"}, "/home/alex", nil, func(string) string { return "" })

	method, _, _, _, failure := planMethod(host, "", Entry{ID: "ovdb"}, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect {
		t.Errorf("method = %v, want MethodDirect (per-user bin dir)", method)
	}
}

func TestPlanMethod_PerUserBinDirDeterminationFails(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "linux"

	host := Entry{ID: "wb"}
	opts := installTestOpts(nil, "", errors.New("no home"), func(string) string { return "" })

	_, _, _, _, failure := planMethod(host, "/opt/weird/place", Entry{ID: "ovdb"}, opts)
	if failure == nil || failure.Kind != selfupdate.KindNoInstallDir {
		t.Fatalf("planMethod failure = %v, want KindNoInstallDir", failure)
	}
}

func TestPlanMethod_WindowsPerUserBinDir(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "windows"

	host := Entry{ID: "wb"}
	getenv := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\alex\AppData\Local`
		}
		return ""
	}
	opts := installTestOpts([]string{`c:\users\alex\appdata\local\programs\strongo\bin`}, "", nil, getenv)

	method, destDir, _, createIfMissing, failure := planMethod(host, `C:\Program Files\wb`, Entry{ID: "ovdb"}, opts)
	if failure != nil {
		t.Fatalf("planMethod failure = %v", failure)
	}
	if method != MethodDirect || !createIfMissing {
		t.Errorf("planMethod = (%v, %v)", method, createIfMissing)
	}
	if want := `C:\Users\alex\AppData\Local\Programs\strongo\bin`; destDir != want {
		t.Errorf("destDir = %q, want %q", destDir, want)
	}
}
