// Command selfupdateequivprobe is a second throwaway fixture binary, built
// and exec'd only by cliinstall/cobracmd's own TestSelfUpdateEqualsUpgradeSelf
// (task-22 third review S4: "real --yes through an executable manager ...
// with host hook count"). Like ../selfupdateequiv, "testdata" keeps it out
// of this module's own build graph.
//
// selfupdate.Config.UpdateAt's managed-executable path (Config.updateManaged)
// only calls the host's AfterUpdate hook once Options.VerifyManaged has
// found and probed a REAL executable named Config.BinaryName on PATH
// reporting the expected post-upgrade version
// (selfupdate/cliui.VerifyManagedBinary — see its own doc comment). A fake
// RunManaged step alone (e.g. running `go version`) never satisfies that:
// nothing it does places a "cover100" binary on PATH at all. This probe
// stands in for what a real package manager's own upgrade step would leave
// behind: placed on PATH under the fixture's exact BinaryName
// (cliinstall/cobracmd/selfupdate_equivalence_test.go's equivBinaryName()),
// it answers a --version probe (Config's default VersionProbeArgs) with
// PROBE_VERSION so VerifyManagedBinary's post-swap check succeeds for real,
// letting AfterUpdate actually run on both the self-update and upgrade
// <self> sides — proving the equivalence extends to the hook, not just the
// reported Action.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("cover100 version %s\n", os.Getenv("PROBE_VERSION"))
		return
	}
	// Any other invocation (this probe doubling as the manager's own
	// executable-upgrade step) is a harmless no-op that always succeeds.
}
