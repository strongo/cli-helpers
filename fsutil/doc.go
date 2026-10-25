// Package fsutil provides small, UI-free filesystem path helpers shared by
// CLIs: home-directory expansion and directory-existence checks.
//
// It has no dependencies beyond the standard library, so any consumer (a
// terminal UI, a plain CLI, a daemon) can import it without pulling in a
// UI toolkit.
package fsutil
