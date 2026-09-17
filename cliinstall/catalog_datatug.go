package cliinstall

import "github.com/strongo/cli-helpers/selfupdate"

func init() {
	register(Entry{
		ID:          "datatug",
		Homepage:    "https://datatug.io",
		Description: "An open-source, CLI-first data exploration platform with a Web UI for querying and connecting data across sources.",
		Details: "DataTug is a CLI-first data exploration platform with a Web UI: it explores, " +
			"queries and connects data across multiple sources, surfacing related data even " +
			"across different systems so you can move naturally between datasets, queries and " +
			"results.\n\n" +
			"DataTug reads inGitDB databases directly and connects to OpenVaultDB servers as " +
			"`openvaultdb` catalogs; `datatug db copy` can move data into and out of both. Its " +
			"own specifications are SpecScore artifacts.",

		Repository: "datatug/datatug-cli",
		// datatug has no self-update today; task-15 adds one built from
		// this entry with HomebrewCask("datatug") steps.
		Managers: []selfupdate.Manager{
			selfupdate.HomebrewCask("datatug"),
		},
		// Matches datatug's .goreleaser.yaml: the datatug-unix build id
		// (linux/darwin x amd64/arm64) plus the separate datatug-windows
		// build id (windows/amd64 only).
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "windows", GOARCH: "amd64"},
		},
		// AssetName/ChecksumsName/VersionProbeArgs/UndeterminedVersions left
		// at the shared GoReleaser-shaped defaults: datatug_<version>_<os>_
		// <arch> archives and datatug_<version>_checksums.txt, matching its
		// own .goreleaser.yaml exactly — no pipeline change needed.

		CaskToken: "datatug/tap/datatug",
		CaskOS:    []string{"darwin", "linux"},
	})
}
