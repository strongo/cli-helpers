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
// Name, UpgradeCommand, and PathMarkers. A manager remains redirect-only
// unless WithExecutableUpgrade explicitly configures structured argv; the
// display command is never parsed or passed to a shell. The three
// constructors below exist
// because those three account for effectively every managed Go CLI install
// in the wild, and getting their marker sets right (see Homebrew's doc
// comment for the Intel-cask gotcha) is exactly the kind of detail this
// package exists to get right once instead of per consumer.
type Manager struct {
	// Name is shown to the user, e.g. "Homebrew".
	Name string
	// UpgradeCommand is the exact command printed for the user to run,
	// e.g. "brew upgrade --cask wb". It is display-only and is never parsed
	// or passed to a shell.
	UpgradeCommand string
	// UpgradeExecutable is the program invoked for an executable managed
	// update. Empty keeps this manager redirect-only for backward
	// compatibility. Configure it through WithExecutableUpgrade so its argv
	// is copied rather than aliased.
	UpgradeExecutable string
	// UpgradeArgs are passed directly to UpgradeExecutable without shell
	// parsing or interpolation.
	UpgradeArgs []string
	// UpgradeSteps are executed in order when non-empty. They allow a manager
	// whose local metadata must be refreshed before upgrade to express both
	// operations as structured argv without invoking a shell. A failed step
	// stops the sequence.
	UpgradeSteps []ManagedCommand
	// PathMarkers are lowercased, '/'-separated substrings; a resolved
	// executable path containing any one of them classifies as this
	// manager's install.
	PathMarkers []string
}

// ManagedCommand is one argv-safe package-manager process. Executable and Args
// are passed directly to ManagedCommandRunner; neither is shell parsed.
type ManagedCommand struct {
	Executable string
	Args       []string
}

// WithExecutableUpgrade opts this manager into executable updates. executable
// and args are passed directly to the consumer-supplied ManagedCommandRunner;
// UpgradeCommand remains the independently configured human-readable form.
// The argument slice is copied so later caller mutations cannot change the
// command that will run.
func (m Manager) WithExecutableUpgrade(executable string, args ...string) Manager {
	m.UpgradeExecutable = executable
	m.UpgradeArgs = append([]string(nil), args...)
	m.UpgradeSteps = nil
	return m
}

// WithExecutableUpgradeSteps opts this manager into an ordered executable
// update. It is intended for managers such as Homebrew where refreshing local
// metadata and upgrading the package are separate commands. Every argv slice
// is copied and empty executables are rejected by CanExecuteUpgrade.
func (m Manager) WithExecutableUpgradeSteps(steps ...ManagedCommand) Manager {
	m.UpgradeExecutable = ""
	m.UpgradeArgs = nil
	m.UpgradeSteps = make([]ManagedCommand, len(steps))
	for i, step := range steps {
		m.UpgradeSteps[i] = ManagedCommand{Executable: step.Executable, Args: append([]string(nil), step.Args...)}
	}
	return m
}

// CanExecuteUpgrade reports whether the consumer explicitly opted this manager
// into executable updates. A display-only UpgradeCommand is never sufficient.
func (m Manager) CanExecuteUpgrade() bool {
	if len(m.UpgradeSteps) > 0 {
		for _, step := range m.UpgradeSteps {
			if step.Executable == "" {
				return false
			}
		}
		return true
	}
	return m.UpgradeExecutable != ""
}

func (m Manager) executableUpgradeSteps() []ManagedCommand {
	if len(m.UpgradeSteps) > 0 {
		steps := make([]ManagedCommand, len(m.UpgradeSteps))
		for i, step := range m.UpgradeSteps {
			steps[i] = ManagedCommand{Executable: step.Executable, Args: append([]string(nil), step.Args...)}
		}
		return steps
	}
	return []ManagedCommand{{Executable: m.UpgradeExecutable, Args: append([]string(nil), m.UpgradeArgs...)}}
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
