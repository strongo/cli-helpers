package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "chatwright",
		Homepage:    "https://chatwright.dev",
		Description: "Deterministic and AI-driven testing for conversational applications.",
		Details: "Chatwright is a testing CLI for conversational applications: it drives " +
			"platform-neutral scenarios against a chat surface both deterministically and with " +
			"AI-driven checks, so a conversational feature can be proven the way it is used.\n\n" +
			"Chatwright is developed spec-first with SpecScore: its product and CLI behavior " +
			"are specified as SpecScore features before they are built.",

		Repository: "chatwright/cli",
		Managers: []selfupdate.Manager{
			// Redirect-only, matching chatwright's own current self-update:
			// no WithExecutableUpgrade.
			selfupdate.Homebrew("brew upgrade --cask chatwright"),
		},
		// Matches chatwright's .goreleaser.yml: linux/darwin/windows x
		// amd64/arm64, minus windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		VersionProbeArgs: []string{"version"},
		// chatwright's self-update declares "dev" and "(devel)" as
		// undetermined; "dev" alone is already the library default, but is
		// listed explicitly here to reproduce chatwright's own Config.
		UndeterminedVersions: []string{"dev", "(devel)"},
		// AssetName/ChecksumsName left at the shared GoReleaser-shaped
		// defaults, matching chatwright's own .goreleaser.yml exactly.

		CaskToken: "chatwright/tap/chatwright",
		CaskOS:    []string{"darwin", "linux"},
	})
}
