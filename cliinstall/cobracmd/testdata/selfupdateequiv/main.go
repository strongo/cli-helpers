// Command selfupdateequiv is a throwaway fixture binary, built and exec'd
// only by cliinstall/cobracmd's own TestSelfUpdateEqualsUpgradeSelf. It is
// never part of the module's own build graph — this directory is
// "testdata", which every Go tool (build, vet, golangci-lint) skips for
// "./..." patterns by convention — so it is compiled on demand, once, by
// that test's own `go build` call and exec'd like any other CLI.
//
// It registers BOTH a real self-update command (selfupdate/cobracmd.New)
// and a real upgrade command (cliinstall/cobracmd.NewUpgrade) for the same
// catalog host id ("cover100", the catalog's own generic test host — see
// cliinstall/catalog_cover100.go), built from the IDENTICAL selfupdate.
// Config (cli-install#req:host-target-is-running-binary: "The host MUST use
// its own self-update Config and options... so upgrade <self> and
// self-update reach the same library call").
//
// The whole point of running this as a REAL, separately-built and exec'd
// process, rather than calling selfupdatecmd.New/cliinstallcmd.NewUpgrade
// in-process from the test, is that selfupdate.Config.DetectSelf calls the
// unexported osExecutable (== os.Executable) with no test seam reachable
// from outside the selfupdate package itself — the ONLY way to control what
// self-update classifies its own running binary AS (manual, managed,
// ambiguous) is to actually build and place a real binary at a chosen path
// and let it detect itself for real. cliinstall's own host-row planning
// (cliinstall.UpgradeOptions.Env.HostDir, defaulted to
// cliinstall.DefaultInstallEnv()) resolves the SAME way — via the real
// os.Executable() of whichever process is running — so both commands
// naturally agree on "where am I" without any fakery at all; only the
// release endpoint and the declared Managers are configured through
// environment variables, which is the same "no network in tests"
// injection point cliinstall#req:no-network-in-tests already requires of
// production code.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	cliinstallcmd "github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
	selfupdatecmd "github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

// fixtureHostID is cliinstall's own generic test-host catalog entry
// (cliinstall/catalog_cover100.go) — it declares no Managers of its own, so
// this fixture's classification is entirely decided by the Managers
// fixtureConfig builds from FIXTURE_MANAGER_MARKER/FIXTURE_MANAGER_EXECUTABLE,
// never by anything cover100's real catalog entry declares.
const fixtureHostID = "cover100"

// fixtureConfig builds the ONE selfupdate.Config both commands share,
// exactly as REQ: host-target-is-running-binary requires of a real host:
// the release endpoint and Managers come from environment variables so the
// test controls them without any network access or real package manager.
func fixtureConfig() selfupdate.Config {
	endpoint := os.Getenv("FIXTURE_RELEASE_ENDPOINT")
	cfg := selfupdate.Config{
		BinaryName:     fixtureHostID,
		Repository:     "sneat-dev/cover100-cli",
		CurrentVersion: os.Getenv("FIXTURE_CURRENT_VERSION"),
		ReleasesAPIURL: endpoint + "/releases",
		DownloadURL: func(_, tag, asset string) string {
			return endpoint + "/" + tag + "/" + asset
		},
	}
	if marker := os.Getenv("FIXTURE_MANAGER_MARKER"); marker != "" {
		m := selfupdate.Manager{
			Name:           "testpm",
			UpgradeCommand: "testpm upgrade " + fixtureHostID,
			PathMarkers:    []string{marker},
		}
		if os.Getenv("FIXTURE_MANAGER_EXECUTABLE") == "1" {
			// "true" is never actually invoked by the test's own dry-run
			// scenarios (selfupdate.Config.UpdateAt's managed dry-run branch
			// returns ActionPlanned before ever calling RunManaged), but a
			// real, portable no-op executable name keeps this fixture
			// honest about what CanExecuteUpgrade() means rather than
			// naming something that could never run at all.
			m = m.WithExecutableUpgrade("true")
		}
		cfg.Managers = []selfupdate.Manager{m}
	}
	return cfg
}

func main() {
	root := &cobra.Command{Use: fixtureHostID}
	root.AddCommand(selfupdatecmd.New(fixtureConfig(), selfupdatecmd.CommandOptions{JSONFormat: true}))
	root.AddCommand(cliinstallcmd.NewUpgrade(cliinstallcmd.UpgradeCommandOptions{
		HostID:     fixtureHostID,
		HostConfig: fixtureConfig(),
	}))
	root.SetArgs(os.Args[1:])
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err) //nolint:errcheck
		os.Exit(1)
	}
}
