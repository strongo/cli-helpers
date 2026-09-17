package selfupdate

// SystemPackageDirs returns the fixed set of directories that goos's own OS
// package manager(s) treat as theirs, so a resolved executable path inside
// one of them can be classified managed even when the consumer configured no
// Manager for it (REQ: system-package-dirs-are-managed). getenv resolves the
// environment-derived Windows roots; goos and getenv are both parameters —
// never read from the real environment inside this function — so every OS's
// list is exercisable from any host, the same pattern cliinstall's own
// destination denylist (deniedRoots) already uses.
//
// Why these directories get the same protection as a configured Manager:
//
//  1. Every file dpkg/apt, rpm/dnf, pacman (including the AUR), or apk
//     placed under its own tree is tracked in that manager's database.
//     Overwriting one — even with a binary that reports a newer version —
//     desyncs the database from the filesystem: `dpkg --verify` and
//     `rpm -V` report the file as modified, `pacman -Qkk` reports a
//     checksum mismatch, and the package's next upgrade either silently
//     reverts the overwrite (reinstalling its own bytes) or refuses,
//     conflicting with a file it no longer recognizes as its own.
//     Uninstalling the package later tries to remove a file the manager
//     never actually wrote. The Nix store (`/nix/store`) and NixOS's
//     `/run/current-system` are the same rule under a different mechanism:
//     both are content-addressed and rebuilt by Nix itself, never meant to
//     be written to directly by anything else.
//  2. This is not a new rule invented for this feature — it is the exact
//     rule this package already applies to Homebrew, Scoop, WinGet, and
//     Snap (see Manager and Classify): never overwrite a manager-owned
//     install in place; redirect the user to that manager's own upgrade
//     command instead. An OS package manager is just one more manager whose
//     directories this package now recognizes by convention, which is what
//     lets the protection apply even to a consumer that configured no
//     Managers at all.
//  3. These directories are writable only by an elevated user — root on
//     POSIX, Administrator/TrustedInstaller on Windows. A process able to
//     replace a file there is necessarily running elevated (a self-update
//     invoked through `sudo`, for instance), and "swap the file" then means
//     writing a binary downloaded from this package's own GitHub release
//     channel — bypassing the distribution's signed package channel and
//     its own integrity verification — as the machine's most privileged
//     user. The blast radius of a bad or compromised download is far wider
//     than a manual per-user install going wrong.
//  4. This module's own destination-choosing logic (cliinstall's install
//     command) never plans a write into these directories in the first
//     place — see deniedRoots, which reuses this same list. So any copy
//     found inside one was placed by something else: almost always the
//     OS's own package manager.
//  5. `/usr/local/**`, `/opt/**`, and anything under the user's home
//     directory (including `~/go/bin`) are deliberately EXCLUDED. All three
//     are the conventional locations for software placed by hand, outside
//     the distribution's own package tree — the Filesystem Hierarchy
//     Standard's entire reason for splitting `/usr` from `/usr/local` — and
//     Homebrew's own Intel-Mac prefix (`/usr/local`) is already recognized
//     through Manager.PathMarkers (see Homebrew's own doc comment on the
//     Caskroom marker). A binary a user placed in `/usr/local/bin`, `/opt/
//     mytool/bin`, or `~/go/bin` by hand remains a manual install, eligible
//     for self-replace exactly as it always was.
func SystemPackageDirs(goos string, getenv func(string) string) []string {
	switch goos {
	case "windows":
		var dirs []string
		for _, name := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)"} {
			if v := getenv(name); v != "" {
				dirs = append(dirs, v)
			}
		}
		return dirs
	case "darwin":
		// /usr/local is Homebrew's own Intel-Mac prefix (see Homebrew's doc
		// comment) and is deliberately absent here. /System is SIP-protected
		// on modern macOS regardless of privilege, but is still worth
		// classifying explicitly: the redirect message is more useful than
		// a permission failure with no explanation.
		return []string{"/usr/bin", "/usr/sbin", "/usr/libexec", "/bin", "/sbin", "/System"}
	default:
		// Linux and every other Unix this package might run on. /lib64 is
		// a separate root from /lib on 64-bit multilib distributions (RHEL/
		// Fedora family); both are listed because a package may own either.
		return []string{
			"/usr/bin", "/usr/sbin", "/usr/lib", "/usr/lib64", "/usr/libexec", "/usr/share",
			"/bin", "/sbin", "/lib", "/lib64",
			"/nix/store", "/run/current-system",
		}
	}
}

// systemPackageManagerName is the Manager.Name Classify reports for a path
// resolved inside SystemPackageDirs. It names the class of tool, not one
// specific manager, because Classify has no way to know which one actually
// owns a given file — that answer lives in each manager's own database
// (dpkg -S, rpm -qf, pacman -Qo, apk info --who-owns, nix-store -q
// --referrers, or Windows' own installed-app records), not in the file's
// path alone.
const systemPackageManagerName = "the system package manager"

// systemPackageManagerFor returns the built-in, redirect-only Manager
// Classify assigns to a path inside SystemPackageDirs(goos, ...). It never
// opts into executable updates (see Manager.CanExecuteUpgrade): self-update
// always redirects rather than executes, on every OS, because it cannot
// know which manager actually owns the file and so cannot safely choose one
// of their upgrade commands to run.
//
// The redirect text names goos's own real package-manager family rather
// than one fixed string for every OS: apt/dnf/pacman/apk/nix name nothing
// useful on Windows, and Windows Update names nothing useful on Linux — a
// wrong example is worse than a generic one because it sends the reader
// looking for a tool that was never involved.
func systemPackageManagerFor(goos string) Manager {
	upgradeCommand := "the package manager that installed it (e.g. apt, dnf, pacman/AUR, apk, or nix)"
	switch goos {
	case "windows":
		upgradeCommand = "Windows Update, or the installer (MSI/EXE) that originally placed it there"
	case "darwin":
		upgradeCommand = "macOS itself (this directory is protected by System Integrity Protection), or whatever tool originally placed it there"
	}
	return Manager{Name: systemPackageManagerName, UpgradeCommand: upgradeCommand}
}
