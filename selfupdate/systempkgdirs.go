package selfupdate

import "strings"

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
//     Overwriting one does not typically make the manager refuse anything:
//     the manager simply does not know a foreign file landed on top of its
//     own, so the NEXT ordinary upgrade of that package (`apt upgrade`,
//     `dnf upgrade`, `pacman -Syu`) silently overwrites it again with the
//     package's own bytes — undoing our update without any error — while in
//     the meantime `dpkg --verify`, `rpm -V`, and `pacman -Qkk` all report
//     the file as modified against what the manager's database recorded.
//     Either way, the two copies — the manager's own and whatever self-
//     update wrote — fight over the same path, which is the failure mode
//     this check exists to avoid, not a refusal on our part. The Nix store
//     (`/nix/store`) is the same rule under a different mechanism: it is
//     content-addressed, and nothing but Nix is meant to write into it.
//     Whether a write there fails depends on the setup (NixOS mounts the
//     store read-only; a root process on other Linux or macOS, or the
//     owning user of a single-user install, can still write), but a
//     replaced file breaks the store path's content hash, and
//     `nix-store --verify --check-contents` reports it as corrupted;
//     NixOS's `/run/current-system` (and nix-darwin's, on macOS) is the
//     live symlink into that same store.
//
//  2. This is not a new rule invented for this feature — it is the exact
//     rule this package already applies to Homebrew, Scoop, WinGet, and
//     Snap (see Manager and Classify): never overwrite a manager-owned
//     install in place; redirect the user to that manager's own upgrade
//     command instead. An OS package manager is just one more manager whose
//     directories this package now recognizes by convention, which is what
//     lets the protection apply even to a consumer that configured no
//     Managers at all.
//
//  3. On a typical multi-user install these directories are writable only
//     by root (Administrator/TrustedInstaller on Windows), so a process
//     able to replace a file there is normally running elevated — a
//     self-update invoked through `sudo`, for instance — and "swap the
//     file" then means writing a binary downloaded from this package's own
//     GitHub release channel, bypassing the distribution's signed package
//     channel and its own integrity verification, as the machine's most
//     privileged user. (A single-user Nix install is the exception to
//     "writable only by root": the store belongs to that user, so the
//     reason to refuse is the store's content-addressing, per point 1, not
//     file ownership.)
//
//  4. This module's own destination-choosing logic (cliinstall's install
//     command) never plans a write into these directories in the first
//     place — see deniedRoots, which reuses this same list. So any copy
//     found inside one was placed by something else: almost always the
//     OS's own package manager.
//
//  5. `/usr/local/**` and anything under the user's home directory
//     (including `~/go/bin`) are deliberately EXCLUDED: both are the
//     conventional locations for software placed by hand, outside the
//     distribution's own package tree — the Filesystem Hierarchy
//     Standard's entire reason for splitting `/usr` from `/usr/local` — and
//     Homebrew's own Intel-Mac prefix (`/usr/local`) is already recognized
//     through Manager.PathMarkers (see Homebrew's own doc comment on the
//     Caskroom marker).
//
//     `/opt/**` is EXCLUDED too, but for a different, more deliberate
//     reason: the FHS defines `/opt` as the location for add-on application
//     software, and a vendor's own `.deb`/`.rpm` MAY legitimately install
//     there — unlike `/usr/local` or `$HOME`, a path under `/opt` is not
//     reliably a hand-placed manual install. It is excluded anyway because
//     manual/tarball installs commonly live under `/opt` too (this
//     repository's own `cover100`, for one), and there is no way to tell
//     the two apart from the path alone; treating every `/opt` path as
//     package-owned would make an ordinary manual install there
//     permanently un-self-updatable with no override. A consumer whose
//     catalog target really is `/opt`-installed by a package manager should
//     declare that manager's own PathMarkers (see Manager) instead of
//     relying on this built-in, path-only check.
//
// A binary a user placed in `/usr/local/bin`, `/opt/mytool/bin`, or
// `~/go/bin` by hand remains a manual install, eligible for self-replace
// exactly as it always was.
func SystemPackageDirs(goos string, getenv func(string) string) []string {
	switch goos {
	case "windows":
		return windowsSystemPackageDirs(getenv)
	case "darwin":
		// /usr/local is Homebrew's own Intel-Mac prefix (see Homebrew's doc
		// comment) and is deliberately absent here. /System is SIP-protected
		// on modern macOS regardless of privilege, but is still worth
		// classifying explicitly: the redirect message is more useful than
		// a permission failure with no explanation. macOS has no bare /lib
		// the way Linux does — its C libraries live under /usr/lib and
		// /System instead — so there is no separate /lib entry to list here.
		// /nix/store and /run/current-system cover a nix-darwin install,
		// exactly as on Linux (see point 1 above).
		return []string{
			"/usr/bin", "/usr/sbin", "/usr/libexec", "/bin", "/sbin", "/System",
			"/nix/store", "/run/current-system",
		}
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

// windowsSystemPackageDirs resolves SystemRoot/ProgramFiles/ProgramFiles(x86)
// through getenv, normalizing each value the way a real Windows environment
// variable needs before it can safely anchor a boundary-aware prefix match
// (Classify appends "/" to build that boundary — see inSystemPackageDir):
// a trailing separator is stripped, because an unstripped one would leave
// the appended boundary character doubled up and the match would silently
// never fire; and a value that normalizes to nothing but a bare drive root
// ("C:" or "C:\", once trimmed) is skipped entirely, because treating an
// entire drive as a system directory would be catastrophically wrong for a
// misconfigured or unusual environment — an empty or absent variable is
// already skipped the same way.
func windowsSystemPackageDirs(getenv func(string) string) []string {
	var dirs []string
	for _, name := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)"} {
		v := strings.TrimRight(getenv(name), `\/`)
		if v == "" || isWindowsDriveRoot(v) {
			continue
		}
		dirs = append(dirs, v)
	}
	return dirs
}

// isWindowsDriveRoot reports whether v (already trimmed of any trailing
// separator) is nothing but a bare drive letter and colon, e.g. "C:" — the
// result of a "C:\" environment variable value once its trailing separator
// is stripped.
func isWindowsDriveRoot(v string) bool {
	return len(v) == 2 && v[1] == ':'
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
// UpgradeCommand is deliberately left empty and the redirect prose goes in
// UpgradeHint instead: there is no single copy-pasteable command here (this
// package cannot know whether the real owner is apt, dnf, pacman, apk, or
// nix), and UpgradeCommand's own contract is "the exact command printed for
// the user to run" — printing prose there would make a renderer's "Run: "
// prefix lie. See Manager.UpgradeHint's own doc comment.
//
// The hint names goos's own real package-manager family rather than one
// fixed string for every OS: apt/dnf/pacman/apk/nix name nothing useful on
// Windows, and Windows Update names nothing useful on Linux — a wrong
// example is worse than a generic one because it sends the reader looking
// for a tool that was never involved.
func systemPackageManagerFor(goos string) Manager {
	hint := "the package manager that installed it (e.g. apt, dnf, pacman/AUR, apk, or nix)"
	switch goos {
	case "windows":
		hint = "Windows Update, or the installer (MSI/EXE) that originally placed it there"
	case "darwin":
		hint = "macOS itself (this directory is protected by System Integrity Protection), or whatever tool originally placed it there"
	}
	return Manager{Name: systemPackageManagerName, UpgradeHint: hint}
}
