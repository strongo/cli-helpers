package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "sneat",
		Homepage:    "https://sneat.app",
		Description: "Sneat.app command-line interface: conversational agent, TUI, and Action Protocol client.",
		Details: "`sneat` is the command-line interface for Sneat.app: it provides a conversational " +
			"agent, a terminal UI for browsing spaces and contacts, and Action Protocol tools " +
			"for planning and executing actions.",

		Repository: "sneat-co/sneat-cli",
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew("brew upgrade --cask sneat").
				WithExecutableUpgrade("brew", "upgrade", "--cask", "sneat"),
		},
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		VersionProbeArgs: []string{"--version"},
		ChecksumsName:    func(string, string) string { return "checksums.txt" },

		CaskToken: "sneat-co/tap/sneat",
		CaskOS:    []string{"darwin", "linux"},
	})
}
