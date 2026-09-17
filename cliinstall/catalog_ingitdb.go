package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

// ingitdbSnap models ingitdb's Snap distribution as a redirect-only manager:
// ingitdb-cli's current internal/selfupdate enum has no cli-helpers
// equivalent constructor, so it is declared directly here with the same
// marker ("/snap/") and upgrade command ("snap refresh ingitdb") that
// package already uses.
var ingitdbSnap = selfupdate.Manager{
	Name:           "Snap",
	UpgradeCommand: "snap refresh ingitdb",
	PathMarkers:    []string{"/snap/"},
}

func init() {
	register(Entry{
		ID:          "ingitdb",
		Homepage:    "https://ingitdb.com",
		Description: "A developer-grade, schema-validated, AI-native database whose storage engine is a Git repository.",
		Details: "inGitDB is a database whose storage engine is a Git repository: every record " +
			"is a plain YAML or JSON file, every change is a commit, and branching, review and " +
			"pull requests extend naturally to data. Collections are defined with typed schemas " +
			"that `ingitdb validate` checks.\n\n" +
			"inGitDB is a storage engine behind OpenVaultDB and Synchestra's own coordination " +
			"state, and DataTug and SpecScore Studio both read or export data in its record " +
			"encoding.",

		Repository: "ingitdb/ingitdb-cli",
		Managers: []selfupdate.Manager{
			// Redirect-only, matching ingitdb's current internal/selfupdate:
			// package-managed installs are print-and-exit, never replaced.
			selfupdate.Homebrew("brew upgrade --cask ingitdb"),
			ingitdbSnap,
		},
		// Matches ingitdb's .goreleaser.yaml: linux/darwin/windows x
		// amd64/arm64, minus windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		// ingitdb's real release publishes ONE flat checksums.txt for every
		// platform (verified: `gh release view` on the current tag lists
		// exactly "checksums.txt", no per-OS variants). Its own
		// internal/selfupdate package fetches "checksums-darwin.txt" and
		// "checksums-windows.txt" instead, which 404 — a known bug task-14
		// fixes by rebuilding self-update on this entry. The library
		// default ("<binary>_<version>_checksums.txt") would ALSO be wrong
		// here, so this is an explicit override, not a left-at-default.
		ChecksumsName: func(string, string) string { return "checksums.txt" },
		// VersionProbeArgs left at the shared default ({"--version"}):
		// ingitdb's rebuilt self-update (task-14) wires the same buildinfo
		// version reporting every other fleet CLI uses.

		CaskToken: "ingitdb/cli/ingitdb",
		CaskOS:    []string{"darwin", "linux"},
	})
}
