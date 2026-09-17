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

func TestSystemPackageDirs_Darwin(t *testing.T) {
	got := SystemPackageDirs("darwin", func(string) string { return "" })
	want := []string{"/usr/bin", "/usr/sbin", "/usr/libexec", "/bin", "/sbin", "/System"}
	if !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
		t.Errorf("SystemPackageDirs(darwin) = %v, want %v", got, want)
	}
	for _, excluded := range []string{"/usr/local", "/usr/lib64", "/nix/store"} {
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

// systemPackageManagerFor's redirect text must name the right OS's own real
// tooling: a Linux/other-unix example set on Windows or macOS would send
// the reader looking for a package manager that was never involved.
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
		if !strings.Contains(m.UpgradeCommand, c.contains) {
			t.Errorf("systemPackageManagerFor(%q).UpgradeCommand = %q, want it to contain %q", c.goos, m.UpgradeCommand, c.contains)
		}
		if m.CanExecuteUpgrade() {
			t.Errorf("systemPackageManagerFor(%q) unexpectedly executes an upgrade", c.goos)
		}
	}
	// Windows and darwin must not accidentally get the POSIX example set.
	if windows := systemPackageManagerFor("windows"); strings.Contains(windows.UpgradeCommand, "apt") {
		t.Errorf("Windows UpgradeCommand = %q, must not mention apt", windows.UpgradeCommand)
	}
	if darwin := systemPackageManagerFor("darwin"); strings.Contains(darwin.UpgradeCommand, "apt") {
		t.Errorf("darwin UpgradeCommand = %q, must not mention apt", darwin.UpgradeCommand)
	}
}
