package selfupdate

// Manager describes one package manager that might own the running binary's
// install. PathMarkers are lowercased, '/'-separated substrings of a
// resolved executable path that identify that manager's install layout —
// Classify normalizes both the candidate path and these markers the same
// way (lowercase, backslashes folded to forward slashes) so a Windows path
// can be classified on any host, including in tests.
//
// Consumers are not limited to Homebrew, Scoop, and WinGet: any manager can
// be described by constructing a Manager literal directly with its own
// Name, UpgradeCommand, and PathMarkers. The three constructors below exist
// because those three account for effectively every managed Go CLI install
// in the wild, and getting their marker sets right (see Homebrew's doc
// comment for the Intel-cask gotcha) is exactly the kind of detail this
// package exists to get right once instead of per consumer.
type Manager struct {
	// Name is shown to the user, e.g. "Homebrew".
	Name string
	// UpgradeCommand is the exact command printed for the user to run,
	// e.g. "brew upgrade --cask wb". The package never runs it — printing
	// it and stopping is the entire redirect behavior.
	UpgradeCommand string
	// PathMarkers are lowercased, '/'-separated substrings; a resolved
	// executable path containing any one of them classifies as this
	// manager's install.
	PathMarkers []string
}

// Homebrew describes a Homebrew-managed install (macOS, Linux, or
// Linuxbrew), covering both Formula and Cask installs.
//
// The marker set has one non-obvious entry: a GoReleaser homebrew_casks
// install resolves, through the symlink Homebrew creates, into a Caskroom
// path. On Apple Silicon that path already contains "/homebrew/" (it lives
// under /opt/homebrew/Caskroom/...) so the Cellar/Homebrew markers alone
// would catch it, but on Intel it is /usr/local/Caskroom/..., which matches
// none of the other markers — "/caskroom/" is required specifically so an
// Intel cask install classifies as managed instead of falling through to
// ambiguous.
func Homebrew(upgradeCommand string) Manager {
	return Manager{
		Name:           "Homebrew",
		UpgradeCommand: upgradeCommand,
		PathMarkers:    []string{"/cellar/", "/homebrew/", "/linuxbrew/", "/caskroom/"},
	}
}

// Scoop describes a Scoop-managed install (Windows). Both the versioned
// "apps" directory and the "shims" directory Scoop puts on PATH are
// markers, because either one may be the resolved, symlink-followed path
// depending on how the binary was invoked.
func Scoop(upgradeCommand string) Manager {
	return Manager{
		Name:           "Scoop",
		UpgradeCommand: upgradeCommand,
		PathMarkers:    []string{"/scoop/apps/", "/scoop/shims/"},
	}
}

// WinGet describes a WinGet-managed install (Windows Package Manager),
// under the user's local Microsoft\WinGet packages or links directory.
func WinGet(upgradeCommand string) Manager {
	return Manager{
		Name:           "WinGet",
		UpgradeCommand: upgradeCommand,
		PathMarkers:    []string{"/microsoft/winget/packages/", "/microsoft/winget/links/"},
	}
}
