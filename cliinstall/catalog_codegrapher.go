package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "codegrapher",
		Homepage:    "https://github.com/code-grapher/codegrapher",
		Description: "Builds and queries a SQLite knowledge graph of every symbol, edge and file in a codebase.",
		Details: "codegrapher is a code-intelligence tool: a single static binary that indexes a " +
			"codebase into a SQLite knowledge graph and answers queries against it — callers, " +
			"callees, blast-radius impact, and symbol-to-file paths — without a language " +
			"server.\n\n" +
			"codegrapher links source symbols to the SpecScore artifacts that implement them " +
			"(its Stable specscore-source-traceability feature), so a spec-driven fleet CLI can " +
			"query what code a spec requirement actually touches.",

		Repository: "code-grapher/codegrapher",
		Managers: []selfupdate.Manager{
			selfupdate.HomebrewCask("codegrapher"),
		},
		// Matches codegrapher's .goreleaser.yaml: linux/darwin/windows x
		// amd64/arm64, minus windows/arm64.
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		VersionProbeArgs: []string{"--version"},
		// codegrapher's .goreleaser.yaml publishes one flat checksums.txt
		// covering every platform, not the library's default
		// "<binary>_<version>_checksums.txt".
		ChecksumsName: func(string, string) string { return "checksums.txt" },

		CaskToken: "code-grapher/tap/codegrapher",
		CaskOS:    []string{"darwin", "linux"},
	})
}
