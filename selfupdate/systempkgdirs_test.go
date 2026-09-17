package selfupdate

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestSystemPackageDirs_Linux(t *testing.T) {
	got := SystemPackageDirs("linux", func(string) string { return "" })
	want := []string{
		"/usr/bin", "/usr/sbin", "/usr/lib", "/usr/lib64", "/usr/libexec", "/usr/share",
		"/bin", "/sbin", "/lib", "/lib64",
		"/nix/store", "/run/current-system",
	}
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
		t.Errorf("SystemPackageDirs(linux) = %v, want %v", got, want)
	}
	// getenv must never be consulted for a POSIX goos.
	if got2 := SystemPackageDirs("linux", func(string) string { t.Fatal("getenv called for linux"); return "" }); len(got2) == 0 {
		t.Error("SystemPackageDirs(linux) returned nothing")
	}
}

// "other unix" (anything that is neither windows nor darwin) gets the same
// list as linux — a freebsd host, for instance.
func TestSystemPackageDirs_OtherUnixMatchesLinux(t *testing.T) {
	linux := SystemPackageDirs("linux", func(string) string { return "" })
	freebsd := SystemPackageDirs("freebsd", func(string) string { return "" })
	if !reflect.DeepEqual(sortedCopy(linux), sortedCopy(freebsd)) {
		t.Errorf("SystemPackageDirs(freebsd) = %v, want the same set as linux %v", freebsd, linux)
	}
}

// darwin includes /nix/store and /run/current-system for a nix-darwin
// install, exactly as Linux does, alongside macOS's own system dirs.
func TestSystemPackageDirs_Darwin(t *testing.T) {
	got := SystemPackageDirs("darwin", func(string) string { return "" })
	want := []string{
		"/usr/bin", "/usr/sbin", "/usr/libexec", "/bin", "/sbin", "/System",
		"/nix/store", "/run/current-system",
	}
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
		t.Errorf("SystemPackageDirs(darwin) = %v, want %v", got, want)
	}
	// /usr/local is Homebrew's own Intel-Mac prefix (already recognized via
	// Manager.PathMarkers); /usr/lib64 and /lib are Linux-only spellings
	// that have no macOS equivalent.
	for _, excluded := range []string{"/usr/local", "/usr/lib64", "/lib"} {
		for _, dir := range got {
			if dir == excluded {
				t.Errorf("SystemPackageDirs(darwin) unexpectedly includes %q", excluded)
			}
		}
	}
}

func TestSystemPackageDirs_WindowsAllSet(t *testing.T) {
	env := map[string]string{
		"SystemRoot":        `C:\Windows`,
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
	}
	got := SystemPackageDirs("windows", func(k string) string { return env[k] })
	want := []string{`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SystemPackageDirs(windows) = %v, want %v (in this order)", got, want)
	}
}

func TestSystemPackageDirs_WindowsPartiallySet(t *testing.T) {
	got := SystemPackageDirs("windows", func(k string) string {
		if k == "SystemRoot" {
			return `C:\Windows`
		}
		return ""
	})
	want := []string{`C:\Windows`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SystemPackageDirs(windows, SystemRoot only) = %v, want %v", got, want)
	}
}

func TestSystemPackageDirs_WindowsNoneSet(t *testing.T) {
	got := SystemPackageDirs("windows", func(string) string { return "" })
	if len(got) != 0 {
		t.Errorf("SystemPackageDirs(windows, nothing set) = %v, want empty", got)
	}
}

// A trailing separator on a Windows env var value must be stripped before
// it anchors Classify's boundary-aware prefix match — an unstripped one
// would double up with the "/" Classify itself appends and the check would
// silently never match anything real (e.g. "C:\Windows\" would never match
// "C:\Windows\System32\foo.exe").
func TestSystemPackageDirs_WindowsTrailingSeparatorStripped(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"backslash", `C:\Windows\`, `C:\Windows`},
		{"forward slash", `C:\Windows/`, `C:\Windows`},
		{"no trailing separator", `C:\Windows`, `C:\Windows`},
	}
	for _, c := range cases {
		got := SystemPackageDirs("windows", func(k string) string {
			if k == "SystemRoot" {
				return c.raw
			}
			return ""
		})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: SystemPackageDirs(windows, SystemRoot=%q) = %v, want [%q]", c.name, c.raw, got, c.want)
		}
	}
}

// A value that normalizes to nothing but a bare drive root ("C:" or "C:\")
// is skipped: treating an entire drive as a system directory would be a
// catastrophic false positive from a misconfigured or unusual environment.
func TestSystemPackageDirs_WindowsDriveRootSkipped(t *testing.T) {
	for _, raw := range []string{`C:`, `C:\`, `C:/`} {
		got := SystemPackageDirs("windows", func(k string) string {
			if k == "SystemRoot" {
				return raw
			}
			return ""
		})
		if len(got) != 0 {
			t.Errorf("SystemPackageDirs(windows, SystemRoot=%q) = %v, want empty (bare drive root)", raw, got)
		}
	}
	// A drive root alongside a legitimate value: only the drive root is
	// skipped, the real directory still comes through.
	got := SystemPackageDirs("windows", func(k string) string {
		switch k {
		case "SystemRoot":
			return `C:\`
		case "ProgramFiles":
			return `C:\Program Files`
		default:
			return ""
		}
	})
	want := []string{`C:\Program Files`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SystemPackageDirs(windows, drive root + real dir) = %v, want %v", got, want)
	}
}

// systemPackageManagerFor's redirect hint must name the right OS's own real
// tooling: a Linux/other-unix example set on Windows or macOS would send
// the reader looking for a package manager that was never involved.
// UpgradeCommand stays empty throughout: there is no single copy-pasteable
// command for a manager this package only identified by directory, not by
// which of several candidates actually owns the file.
func TestSystemPackageManagerFor(t *testing.T) {
	cases := []struct {
		goos     string
		name     string
		contains string
	}{
		{"linux", systemPackageManagerName, "apt"},
		{"freebsd", systemPackageManagerName, "pacman"},
		{"darwin", systemPackageManagerName, "macOS"},
		{"windows", systemPackageManagerName, "Windows Update"},
	}
	for _, c := range cases {
		m := systemPackageManagerFor(c.goos)
		if m.Name != c.name {
			t.Errorf("systemPackageManagerFor(%q).Name = %q, want %q", c.goos, m.Name, c.name)
		}
		if m.UpgradeCommand != "" {
			t.Errorf("systemPackageManagerFor(%q).UpgradeCommand = %q, want empty (prose belongs in UpgradeHint)", c.goos, m.UpgradeCommand)
		}
		if !strings.Contains(m.UpgradeHint, c.contains) {
			t.Errorf("systemPackageManagerFor(%q).UpgradeHint = %q, want it to contain %q", c.goos, m.UpgradeHint, c.contains)
		}
		if m.CanExecuteUpgrade() {
			t.Errorf("systemPackageManagerFor(%q) unexpectedly executes an upgrade", c.goos)
		}
	}
	// Windows and darwin must not accidentally get the POSIX example set.
	if windows := systemPackageManagerFor("windows"); strings.Contains(windows.UpgradeHint, "apt") {
		t.Errorf("Windows UpgradeHint = %q, must not mention apt", windows.UpgradeHint)
	}
	if darwin := systemPackageManagerFor("darwin"); strings.Contains(darwin.UpgradeHint, "apt") {
		t.Errorf("darwin UpgradeHint = %q, must not mention apt", darwin.UpgradeHint)
	}
}
