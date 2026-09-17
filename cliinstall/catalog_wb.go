package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "wb",
		Homepage:    "https://sneat.work/bench",
		Description: "Fleet-wide GitHub repository sync and config-driven recipes across many local clones, from the terminal.",
		Details: "wb keeps every local clone of your GitHub repositories in sync and runs " +
			"config-driven recipes across every repository that matches — no per-repository " +
			"scripting. It is part of Sneat.work's Workbench.\n\n" +
			"wb also runs governed commands, tracks worktree activity, and coordinates " +
			"cross-repository streams, so it is often the tool a fleet CLI's own CI and local " +
			"development already run through.",

		Repository: "sneat-dev/wb",
		Managers: []selfupdate.Manager{
			selfupdate.HomebrewCask("wb"),
		},
		// Matches wb's .goreleaser.yml builds.goos/goarch: darwin and linux
		// only, no windows.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		},
		// wb's own self-update probes its machine-readable version, not the
		// shared buildinfo default.
		VersionProbeArgs: []string{"version", "--json"},
		// wb's self-update declares "unknown" (collectVersion's own final
		// fallback) and "(devel)" (a `go build ./cmd/wb` module stamp) as
		// undetermined, neither of which is the library's "dev" default.
		UndeterminedVersions: []string{"unknown", "(devel)"},
		// AssetName/ChecksumsName left at selfupdate's GoReleaser-shaped
		// defaults: wb_<version>_<os>_<arch>.tar.gz and
		// wb_<version>_checksums.txt, matching wb's own .goreleaser.yml.

		CaskToken: "sneat-dev/tap/wb",
		CaskOS:    []string{"darwin", "linux"},
	})
}
