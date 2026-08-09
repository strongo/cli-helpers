// Package selfupdate lets a Go CLI update its own binary in place.
//
// The problem it solves is not the download — that part is easy — but the
// decision of whether a swap is safe at all. A binary installed by Homebrew,
// Scoop, or WinGet is owned by that manager's bookkeeping; overwriting it out
// from under the manager leaves the manager's records pointing at a version
// that no longer matches the file on disk, and the next "brew upgrade" fights
// the CLI's own write. So the package classifies how the running binary got
// where it is before it does anything else: a managed install is redirected
// to the manager's own upgrade command and never touched, a manual install
// (a release archive someone unpacked, or a `go install` target) is eligible
// for replacement, and anything the package cannot confidently place in
// either bucket is treated as manual-adjacent risk and refused — ambiguity
// never resolves to "safe to overwrite".
//
// The second problem is that the update code path is the one place a bug
// leaves the user with no working tool: a partially written executable
// cannot re-run itself to recover. Every write the package performs is
// therefore staged to a temporary file on the same filesystem as the target
// and moved into place with a single atomic rename, and every step that can
// fail — release lookup, download, checksum mismatch, staging, permission —
// fails before that rename, leaving the previous binary exactly as it was.
//
// # Identity stays with the caller
//
// Everything specific to one CLI — its binary name, GitHub repository,
// current version, which strings mean "this build cannot say its version",
// the managers that might own its install and their upgrade commands, the
// asset/checksum naming convention, the version-probe arguments, and which
// platforms it publishes — is supplied through Config. The package hard-codes
// none of it, which is what lets two CLIs with incompatible exit-code
// conventions both build a working self-update command from the same
// Config shape (see the cobracmd subpackage).
//
// # What the core does not do
//
// Config.Update and Config.Check perform no I/O beyond the GitHub HTTP
// requests and the filesystem writes the update itself requires: they never
// print to a terminal, never read from stdin, and never decide a process
// exit code. Confirmation is a caller-supplied callback (Options.Confirm);
// output formatting and exit-code mapping belong to the caller or to the
// optional cobracmd adapter. This is what makes the package usable by a CLI
// with any output convention, and what makes its own test suite able to
// exercise every path without a network connection or a real installed
// binary — every GitHub endpoint, filesystem call, and executable-resolution
// call the package makes is an overridable package-level seam.
//
// # Typical use
//
// A CLI builds one Config describing itself, then either calls Config.Check
// for a read-only availability report, or Config.Update to perform (or plan,
// via Options.DryRun) the replacement. The cobracmd subpackage wraps both
// behind a ready-made Cobra command for CLIs that use that framework; the
// root package has no dependency on it, so a CLI built on any other command
// framework — or none — can call Config.Update directly.
package selfupdate
