package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"
	update "github.com/strongo/cli-helpers/selfupdate"
	updatecmd "github.com/strongo/cli-helpers/selfupdate/cobracmd"
	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
	"github.com/strongo/cli-helpers/skillsync/reexec"
	refresh "github.com/strongo/cli-helpers/skillsync/selfupdate"
)

//go:embed skills
var content embed.FS

var buildVersion = "1.0.0"

func main() {
	source, err := fs.Sub(content, "skills")
	if err != nil {
		fatal(err)
	}
	digest, err := skillsync.Digest(source)
	if err != nil {
		fatal(err)
	}
	bundle, err := skillsync.EmbeddedBundle(skillsync.BundleDescriptor{
		Plugin: skillsync.PluginIdentity{Publisher: "fixture", Name: "plugin"},
		Source: skillsync.Source{
			Repository: "fixture/cli",
			Path:       "skills",
			Revision:   strings.Repeat(buildVersion[:1], 40),
			Version:    buildVersion,
			Digest:     digest,
		},
	}, source)
	if err != nil {
		fatal(err)
	}

	root := &cobra.Command{
		Use:           "fixture-cli",
		Version:       buildVersion,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			if len(os.Args) > 1 && os.Args[1] == "skills" && os.Getenv("FIXTURE_REFRESH_LOG") != "" {
				_ = os.WriteFile(os.Getenv("FIXTURE_REFRESH_LOG"), []byte(buildVersion+"\n"), 0o600)
			}
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(skillscmd.New(skillsync.Config{
		CLI:            skillsync.Identity{Publisher: "fixture", Name: "cli"},
		CurrentVersion: buildVersion,
		Bundles:        []skillsync.Bundle{bundle},
	}, skillscmd.CommandOptions{}))

	endpoint := os.Getenv("FIXTURE_RELEASE_ENDPOINT")
	root.AddCommand(updatecmd.New(update.Config{
		BinaryName:     "fixture-cli",
		Repository:     "fixture/cli",
		CurrentVersion: buildVersion,
		ReleasesAPIURL: endpoint + "/releases",
		DownloadURL: func(_, _, asset string) string {
			return endpoint + "/assets/" + asset
		},
	}, updatecmd.CommandOptions{
		JSONFormat: true,
		Interactive: func() bool {
			return os.Getenv("FIXTURE_INTERACTIVE") == "1"
		},
		AfterUpdate: refresh.AfterUpdate(reexec.Runner{
			Args:   []string{"skills", "sync", "--dir", os.Getenv("FIXTURE_SKILLS_DIR"), "--format", "json"},
			Stderr: os.Stderr,
		}),
	}))
	if err := root.Execute(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
