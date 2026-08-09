// Command selfupdate is the reference consumer of github.com/strongo/
// selfupdate: it exists so the package's own release path is genuinely
// exercised — something has to actually download, verify, and swap a real
// executable, and it should be this repository's own binary rather than a
// downstream CLI's users finding a bug first (REQ: reference-cli-single-
// command).
//
// It is deliberately thin: every behavior it exposes comes from the public
// selfupdate and cobracmd packages, wired the same way any other consumer
// would wire them. Nothing here reaches into an unexported symbol — if this
// CLI needs something, by construction every consumer already has access to
// it.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/strongo/selfupdate"
	"github.com/strongo/selfupdate/cobracmd"
)

// version is overridden at link time by the release build
// (-ldflags "-X main.version=..."; see .goreleaser.yml). An unstamped local
// build keeps the undeterminedVersion placeholder, so `selfupdate --version`
// and `selfupdate self-update --check` both honestly report "this build
// doesn't know its own version" instead of pretending to be a release
// (REQ: reference-cli-self-hosting).
var version = undeterminedVersion

// undeterminedVersion is the placeholder CurrentVersion this CLI declares as
// its one UndeterminedVersions entry.
const undeterminedVersion = "undetermined"

// repository is this module's own GitHub repository: the reference CLI
// updates itself from the same releases the package's default AssetName/
// ChecksumsName/DownloadURL conventions already point at, so building this
// Config takes no overrides beyond identity (REQ: reference-cli-self-
// hosting).
const repository = "strongo/selfupdate"

// binaryName matches .goreleaser.yml's archive/binary name, which is also
// this package's own default asset-naming convention
// ("selfupdate_<version>_<os>_<arch>.tar.gz").
const binaryName = "selfupdate"

// osExit is a seam over os.Exit so main can be exercised in-process by a
// test (which sets os.Args, stubs osExit to a recorder instead of a real
// process termination, calls main(), and restores both) without spawning a
// subprocess or needing GOCOVERDIR machinery.
var osExit = os.Exit

func main() {
	osExit(run())
}

// run executes the root command and returns the process exit code this
// module has always used: 0 on success, 1 if the command returned an error
// (also printed to stderr, exactly as the previous inline main() body did).
// It is a plain function, not inlined into main, purely so main's own body
// is a single trivial statement a test can drive via the osExit seam.
func run() int {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err) //nolint:errcheck
		return 1
	}
	return 0
}

// buildConfig returns this CLI's own selfupdate.Config. It is a plain
// function (not inlined into main) so main_test.go can assert its fields
// directly without constructing a command or touching any I/O.
func buildConfig() selfupdate.Config {
	return selfupdate.Config{
		BinaryName:           binaryName,
		Repository:           repository,
		CurrentVersion:       version,
		UndeterminedVersions: []string{undeterminedVersion},
		// These three managers are registered purely so --explain-path has
		// something realistic to classify against layouts this machine
		// doesn't have (REQ: reference-cli-inspection) — this binary is
		// published ONLY as plain GitHub release archives (see
		// .goreleaser.yml; no homebrew_casks, no Scoop bucket, no WinGet
		// manifest), so in normal operation none of these three ever
		// actually match. If one ever does match a real running install
		// (e.g. someone manually places the binary under a matching path),
		// the printed command is illustrative, not a real distribution
		// channel.
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew("brew upgrade selfupdate"),
			selfupdate.Scoop("scoop update selfupdate"),
			selfupdate.WinGet("winget upgrade strongo.selfupdate"),
		},
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		},
		// The root command's own --version flag (registered below via
		// cobra.Command.Version) is what the post-swap probe reads, so this
		// CLI's version-probe arguments match cobra's built-in flag rather
		// than a custom subcommand.
		VersionProbeArgs: []string{"--version"},
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
	}
}

// buildConfigFunc is a seam over buildConfig so a test can point newRootCmd
// at a Config with a local/fake ReleasesAPIURL and HTTPClient instead of
// this CLI's own real GitHub repository — the only way to exercise the
// subcommand's real (non-explain-path) RunE from this package's own tests
// without touching the network (REQ: no-network-in-tests). Only tests
// override this.
var buildConfigFunc = buildConfig

// newRootCmd builds the root command: a bare version-reporting root (via
// cobra's own --version flag) plus the self-update subcommand built
// entirely through the public API, plus --explain-path, a reference-CLI-
// only inspection flag layered on top without touching the subcommand's
// own RunE logic.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     binaryName,
		Short:   "selfupdate — reference CLI for github.com/strongo/selfupdate",
		Version: version,
		// main() prints the returned error itself; cobra's own default
		// error/usage printing would otherwise duplicate it.
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cfg := buildConfigFunc()
	update := cobracmd.New(cfg, cobracmd.CommandOptions{
		Aliases:     []string{"update"},
		Errors:      passthroughErrors{},
		JSONFormat:  true,
		Interactive: nil, // real TTY detection; only tests override this.
	})

	// --explain-path is reference-CLI-only tooling (REQ: reference-cli-
	// inspection): it classifies an arbitrary supplied path through the
	// exported selfupdate.Classify, so a manager's layout can be
	// demonstrated and verified on a machine that doesn't have it, without
	// touching this process's own install location at all. It is layered
	// on top of the command cobracmd.New already built, rather than
	// reimplementing any of cobracmd's flag/confirmation/output logic.
	update.Flags().String("explain-path", "", "classify an arbitrary path instead of updating; prints the install method and manager")
	wrapped := update.RunE
	update.RunE = func(cmd *cobra.Command, args []string) error {
		if p, _ := cmd.Flags().GetString("explain-path"); p != "" {
			return explainPath(cmd, cfg.Managers, p)
		}
		return wrapped(cmd, args)
	}

	root.AddCommand(update)
	return root
}

// explainPath classifies path against managers and prints the result. It
// performs no filesystem or network access — Classify is a pure function —
// so this mode genuinely "modifies nothing" regardless of what path names.
func explainPath(cmd *cobra.Command, managers []selfupdate.Manager, path string) error {
	detection := selfupdate.Classify(path, managers)
	out := cmd.OutOrStdout()

	switch detection.Method {
	case selfupdate.Managed:
		_, err := fmt.Fprintf(out, "%s: managed by %s (upgrade command: %s)\n", path, detection.Manager.Name, detection.Manager.UpgradeCommand)
		return err
	case selfupdate.Manual:
		_, err := fmt.Fprintf(out, "%s: manual install (eligible for self-replace)\n", path)
		return err
	default:
		_, err := fmt.Fprintf(out, "%s: ambiguous (not eligible for self-replace)\n", path)
		return err
	}
}

// passthroughErrors is the reference CLI's ErrorMapper: it has no exit-code
// contract of its own to enforce, so it returns every error unchanged and
// treats an available update on --check as non-fatal (exit 0, informational
// only). A real consumer with its own exit-code contract — see wb and
// specscore's Features — implements this interface to reclassify errors
// into its own type instead.
type passthroughErrors struct{}

func (passthroughErrors) Failure(err error) error { return err }

func (passthroughErrors) UpdateAvailable(selfupdate.CheckResult) error { return nil }
