package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "ovdb",
		Homepage:    "https://github.com/openvaultdb/openvaultdb-go",
		Description: "The OpenVaultDB command-line interface for user-owned, portable databases with pluggable storage engines.",
		Details: "ovdb is the canonical developer/admin CLI for OpenVaultDB: it creates, runs " +
			"and operates instances of user-owned, portable databases backed by pluggable " +
			"storage engines, including SQLite and inGitDB.\n\n" +
			"inGitDB is one of ovdb's storage engines, so an inGitDB database an OpenVaultDB " +
			"instance keeps can be validated and edited directly with `ingitdb`, and explored " +
			"with DataTug.",

		Repository: "openvaultdb/ovdb",
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew("brew upgrade --cask ovdb").
				WithExecutableUpgrade("brew", "upgrade", "--cask", "ovdb"),
		},
		// Matches ovdb's .goreleaser.yaml: linux/darwin/windows x
		// amd64/arm64, minus windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		VersionProbeArgs: []string{"--version"},
		// ovdb's .goreleaser.yaml publishes one flat checksums.txt covering
		// every platform, not the library's default
		// "<binary>_<version>_checksums.txt".
		ChecksumsName: func(string, string) string { return "checksums.txt" },

		CaskToken: "openvaultdb/tap/ovdb",
		CaskOS:    []string{"darwin", "linux"},
	})
}
