package cliinstall

import (
	"fmt"
	"os"
	"strings"

	"github.com/strongo/cli-helpers/selfupdate"
)

// getwdFunc is a test seam over os.Getwd, used only to resolve a relative
// --dir against the working directory (cli-install#req:install-method-
// mirrors-host: "a relative --dir resolves against the working directory").
var getwdFunc = os.Getwd

// isAbsPath reports whether p is an absolute path for goos, without relying
// on path/filepath's build-time OS behavior — this package's own tests
// exercise Windows-shaped paths (e.g. `C:\Program Files`) from a POSIX test
// binary via an injected goos, and path/filepath always behaves like the
// host OS it was compiled for regardless of that injection.
func isAbsPath(goos, p string) bool {
	if goos == "windows" {
		if len(p) >= 2 && p[1] == ':' {
			return true
		}
		return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//")
	}
	return strings.HasPrefix(p, "/")
}

// joinPath joins parts with goos's own separator, trimming any separators
// already trailing each part — the same "no build-time OS dependency"
// reasoning as isAbsPath applies here.
func joinPath(goos string, parts ...string) string {
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	cleaned := make([]string, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimRight(p, `/\`)
		if i > 0 {
			// Only the first part may keep a leading separator (an
			// absolute POSIX root); every later part's own leading
			// separator would otherwise double up with the one Join adds.
			p = strings.TrimLeft(p, `/\`)
		}
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return strings.Join(cleaned, sep)
}

// installFilePath builds the full destination file path for id inside dir,
// applying goos's platform executable suffix — the file path Locate itself
// would find (cli-install#req:status-locate).
func installFilePath(goos, dir, id string) string {
	name := id
	if goos == "windows" {
		name += ".exe"
	}
	return joinPath(goos, dir, name)
}

// resolveDir turns a --dir value into an absolute, symlink-resolved path
// (cli-install#req:destination-denylist: "including --dir (resolved to an
// absolute, symlink-resolved path)"). A relative value is joined against
// the working directory; when evalSymlinks is nil or errors, resolution
// falls back to the absolute-but-unresolved path, mirroring DetectSelf's
// own fallback.
func resolveDir(raw, goos string, evalSymlinks func(string) (string, error)) string {
	p := raw
	if !isAbsPath(goos, p) {
		if cwd, err := getwdFunc(); err == nil {
			p = joinPath(goos, cwd, p)
		}
	}
	if evalSymlinks != nil {
		if resolved, err := evalSymlinks(p); err == nil {
			return resolved
		}
	}
	return p
}

// normalizeSlashes lowercases s and folds backslashes to forward slashes,
// matching selfupdate.Classify's own comparison convention so a denylist
// root and a candidate path compare the same way regardless of case or
// separator style.
func normalizeSlashes(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), `\`, "/")
}

// deniedRoots returns REQ: destination-denylist's fixed roots for goos.
// Windows roots and $GOROOT are read through getenv so both are exercisable
// on any host and deterministic in tests; an unset Windows env var is
// simply skipped rather than guessed at, since a real Windows process
// always has it set.
func deniedRoots(goos string, getenv func(string) string) []string {
	var roots []string
	if goos == "windows" {
		for _, name := range []string{"ProgramData", "ProgramFiles", "ProgramFiles(x86)", "SystemRoot"} {
			if v := getenv(name); v != "" {
				roots = append(roots, v)
			}
		}
	} else {
		roots = append(roots, "/usr", "/bin", "/sbin", "/lib",
			"/opt/homebrew", "/home/linuxbrew/.linuxbrew", "/snap", "/nix")
	}
	if goroot := getenv("GOROOT"); goroot != "" {
		roots = append(roots, goroot)
	}
	return roots
}

// destinationDenylistFailure reports REQ: destination-denylist's
// no-install-directory failure when resolvedDir is inside a fixed system
// root or a catalog manager's own path markers, and nil otherwise. There is
// no override for either case.
func destinationDenylistFailure(resolvedDir, goos string, getenv func(string) string) *selfupdate.Failure {
	normalized := normalizeSlashes(resolvedDir)
	for _, root := range deniedRoots(goos, getenv) {
		nr := normalizeSlashes(root)
		if normalized == nr || strings.HasPrefix(normalized, nr+"/") {
			return &selfupdate.Failure{
				Kind: selfupdate.KindNoInstallDir,
				Err:  fmt.Errorf("%s is inside the protected directory %s; choose a different --dir", resolvedDir, root),
			}
		}
	}
	if selfupdate.Classify(resolvedDir, allCatalogManagers()).Method == selfupdate.Managed {
		return &selfupdate.Failure{
			Kind: selfupdate.KindNoInstallDir,
			Err:  fmt.Errorf("%s is inside a package manager's own directory; choose a different --dir", resolvedDir),
		}
	}
	return nil
}

// perUserBinDir resolves cli-install#req:per-user-bin-dir's fixed per-user
// directory for goos: "$HOME/.local/bin" on Linux/macOS,
// "%LOCALAPPDATA%\Programs\strongo\bin" on Windows. It neither creates the
// directory nor checks PATH membership — see dirOnPath for that.
func perUserBinDir(goos string, userHomeDir func() (string, error), getenv func(string) string) (string, error) {
	if goos == "windows" {
		base := getenv("LOCALAPPDATA")
		if base == "" {
			return "", fmt.Errorf("%%LOCALAPPDATA%% is not set")
		}
		return joinPath(goos, base, "Programs", "strongo", "bin"), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return joinPath(goos, home, ".local", "bin"), nil
}

// cleanForCompare prepares p for REQ: per-user-bin-dir's "compared as a
// cleaned absolute path (case-insensitively on Windows)" PATH-membership
// check: trailing separators trimmed, and — on Windows only — lowercased
// with separators folded to a single style, since a Windows PATH entry may
// use either slash style.
func cleanForCompare(p, goos string) string {
	p = strings.TrimRight(p, `/\`)
	if goos == "windows" {
		p = strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	}
	return p
}

// dirOnPath reports whether dir is present in pathDirs once both are
// cleaned for comparison.
func dirOnPath(pathDirs []string, dir, goos string) bool {
	want := cleanForCompare(dir, goos)
	for _, d := range pathDirs {
		if cleanForCompare(d, goos) == want {
			return true
		}
	}
	return false
}

// caskSupportsOS reports whether target's cask declares support for goos.
func caskSupportsOS(target Entry, goos string) bool {
	for _, os := range target.CaskOS {
		if os == goos {
			return true
		}
	}
	return false
}

// planMethod implements cli-install#req:install-method-mirrors-host's
// four-step order, applying cli-install#req:destination-denylist to every
// direct-install destination including --dir. hostDir is the running
// host's own executable directory (Env.HostDir); an empty hostDir (the
// directory could not be resolved) classifies as Ambiguous and falls
// through to the per-user bin directory, the same as any other
// unclassifiable host.
func planMethod(hostEntry Entry, hostDir string, target Entry, opts Options) (method Method, destDir, caskToken string, createIfMissing bool, failure *selfupdate.Failure) {
	getenv := opts.Env.Getenv

	if opts.Dir != "" {
		resolved := resolveDir(opts.Dir, goosName, opts.Env.EvalSymlinks)
		if f := destinationDenylistFailure(resolved, goosName, getenv); f != nil {
			return 0, "", "", false, f
		}
		return MethodDirect, resolved, "", false, nil
	}

	hostExePath := installFilePath(goosName, hostDir, hostEntry.ID)
	hostDetection := selfupdate.Classify(hostExePath, hostEntry.Managers)

	if hostDetection.Method == selfupdate.Managed && hostDetection.Manager != nil &&
		hostDetection.Manager.Name == "Homebrew" && target.HasCask() && caskSupportsOS(target, goosName) {
		return MethodHomebrew, "", target.CaskToken, false, nil
	}

	if hostDetection.Method == selfupdate.Manual {
		if f := destinationDenylistFailure(hostDir, goosName, getenv); f == nil {
			return MethodDirect, hostDir, "", false, nil
		}
	}

	perUser, err := perUserBinDir(goosName, opts.Env.UserHomeDir, getenv)
	if err != nil {
		return 0, "", "", false, &selfupdate.Failure{
			Kind: selfupdate.KindNoInstallDir,
			Err:  fmt.Errorf("determine the per-user bin directory: %w; pass --dir instead", err),
		}
	}
	if !dirOnPath(opts.Env.PathDirs(), perUser, goosName) {
		return 0, "", "", false, &selfupdate.Failure{
			Kind: selfupdate.KindNoInstallDir,
			Err:  fmt.Errorf("%s is not on PATH; add it to PATH, or pass --dir", perUser),
		}
	}
	return MethodDirect, perUser, "", true, nil
}
