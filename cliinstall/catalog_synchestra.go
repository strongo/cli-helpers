package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "synchestra",
		Homepage:    "https://synchestra.io",
		Description: "A spec-driven coordination layer that manages prompts, specs and task queues for AI-assisted development.",
		Details: "Synchestra turns a git repository into a coordination protocol for AI agents: " +
			"it manages the inputs (prompts, specifications, task queues) and outputs (code, " +
			"documents, artifacts) of AI-driven development, keeping token usage minimal and " +
			"humans in the loop. It is built on SpecScore, which defines the spec format " +
			"Synchestra adds orchestration on top of.\n\n" +
			"Synchestra stores its own coordination state in inGitDB, so `ingitdb` can inspect " +
			"and validate it directly, and DataTug can explore it.",

		// The public mirror: this repository's own .goreleaser.yml sets
		// release.disable: true and publishes no GitHub Release here.
		// Every synchestra release lands in synchestra-io/synchestra-releases
		// under a "cli-" prefixed tag.
		Repository: "synchestra-io/synchestra-releases",
		TagPrefix:  "cli-",
		// No managers: synchestra publishes no homebrew_casks/scoops/winget
		// block; scripts/install.sh is its only documented install path.
		// Matches synchestra's .goreleaser.yml: linux/darwin/windows x
		// amd64/arm64, minus windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		VersionProbeArgs: []string{"version"},
		// AssetName/ChecksumsName/UndeterminedVersions left at the shared
		// GoReleaser-shaped defaults, matching synchestra's own
		// .goreleaser.yml and self-update Config exactly.
	})
}
