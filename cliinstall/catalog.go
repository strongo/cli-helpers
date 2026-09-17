package cliinstall

import (
	"sort"

	"github.com/strongo/cli-helpers/selfupdate"
)

//go:generate go run ./gen

// Entry is one compiled-in catalog record: a fleet CLI's stable identity,
// its release identity in the Self-Update Library's Config shape, its
// Homebrew cask coordinates, and the descriptive text a host shows a user
// before installing it (cli-install#req:catalog-entry-identity).
type Entry struct {
	// ID is the catalog id, equal to the binary name
	// (cli-install#req:host-identity-from-catalog).
	ID string
	// Homepage is the CLI's canonical homepage URL.
	Homepage string
	// Description is a one-line description shown in a listing row.
	Description string
	// Details is a longer description of at most a few short paragraphs,
	// shown before a confirmation prompt
	// (cli-install#req:details-before-install).
	Details string

	// Repository is "owner/repo" on GitHub that publishes this CLI's
	// releases — see selfupdate.Config.Repository.
	Repository string
	// TagPrefix selects this binary's releases within Repository when that
	// repository publishes more than one product's releases — see
	// selfupdate.Config.TagPrefix.
	TagPrefix string
	// Managers are the package managers that might own this binary's
	// install, exactly as this CLI's own self-update declares them — see
	// selfupdate.Config.Managers.
	Managers []selfupdate.Manager
	// SupportedPlatforms restricts install and self-replace to the
	// GOOS/GOARCH pairs this CLI's release actually publishes — see
	// selfupdate.Config.SupportedPlatforms.
	SupportedPlatforms []selfupdate.Platform
	// VersionProbeArgs are the arguments run against a newly installed copy
	// to confirm its version. A zero value leaves selfupdate.Config's own
	// default ({"--version"}) in effect.
	VersionProbeArgs []string
	// UndeterminedVersions lists the CurrentVersion values that mean "this
	// build cannot say its own version". A zero value leaves
	// selfupdate.Config's own default ({"dev"}) in effect.
	UndeterminedVersions []string
	// AssetName names this CLI's release archive for one version/platform
	// combination. Nil leaves selfupdate.Config's GoReleaser-shaped default
	// in effect, which every catalog entry's archive naming matches.
	AssetName func(binary, version, goos, goarch string) string
	// ChecksumsName names this CLI's release checksums file for one
	// version. Nil leaves selfupdate.Config's GoReleaser-shaped default
	// ("<binary>_<version>_checksums.txt") in effect; some CLIs publish a
	// single flat "checksums.txt" instead and override this.
	ChecksumsName func(binary, version string) string

	// CaskToken is the argument to `brew install --cask`, tap-qualified
	// (e.g. "sneat-dev/tap/wb"). Empty means this CLI publishes no
	// Homebrew cask.
	CaskToken string
	// CaskOS lists the GOOS values ("darwin", "linux") CaskToken's cask
	// supports. Empty when CaskToken is empty.
	CaskOS []string

	// LegacyVersionSignatures optionally lists bare `--version` output
	// patterns that identify an old build of this CLI, for the
	// status-probe-order fallback step
	// (cli-install#req:status-probe-order). Empty when no such pattern is
	// declared for this CLI.
	LegacyVersionSignatures []string

	// SelfUpdateHooks reports whether this CLI's own `self-update` performs
	// after-update work beyond the swap itself — a daemon restart, a skills
	// re-sync — via selfupdate.Options.AfterUpdate
	// (cli-install#req:self-update-hook-hint). Upgrade does not run another
	// CLI's hooks itself; when a target other than the host has this set and
	// is upgraded or has its manager command executed, the result carries a
	// `<target> self-update` finish hint instead. True today for wb (daemon
	// restart, skills sync) and codegrapher (skills sync) — verified against
	// each repository's own self-update command wiring; every other catalog
	// entry leaves this at its false zero value.
	SelfUpdateHooks bool
}

// Config returns e's release identity as a selfupdate.Config for a build
// currently reporting currentVersion (cli-install#req:catalog-identity-
// single-source). It reproduces e's fields exactly; a caller that needs
// something only its own self-update requires (an AfterUpdate hook, for
// instance, which is a cobracmd.CommandOptions field, not a Config one) adds
// it outside this method.
func (e Entry) Config(currentVersion string) selfupdate.Config {
	return selfupdate.Config{
		BinaryName:           e.ID,
		Repository:           e.Repository,
		CurrentVersion:       currentVersion,
		UndeterminedVersions: e.UndeterminedVersions,
		Managers:             e.Managers,
		SupportedPlatforms:   e.SupportedPlatforms,
		TagPrefix:            e.TagPrefix,
		VersionProbeArgs:     e.VersionProbeArgs,
		AssetName:            e.AssetName,
		ChecksumsName:        e.ChecksumsName,
	}
}

// HasCask reports whether e publishes a Homebrew cask.
func (e Entry) HasCask() bool {
	return e.CaskToken != ""
}

// catalog holds every compiled-in Entry, keyed by id. Populated by the
// per-CLI catalog_<id>.go files' init functions via register.
var catalog = map[string]Entry{}

// register adds e to the compiled-in catalog. It is called only from this
// package's own init functions (one per catalog_<id>.go file) and panics on
// a duplicate id, which would be a programming error caught at package
// init, never a runtime state a caller can trigger.
func register(e Entry) {
	if _, exists := catalog[e.ID]; exists {
		panic("cliinstall: duplicate catalog id " + e.ID)
	}
	catalog[e.ID] = e
}

// Entries returns every catalog entry, sorted by id, as a defensive copy —
// mutating the returned slice or its elements' slice/func fields never
// affects the compiled-in catalog.
func Entries() []Entry {
	ids := IDs()
	out := make([]Entry, len(ids))
	for i, id := range ids {
		out[i] = catalog[id]
	}
	return out
}

// IDs returns every catalog id, sorted.
func IDs() []string {
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ByID returns the catalog entry for id and whether it was found
// (cli-install#req:host-identity-from-catalog).
func ByID(id string) (Entry, bool) {
	e, ok := catalog[id]
	return e, ok
}

// Relevance is one host -> target row of the catalog's relevance matrix
// (cli-install#req:relevance-matrix): why a user of the host would want the
// target.
type Relevance struct {
	// Target is the relevant CLI's catalog id.
	Target string
	// Text is the relevance text written for this exact host/target pair.
	Text string
}

// Relevant returns hostID's relevant targets in matrix order
// (cli-install#req:relevance-matrix, cli-install#req:list-relevant). An
// unknown or catalog-absent hostID, or one with no relevant targets, both
// return nil.
func Relevant(hostID string) []Relevance {
	var out []Relevance
	for _, row := range matrixRows {
		if row.Host == hostID {
			out = append(out, Relevance{Target: row.Target, Text: row.Text})
		}
	}
	return out
}
