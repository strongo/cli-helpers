// Package cliinstall carries the fleet's compiled-in catalog of installable
// CLIs (cli-install#req:catalog-compiled-in): a stable id per CLI, its
// release identity in the [github.com/strongo/cli-helpers/selfupdate]
// Config shape, its Homebrew cask coordinates, and the host -> target
// relevance texts a fleet CLI shows when it lists or explains its siblings.
//
// # Identity lives here, once
//
// Every per-CLI identity constructor — repository, tag prefix, managers,
// supported platforms, asset and checksum naming, version-probe arguments —
// lives in this package as an [Entry], never in selfupdate itself
// (cli-install#req:catalog-identity-single-source). A host builds its own
// self-update Config from its own Entry.Config, so its self-update and every
// other host's "install <that cli>" resolve releases identically. This
// couples a CLI's release naming to a cli-helpers release: a CLI that
// changes its GoReleaser archive or checksum naming must first update its
// catalog entry here.
//
// # What this package does not do
//
// cliinstall carries data and validates it against recorded snapshots
// (see the gen subpackage and TestCatalog*); it does not locate installed
// binaries, plan a destination, or install anything. Those are later
// pieces of the cli-install Feature, built on top of this catalog.
package cliinstall
