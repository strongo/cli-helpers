package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "cover100",
		Homepage:    "https://cover100.dev",
		Description: "Collects Go and TypeScript/JavaScript test coverage and opens a zoomable local treemap of it.",
		Details: "`cover100` collects test coverage from Go and TypeScript/JavaScript projects, " +
			"normalizes it into one JSON document, and opens a zoomable treemap in your " +
			"browser: box area is code volume, box colour is coverage, so the biggest untested " +
			"surface is impossible to miss. Nothing is uploaded and no server is left running.\n\n" +
			"`wb coverage --fleet` shows which repositories in a local fleet need attention; " +
			"run cover100 in one of them for the visual view, and `codegrapher callers` shows " +
			"what calls into an uncovered file.",

		Repository: "sneat-dev/cover100-cli",
		// No managers: cover100 publishes only plain GitHub release
		// archives, matching its own self-update Config exactly.
		// Every combination cover100's .goreleaser.yml publishes, including
		// windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
			{GOOS: "windows", GOARCH: "arm64"},
		},
		VersionProbeArgs: []string{"--version"},
		// AssetName/ChecksumsName/UndeterminedVersions left at the shared
		// GoReleaser-shaped defaults, matching cover100's own
		// .goreleaser.yml and its self-update Config exactly.
	})
}
