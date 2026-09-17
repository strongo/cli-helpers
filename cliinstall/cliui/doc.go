// Package cliui holds the framework-neutral parts of an install CLI's user
// interaction: the Row view type a caller assembles from cliinstall's own
// catalog, status and batch-result types; the text and JSON writers for a
// listing, a details/dry-run/install-result view, and the batch
// confirmation prompt. It imports neither Cobra nor any other command
// framework — a hand-rolled CLI with no framework at all can build its own
// "install" command straight from cliinstall.Probe/Install plus this
// package, and get the exact same formatting, labelling and confirmation
// behavior the cobracmd subpackage gives a Cobra-based CLI.
//
// This package deliberately mirrors
// [github.com/strongo/cli-helpers/selfupdate/cliui]'s own shape: a Confirm
// callback matching the core package's own confirmation signature, a
// ManagedCommandRunner-style reuse of that package's IsTerminal check
// rather than a second one, and text/JSON writer pairs named the same way.
// cobracmd is the Cobra-specific flag/wiring layer built on top of this
// package; this package has no dependency on it, or on Cobra, at all.
package cliui
