package cliinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/strongo/buildinfo"
	"github.com/strongo/cli-helpers/selfupdate"
)

// --- test fixtures -----------------------------------------------------

// fakeEnv builds an Env from plain maps/funcs so tests never touch a real
// PATH or exec a real installed binary (cli-install#req:no-network-in-
// tests).
type fakeEnv struct {
	pathDirs     []string
	hostDir      string
	hostErr      error
	executables  map[string]bool
	symlinks     map[string]string
	run          func(ctx context.Context, path string, args []string) ([]byte, error)
	evalDisabled bool
}

func (f *fakeEnv) env() Env {
	e := Env{
		PathDirs:     func() []string { return f.pathDirs },
		HostDir:      func() (string, error) { return f.hostDir, f.hostErr },
		IsExecutable: func(p string) bool { return f.executables[p] },
		Run:          f.run,
	}
	if !f.evalDisabled {
		e.EvalSymlinks = func(p string) (string, error) {
			if t, ok := f.symlinks[p]; ok {
				return t, nil
			}
			return p, nil
		}
	}
	return e
}

// jsonRun returns a Run func that always answers "version --json" with a
// buildinfo.VersionJSON object naming id, and errors on any other args
// (steps 2/3 never run when step 1 matches).
func jsonRun(id, version, commit, date, dateSource string) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, _ string, args []string) ([]byte, error) {
		if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
			b, _ := json.Marshal(buildinfo.VersionJSON{Name: id, Version: version, Commit: commit, Date: date, DateSource: dateSource})
			return b, nil
		}
		return nil, errors.New("unexpected probe step")
	}
}

var testEntry = Entry{ID: "testcli"}

// --- Probe / probeOne: locate -------------------------------------------

func TestProbe_NotInstalled(t *testing.T) {
	f := &fakeEnv{
		pathDirs:    []string{"/bin1", "/bin2"},
		executables: map[string]bool{},
		run:         func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("should not run") },
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	s := got[0]
	if s.ID != "testcli" || s.State != NotInstalled || s.Path != "" || s.OnPath || len(s.Warnings) != 0 {
		t.Errorf("got %+v, want NotInstalled with no path/warnings", s)
	}
}

