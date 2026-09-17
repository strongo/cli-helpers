package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "specscore",
		Homepage:    "https://specscore.md",
		Description: "Lint, query, and scaffold SpecScore specifications.",
		Details: "SpecScore is the CLI for SpecScore: it lints spec trees against structural " +
			"conventions, validates a project's features/plans/tasks, and scaffolds new spec " +
			"artifacts. `specscore studio index` exports facts about specs, code and manifests " +
			"as INGR recordsets for other tools to read.\n\n" +
			"Chatwright, Synchestra and DataTug are developed spec-first with SpecScore, and " +
			"`wb`'s CI profile runs `specscore spec lint` for every repository that has a " +
			"spec/ tree.",

		Repository: "specscore/specscore-cli",
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew("brew upgrade --cask specscore").
				WithExecutableUpgrade("brew", "upgrade", "--cask", "specscore"),
			selfupdate.Scoop("scoop update specscore").
				WithExecutableUpgrade("scoop", "update", "specscore"),
			selfupdate.WinGet("winget upgrade SpecScore.CLI").
				WithExecutableUpgrade("winget", "upgrade", "--id", "SpecScore.CLI"),
		},
		// Matches specscore's .goreleaser.yml: linux/darwin/windows x
		// amd64/arm64, minus windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		VersionProbeArgs: []string{"--version"},
		// AssetName/ChecksumsName left at the shared GoReleaser-shaped
		// defaults, matching specscore's own .goreleaser.yml exactly.

		CaskToken: "specscore/tap/specscore",
		CaskOS:    []string{"darwin", "linux"},
	})
}
