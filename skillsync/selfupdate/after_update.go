// Package selfupdate connects the reusable skills refresh runner to the
// optional typed callback exposed by cli-helpers/selfupdate.
package selfupdate

import (
	"context"

	helperselfupdate "github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/skillsync/reexec"
)

// AfterUpdate returns the callback to assign to selfupdate.Options.AfterUpdate
// or selfupdate/cobracmd.CommandOptions.AfterUpdate. The self-update core owns
// deciding whether an outcome is eligible and turns a returned error into its
// non-fatal AfterUpdateWarning; this bridge only starts the resolved new binary.
func AfterUpdate(runner reexec.Runner) helperselfupdate.AfterUpdateFunc {
	return func(ctx context.Context, update helperselfupdate.AfterUpdate) error {
		path := update.Executable.ResolvedPath
		if path == "" {
			path = update.Executable.Path
		}
		return runner.Run(ctx, path)
	}
}