func TestProbe_InstalledOnPath_FirstMatchIsPrimary(t *testing.T) {
	f := &fakeEnv{
		pathDirs: []string{"/bin1", "/bin2"},
		executables: map[string]bool{
			filepath.Join("/bin1", "testcli"): true,
			filepath.Join("/bin2", "testcli"): true,
		},
		run: jsonRun("testcli", "1.2.3", "abc123", "2026-01-01T00:00:00Z", buildinfo.DateSourceBuild),
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	s := got[0]
	if s.State != Installed {
		t.Fatalf("State = %v, want Installed", s.State)
	}
	if s.Path != filepath.Join("/bin1", "testcli") {
		t.Errorf("Path = %q, want /bin1 copy (first PATH match)", s.Path)
	}
	if !s.OnPath {
		t.Errorf("OnPath = false, want true")
	}
	if want := []string{filepath.Join("/bin2", "testcli")}; !equalStrings(s.OtherPaths, want) {
		t.Errorf("OtherPaths = %v, want %v", s.OtherPaths, want)
	}
	if len(s.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", s.Warnings)
	}
	if s.Version != "1.2.3" || s.Commit != "abc123" || s.Date != "2026-01-01T00:00:00Z" || s.DateSource != buildinfo.DateSourceBuild {
		t.Errorf("version fields = %+v", s)
	}
	if s.VersionSource != VersionSourceJSON {
		t.Errorf("VersionSource = %v, want VersionSourceJSON", s.VersionSource)
	}
}

func TestProbe_HostDirOnly_NotOnPathWarning(t *testing.T) {
	f := &fakeEnv{
		hostDir: "/host",
		executables: map[string]bool{
			filepath.Join("/host", "testcli"): true,
		},
		run: jsonRun("testcli", "9.9.9", "", "", ""),
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	s := got[0]
	if s.OnPath {
		t.Errorf("OnPath = true, want false (found only in host dir)")
	}
	if s.Path != filepath.Join("/host", "testcli") {
		t.Errorf("Path = %q", s.Path)
	}
	found := false
	for _, w := range s.Warnings {
		if w == fmt.Sprintf("%s is not on PATH", s.Path) {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a not-on-PATH warning", s.Warnings)
	}
}

func TestProbe_RelativePathEntryIgnored(t *testing.T) {
	f := &fakeEnv{
		pathDirs: []string{"relative/dir", "/abs/dir"},
		executables: map[string]bool{
			filepath.Join("relative/dir", "testcli"): true, // would match if not filtered
		},
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	if got[0].State != NotInstalled {
		t.Errorf("State = %v, want NotInstalled (relative PATH entry must be ignored)", got[0].State)
	}
}

func TestProbe_DirFlagSearched(t *testing.T) {
	f := &fakeEnv{
		executables: map[string]bool{
			filepath.Join("/custom", "testcli"): true,
		},
		run: jsonRun("testcli", "1.0.0", "", "", ""),
	}
	got := Probe(context.Background(), []Entry{testEntry}, "/custom", f.env(), ProbeOptions{})
	s := got[0]
	if s.State != Installed || s.Path != filepath.Join("/custom", "testcli") {
		t.Errorf("got %+v, want installed at /custom/testcli", s)
	}
	if s.OnPath {
		t.Errorf("OnPath = true, want false for a --dir-only copy")
	}
}

func TestProbe_SameDirOnPathAndHostDir_Deduplicated(t *testing.T) {
	f := &fakeEnv{
		pathDirs: []string{"/x"},
		hostDir:  "/x",
		executables: map[string]bool{
			filepath.Join("/x", "testcli"): true,
		},
		run: jsonRun("testcli", "1.0.0", "", "", ""),
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	s := got[0]
	if len(s.OtherPaths) != 0 {
		t.Errorf("OtherPaths = %v, want none (same path found twice must dedupe)", s.OtherPaths)
	}
}

func TestProbe_HostDirErrorSkipped(t *testing.T) {
	f := &fakeEnv{
		pathDirs:    []string{"/bin1"},
		hostErr:     errors.New("cannot resolve host dir"),
		executables: map[string]bool{filepath.Join("/bin1", "testcli"): true},
		run:         jsonRun("testcli", "1.0.0", "", "", ""),
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	if got[0].State != Installed {
		t.Errorf("State = %v, want Installed (host dir error must not fail the whole probe)", got[0].State)
	}
}

func TestProbe_WindowsExecutableSuffix(t *testing.T) {
	origGOOS := goosName
	t.Cleanup(func() { goosName = origGOOS })
	goosName = "windows"

	// Uses a POSIX-style absolute directory even though goosName is forced
	// to "windows": filepath.IsAbs runs under the actual host GOOS this
	// test executes on (this repository's tests always run on Linux/macOS
	// CI), so a real "C:\..." path would be filtered out as non-absolute
	// here. Only the ".exe" suffix decision is under test.
	f := &fakeEnv{
		pathDirs:    []string{"/bin"},
		executables: map[string]bool{filepath.Join("/bin", "testcli.exe"): true},
		run:         jsonRun("testcli", "1.0.0", "", "", ""),
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	s := got[0]
	if s.State != Installed {
		t.Fatalf("State = %v, want Installed (must search for the .exe suffix)", s.State)
	}
	if s.Path != filepath.Join("/bin", "testcli.exe") {
		t.Errorf("Path = %q, want the .exe path", s.Path)
	}
}

func TestProbe_PreservesTargetOrder(t *testing.T) {
	targets := []Entry{{ID: "b"}, {ID: "a"}, {ID: "c"}}
	f := &fakeEnv{}
	got := Probe(context.Background(), targets, "", f.env(), ProbeOptions{})
	if len(got) != 3 || got[0].ID != "b" || got[1].ID != "a" || got[2].ID != "c" {
		t.Errorf("got %v, want order preserved b,a,c", ids(got))
	}
}

func ids(statuses []Status) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = s.ID
	}
	return out
}

// --- Probe: unrecognized / timeout ---------------------------------------

func TestProbe_Unrecognized_NoStepMatches(t *testing.T) {
	f := &fakeEnv{
		pathDirs:    []string{"/bin"},
		executables: map[string]bool{filepath.Join("/bin", "testcli"): true},
		run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			switch {
			case len(args) == 2 && args[0] == "version" && args[1] == "--json":
				return []byte(`not json`), errors.New("bad")
			case len(args) == 1 && args[0] == "version":
				return []byte("othercli 1.0.0 (abc) 2026-01-01T00:00:00Z\n"), nil
			case len(args) == 1 && args[0] == "--version":
				return []byte("1.0.0\n"), nil
			}
			return nil, errors.New("unexpected args")
		},
	}
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{})
	s := got[0]
	if s.State != Unrecognized {
		t.Fatalf("State = %v, want Unrecognized", s.State)
	}
	if s.Output != "1.0.0" {
		t.Errorf("Output = %q, want the last step's captured output %q", s.Output, "1.0.0")
	}
}

func TestProbe_Timeout(t *testing.T) {
	f := &fakeEnv{
		pathDirs:    []string{"/bin"},
		executables: map[string]bool{filepath.Join("/bin", "testcli"): true},
		run: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	start := time.Now()
	got := Probe(context.Background(), []Entry{testEntry}, "", f.env(), ProbeOptions{Budget: 20 * time.Millisecond})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("Probe took %s, want well under the real 3s default (budget was overridden)", elapsed)
	}
	s := got[0]
	if s.State != Unrecognized {
		t.Fatalf("State = %v, want Unrecognized on timeout", s.State)
	}
	found := false
	for _, w := range s.Warnings {
		if w == fmt.Sprintf("timed out probing %s after %s", s.Path, 20*time.Millisecond) {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a timeout warning", s.Warnings)
	}
}

// --- Probe: concurrency ---------------------------------------------------

func TestProbe_ConcurrencyBounded(t *testing.T) {
	const concurrency = 3
	const numTargets = 7

	var (
		mu      sync.Mutex
		current int
		max     int
	)
	targets := make([]Entry, numTargets)
	f := &fakeEnv{pathDirs: []string{"/bin"}, executables: map[string]bool{}}
	for i := range targets {
		id := fmt.Sprintf("cli%d", i)
		targets[i] = Entry{ID: id}
		f.executables[filepath.Join("/bin", id)] = true
	}
	f.run = func(_ context.Context, _ string, args []string) ([]byte, error) {
		mu.Lock()
		current++
		if current > max {
			max = current
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		current--
		mu.Unlock()

		if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
			return []byte(`{}`), nil // no name -> falls through, but we only care about concurrency here
		}
		return nil, errors.New("stop")
	}

	got := Probe(context.Background(), targets, "", f.env(), ProbeOptions{Concurrency: concurrency, Budget: time.Second})
	if len(got) != numTargets {
		t.Fatalf("len = %d, want %d", len(got), numTargets)
	}

	mu.Lock()
	gotMax := max
	mu.Unlock()
	if gotMax > concurrency {
		t.Errorf("observed max concurrency %d, want <= %d", gotMax, concurrency)
	}
	if gotMax < 2 {
		t.Errorf("observed max concurrency %d, want > 1 (proves goroutines actually overlap)", gotMax)
	}
}

func TestProbeOptions_DefaultsAreProductionCorrect(t *testing.T) {
	opts := ProbeOptions{}.withDefaults()
	if opts.Concurrency != 4 {
		t.Errorf("default Concurrency = %d, want 4", opts.Concurrency)
	}
	if opts.Budget != 3*time.Second {
		t.Errorf("default Budget = %s, want 3s", opts.Budget)
	}

	overridden := ProbeOptions{Concurrency: 2, Budget: time.Millisecond}.withDefaults()
	if overridden.Concurrency != 2 || overridden.Budget != time.Millisecond {
		t.Errorf("withDefaults changed explicit values: %+v", overridden)
	}
}

// --- State / VersionSource String ------------------------------------------

func TestState_String(t *testing.T) {
	cases := map[State]string{
		NotInstalled: "not_installed",
		Installed:    "installed",
		Unrecognized: "unrecognized",
		State(99):    "unknown",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func TestVersionSource_String(t *testing.T) {
	cases := map[VersionSource]string{
		VersionSourceNone: "",
		VersionSourceJSON: "version_json",
		VersionSourceText: "version_text",
		VersionSourceFlag: "version_flag",
		VersionSource(99): "",
	}
	for source, want := range cases {
		if got := source.String(); got != want {
			t.Errorf("VersionSource(%d).String() = %q, want %q", source, got, want)
		}
	}
}

// --- allCatalogManagers / classifyLocated ---------------------------------

func TestAllCatalogManagers(t *testing.T) {
	managers := allCatalogManagers()
	if len(managers) == 0 {
		t.Fatal("allCatalogManagers() returned none")
	}
	if !sort.SliceIsSorted(managers, func(i, j int) bool { return managers[i].Name < managers[j].Name }) {
		t.Errorf("allCatalogManagers() not sorted by Name")
	}
	seen := map[string]bool{}
	for _, m := range managers {
		key := m.Name + "\x00" + fmt.Sprint(m.PathMarkers)
		if seen[key] {
			t.Errorf("duplicate manager in allCatalogManagers(): %+v", m)
		}
		seen[key] = true
	}
	foundSnap := false
	for _, m := range managers {
		if m.Name == "Snap" {
			foundSnap = true
			if !containsPath(m.PathMarkers, "/snap/") {
				t.Errorf("Snap manager markers = %v, want to contain /snap/", m.PathMarkers)
			}
		}
	}
	if !foundSnap {
		t.Errorf("allCatalogManagers() has no Snap manager, want ingitdb's declared one")
	}
}

func TestClassifyLocated_UnresolvedManaged_NeverCallsEvalSymlinks(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Homebrew", PathMarkers: []string{"/homebrew/"}}}
	env := Env{} // EvalSymlinks left nil deliberately
	det := classifyLocated("/opt/homebrew/bin/testcli", env, managers)
	if det.Method != selfupdate.Managed {
		t.Fatalf("Method = %v, want Managed", det.Method)
	}
}

func TestClassifyLocated_NilEvalSymlinks_FallsBackToUnresolved(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Homebrew", PathMarkers: []string{"/homebrew/"}}}
	env := Env{}
	det := classifyLocated("/home/user/go/bin/testcli", env, managers)
	if det.Method != selfupdate.Manual {
		t.Fatalf("Method = %v, want Manual (go/bin heuristic)", det.Method)
	}
}

func TestClassifyLocated_EvalSymlinksError_FallsBackToUnresolved(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Homebrew", PathMarkers: []string{"/homebrew/"}}}
	env := Env{EvalSymlinks: func(string) (string, error) { return "", errors.New("boom") }}
	det := classifyLocated("/home/user/go/bin/testcli", env, managers)
	if det.Method != selfupdate.Manual {
		t.Fatalf("Method = %v, want Manual", det.Method)
	}
}

func TestClassifyLocated_ResolvedEqualsPath_SkipsRecheck(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Homebrew", PathMarkers: []string{"/homebrew/"}}}
	env := Env{EvalSymlinks: func(p string) (string, error) { return p, nil }}
	det := classifyLocated("/opt/xyz/bin/testcli", env, managers)
	if det.Method != selfupdate.Manual {
		t.Fatalf("Method = %v, want Manual", det.Method)
	}
}

func TestClassifyLocated_UnresolvedMatch_PathFieldIsAsFound(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Snap", PathMarkers: []string{"/snap/"}}}
	env := Env{EvalSymlinks: func(p string) (string, error) { return "/usr/bin/snap", nil }}
	det := classifyLocated("/snap/bin/testcli", env, managers)
	// The unresolved path itself already contains "/snap/", so this proves
	// the primary (unresolved-match) path, not the resolved fallback.
	if det.Method != selfupdate.Managed || det.Path != "/snap/bin/testcli" {
		t.Fatalf("det = %+v, want Managed at the unresolved path", det)
	}
}

func TestClassifyLocated_OnlyResolvedMatchesManaged(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Snap", PathMarkers: []string{"/snap/"}}}
	env := Env{EvalSymlinks: func(p string) (string, error) { return "/opt/real/snap/dispatcher", nil }}
	det := classifyLocated("/usr/local/bin/testcli", env, managers)
	if det.Method != selfupdate.Managed {
		t.Fatalf("Method = %v, want Managed via resolved path", det.Method)
	}
	if det.Manager == nil || det.Manager.Name != "Snap" {
		t.Fatalf("Manager = %+v, want Snap", det.Manager)
	}
	if det.Path != "/usr/local/bin/testcli" {
		t.Errorf("Path = %q, want the unresolved (as-found) path preserved", det.Path)
	}
}

func TestClassifyLocated_NeitherMatches_Ambiguous(t *testing.T) {
	managers := []selfupdate.Manager{{Name: "Homebrew", PathMarkers: []string{"/homebrew/"}}}
	env := Env{EvalSymlinks: func(p string) (string, error) { return "/nowhere/special/testcli", nil }}
	det := classifyLocated("/opt/random/testcli", env, managers)
	if det.Method != selfupdate.Ambiguous {
		t.Fatalf("Method = %v, want Ambiguous", det.Method)
	}
}

// --- choosePrimary / containsPath ------------------------------------------

func TestChoosePrimary_FirstPathMatchWins(t *testing.T) {
	paths := []string{"/host/x", "/pathA/x", "/pathB/x"}
	onPath := []bool{false, true, true}
	primary, primaryOnPath, other := choosePrimary(paths, onPath)
	if primary != "/pathA/x" || !primaryOnPath {
		t.Fatalf("primary = %q onPath=%v, want /pathA/x true", primary, primaryOnPath)
	}
	want := []string{"/host/x", "/pathB/x"}
	if !equalStrings(other, want) {
		t.Errorf("other = %v, want %v", other, want)
	}
}

func TestChoosePrimary_NoPathMatch_FirstFoundWins(t *testing.T) {
	paths := []string{"/host/x", "/dir/x"}
	onPath := []bool{false, false}
	primary, primaryOnPath, other := choosePrimary(paths, onPath)
	if primary != "/host/x" || primaryOnPath {
		t.Fatalf("primary = %q onPath=%v, want /host/x false", primary, primaryOnPath)
	}
	if !equalStrings(other, []string{"/dir/x"}) {
		t.Errorf("other = %v", other)
	}
}

func TestContainsPath(t *testing.T) {
	paths := []string{"/a", "/b"}
	if !containsPath(paths, "/a") {
		t.Error("containsPath(/a) = false, want true")
	}
	if containsPath(paths, "/c") {
		t.Error("containsPath(/c) = true, want false")
	}
}

// --- searchDirs -------------------------------------------------------------

func TestSearchDirs_OrderAndFiltering(t *testing.T) {
	f := &fakeEnv{
		pathDirs: []string{"/p1", "relative", "/p2"},
		hostDir:  "/host",
	}
	rd := searchDirs(f.env(), "/dirflag")
	want := []string{"/p1", "/p2", "/host", "/dirflag"}
	if !equalStrings(rd.dirs, want) {
		t.Fatalf("dirs = %v, want %v", rd.dirs, want)
	}
	wantOnPath := []bool{true, true, false, false}
	for i, w := range wantOnPath {
		if rd.onPath[i] != w {
			t.Errorf("onPath[%d] = %v, want %v", i, rd.onPath[i], w)
		}
	}
}

func TestSearchDirs_NoDirFlag(t *testing.T) {
	f := &fakeEnv{pathDirs: []string{"/p1"}, hostErr: errors.New("no host")}
	rd := searchDirs(f.env(), "")
	if !equalStrings(rd.dirs, []string{"/p1"}) {
		t.Errorf("dirs = %v, want [/p1]", rd.dirs)
	}
}

// --- integration: real files, real exec, real symlinks --------------------

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are POSIX-only")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
}

// TestProbe_Integration_HangingBinaryIsKilledOnTimeout exercises DefaultEnv's
// real process execution against a real script that sleeps far longer than
// its budget, proving the context deadline actually kills the process
// instead of the test just waiting it out (cli-install#req:status-probe-
// bounded).
func TestProbe_Integration_HangingBinaryIsKilledOnTimeout(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "hangcli"), "sleep 5\n")

	env := DefaultEnv()
	env.PathDirs = func() []string { return []string{dir} }
	env.HostDir = func() (string, error) { return "", errors.New("no host dir in this test") }

	start := time.Now()
	got := Probe(context.Background(), []Entry{{ID: "hangcli"}}, "", env, ProbeOptions{Budget: 100 * time.Millisecond})
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("Probe took %s, want the process killed well under 3s", elapsed)
	}
	if got[0].State != Unrecognized {
		t.Fatalf("State = %v, want Unrecognized (timed out)", got[0].State)
	}
}

// TestProbe_Integration_SnapStyleDispatcher lays out a real symlink whose
// unresolved PATH location itself carries the "/snap/" marker (mirroring
// "/snap/bin/ingitdb", a symlink to the generic snap launcher) and proves
// Probe both identifies the target through the symlink (probing the
// as-found path, never the resolved one) and classifies it Managed/Snap
// using the real, compiled-in ingitdb catalog entry's declared marker.
func TestProbe_Integration_SnapStyleDispatcher(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	snapBinDir := filepath.Join(root, "snap", "bin")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(snapBinDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dispatcher := filepath.Join(realDir, "dispatcher")
	writeScript(t, dispatcher, `if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"name":"ingitdb","version":"1.2.3","commit":"abc123","date":"2026-01-01T00:00:00Z","date_source":"build"}'
fi
`)

	shim := filepath.Join(snapBinDir, "ingitdb")
	if err := os.Symlink(dispatcher, shim); err != nil {
		t.Fatal(err)
	}

	entry, ok := ByID("ingitdb")
	if !ok {
		t.Fatal("catalog has no ingitdb entry")
	}

	env := DefaultEnv()
	env.PathDirs = func() []string { return []string{snapBinDir} }
	env.HostDir = func() (string, error) { return "", errors.New("no host dir in this test") }

	got := Probe(context.Background(), []Entry{entry}, "", env, ProbeOptions{Budget: time.Second})
	s := got[0]
	if s.State != Installed {
		t.Fatalf("State = %v, want Installed", s.State)
	}
	if s.Path != shim {
		t.Errorf("Path = %q, want the symlink path %q (probed as found)", s.Path, shim)
	}
	if s.Method != selfupdate.Managed {
		t.Fatalf("Method = %v, want Managed", s.Method)
	}
	if s.Manager == nil || s.Manager.Name != "Snap" {
		t.Errorf("Manager = %+v, want Snap", s.Manager)
	}
	if s.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", s.Version)
	}
}

// --- resolvePath --------------------------------------------------------

func TestResolvePath_NilEvalSymlinks(t *testing.T) {
	if got := resolvePath("/a/b", Env{}); got != "/a/b" {
		t.Errorf("resolvePath = %q, want the path unchanged", got)
	}
}

func TestResolvePath_ErrorFallsBackToPath(t *testing.T) {
	env := Env{EvalSymlinks: func(string) (string, error) { return "", errors.New("no such file") }}
	if got := resolvePath("/a/b", env); got != "/a/b" {
		t.Errorf("resolvePath = %q, want the path unchanged on error", got)
	}
}

func TestResolvePath_ResolvesSymlink(t *testing.T) {
	env := Env{EvalSymlinks: func(string) (string, error) { return "/a/resolved", nil }}
	if got := resolvePath("/a/b", env); got != "/a/resolved" {
		t.Errorf("resolvePath = %q, want /a/resolved", got)
	}
}

// --- helpers ----------------------------------------------------------------

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
