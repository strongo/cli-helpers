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
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
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
			// "true" is a real, portable no-op that keeps this fixture
			// honest about what CanExecuteUpgrade() means rather than
			// naming something that could never run at all — safe as the
			// default because most scenarios only ever reach it under
			// --dry-run (selfupdate.Config.UpdateAt's managed dry-run
			// branch returns ActionPlanned before ever calling RunManaged).
			// A REAL --yes run DOES invoke it, so a caller that wants one
			// (task-22 third review S4: "real --yes through an executable
			// manager") can point it at any other real, harmless command —
			// e.g. the `go` toolchain's own binary — via
			// FIXTURE_MANAGER_EXECUTABLE_PATH/_ARGS instead.
			exe := os.Getenv("FIXTURE_MANAGER_EXECUTABLE_PATH")
			if exe == "" {
				exe = "true"
			}
			var args []string
			if raw := os.Getenv("FIXTURE_MANAGER_EXECUTABLE_ARGS"); raw != "" {
				args = strings.Split(raw, ",")
			}
			m = m.WithExecutableUpgrade(exe, args...)
		}
		cfg.Managers = []selfupdate.Manager{m}
	}
	return cfg
}

// fixtureHookMarker returns an AfterUpdateFunc that appends outcome.Action
// to the file named by FIXTURE_HOOK_MARKER, one line per call — task-22
// review S4's own "$FIXTURE_HOOK_MARKER" — so a test can compare hook-
// invocation COUNT and CONTENT between self-update and upgrade <self> by
// reading the file after each real, non-dry-run run. Nil when the env var
// is unset, matching a host that configured no hook at all.
func fixtureHookMarker() selfupdate.AfterUpdateFunc {
	path := os.Getenv("FIXTURE_HOOK_MARKER")
	if path == "" {
		return nil
	}
	return func(_ context.Context, update selfupdate.AfterUpdate) error {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck
		_, err = fmt.Fprintln(f, update.Outcome.Action.String())
		return err
	}
}

// fixtureExitCode maps a failure kind to a distinct, arbitrary exit code —
// task-22 review S4's own "an ErrorMapper that gives each kind its own exit
// code" — so a test can compare exit codes, not just "zero vs non-zero",
// between self-update and upgrade <self> for the SAME underlying kind.
// codeFor is the ONE mapping both commands' ErrorMapper share, so a mapped
// code proves kind parity precisely because both sides consulted the
// identical table.
func fixtureExitCode(err error) int {
	switch selfupdate.KindOf(err) {
	case selfupdate.KindAmbiguous:
		return 3
	case selfupdate.KindReleaseLookup:
		return 4
	case selfupdate.KindNonInteractive:
		return 5
	default:
		return 1
	}
}

type fixtureExitError struct {
	code int
	err  error
}

func (e fixtureExitError) Error() string { return e.err.Error() }
func (e fixtureExitError) ExitCode() int { return e.code }
func (e fixtureExitError) Unwrap() error { return e.err }

// fixtureErrors implements BOTH selfupdate/cobracmd.ErrorMapper and
// cliinstall/cobracmd.ErrorMapper/UpgradeErrorMapper with the SAME
// fixtureExitCode table, so self-update and upgrade <self> map the
// identical failure kind to the identical exit code by construction.
type fixtureErrors struct{}

func (fixtureErrors) Failure(err error) error {
	return fixtureExitError{code: fixtureExitCode(err), err: err}
}
func (fixtureErrors) UpdateAvailable(selfupdate.CheckResult) error {
	return fixtureExitError{code: 2, err: fmt.Errorf("update available")}
}
func (fixtureErrors) UpgradesAvailable([]cliinstall.UpgradeResult) error {
	return fixtureExitError{code: 2, err: fmt.Errorf("update available")}
}

func main() {
	hook := fixtureHookMarker()
	root := &cobra.Command{Use: fixtureHostID}
	root.AddCommand(selfupdatecmd.New(fixtureConfig(), selfupdatecmd.CommandOptions{
		JSONFormat: true, AfterUpdate: hook, Errors: fixtureErrors{},
	}))
	root.AddCommand(cliinstallcmd.NewUpgrade(cliinstallcmd.UpgradeCommandOptions{
		HostID: fixtureHostID, HostConfig: fixtureConfig(),
		HostAfterUpdate: hook, Errors: fixtureErrors{},
	}))
	root.SetArgs(os.Args[1:])
	err := root.Execute()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err) //nolint:errcheck
	var ec fixtureExitError
	if errors.As(err, &ec) {
		os.Exit(ec.ExitCode())
	}
	os.Exit(1)
}
