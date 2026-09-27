package cobracmd

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cliui"
	"github.com/strongo/cli-helpers/selfupdate"
	selfcliui "github.com/strongo/cli-helpers/selfupdate/cliui"
)

// UninstallCommandOptions configures the command NewUninstall builds.
type UninstallCommandOptions struct {
	// Use is the command name, including any argument hint shown in help text. Defaults to "uninstall [name...]".
	Use string
	// Short is the one-line help text. Defaults to a generic description.
	Short string
	// Aliases are additional names the command responds to (e.g. "remove").
	Aliases []string
	// HostID is the running host's own catalog id.
	HostID string
	// Errors maps outcomes to the host's own error type.
	Errors ErrorMapper
	// Interactive reports whether the process is attached to an interactive terminal.
	Interactive func() bool
	// Env carries side-effecting dependencies.
	Env cliinstall.InstallEnv
	// ProbeOptions tunes status-probe concurrency and timeout.
	ProbeOptions cliinstall.ProbeOptions
}

// NewUninstall builds the "uninstall" Cobra command for opts.HostID.
func NewUninstall(opts UninstallCommandOptions) *cobra.Command {
	if _, ok := cliinstall.ByID(opts.HostID); !ok {
		panic("cliinstall/cobracmd: host id " + opts.HostID + " is not in the compiled catalog")
	}

	use := opts.Use
	if use == "" {
		use = "uninstall [name...]"
	}
	short := opts.Short
	if short == "" {
		short = "Uninstall installed fleet CLIs"
	}

	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Aliases: opts.Aliases,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()
			ctx := cmd.Context()

			flagAll, _ := cmd.Flags().GetBool("all")
			flagDryRun, _ := cmd.Flags().GetBool("dry-run")
			flagYes, _ := cmd.Flags().GetBool("yes")
			flagPurge, _ := cmd.Flags().GetBool("purge")
			flagFormat, _ := cmd.Flags().GetString("format")

			if flagAll && len(args) > 0 {
				err := &UsageError{Err: fmt.Errorf("cannot combine positional target arguments with --all")}
				return mapFailure(opts.Errors, err)
			}

			format := strings.ToLower(strings.TrimSpace(flagFormat))
			if format != "" && format != "table" && format != "json" {
				err := &UsageError{Err: fmt.Errorf("invalid --format %q; must be table or json", flagFormat)}
				return mapFailure(opts.Errors, err)
			}

			if len(args) == 0 && !flagAll {
				err := &UsageError{Err: fmt.Errorf("specify one or more CLI names to uninstall, or use --all")}
				return mapFailure(opts.Errors, err)
			}

			interactive := opts.Interactive
			if interactive == nil {
				interactive = selfcliui.IsTerminal
			}

			env := opts.Env
			if env.IsExecutable == nil {
				env = cliinstall.DefaultInstallEnv()
			}
			if env.RunManaged == nil {
				env.RunManaged = selfcliui.ManagedCommandRunner(cmd.InOrStdin(), out, errOut)
			}

			uninstallOpts := cliinstall.UninstallOptions{
				HostID:       opts.HostID,
				All:          flagAll,
				DryRun:       flagDryRun,
				Yes:          flagYes,
				Purge:        flagPurge,
				Env:          env,
				ProbeOptions: opts.ProbeOptions,
			}

			plan, err := cliinstall.PlanUninstall(ctx, args, uninstallOpts)
			if err != nil {
				return mapFailure(opts.Errors, err)
			}

			// Build UI rows
			rows := make([]cliui.UninstallRow, 0, len(plan.Results))
			for _, r := range plan.Results {
				e, _ := cliinstall.ByID(r.Target)
				rows = append(rows, cliui.UninstallRow{
					Entry:  e,
					Result: r,
				})
			}

			if flagDryRun {
				if format == "json" {
					_ = cliui.WriteUninstallJSON(out, opts.HostID, rows)
				} else {
					_ = cliui.WriteUninstallReport(out, errOut, opts.HostID, rows, true)
				}
				return nil
			}

			// Confirmation gate if not --yes
			if !flagYes {
				// Count how many are actually pending uninstall
				pending := 0
				for _, r := range plan.Results {
					if r.Outcome == cliinstall.UninstallOutcomeDryRun {
						pending++
					}
				}

				if pending > 0 {
					if !interactive() {
						refusal := &selfupdate.Failure{
							Kind: selfupdate.KindNonInteractive,
							Err:  fmt.Errorf("--yes is required for non-interactive use; refusing to uninstall"),
						}
						return mapFailure(opts.Errors, refusal)
					}

					prompt := fmt.Sprintf("Uninstall %d CLI(s)? [y/N] ", pending)
					_, _ = fmt.Fprint(out, prompt)
					reader := bufio.NewReader(cmd.InOrStdin())
					line, readErr := reader.ReadString('\n')
					ans := strings.ToLower(strings.TrimSpace(line))
					if readErr != nil || (ans != "y" && ans != "yes") {
						if format == "json" {
							for i := range rows {
								rows[i].Result.Outcome = cliinstall.UninstallOutcomeDeclined
							}
							_ = cliui.WriteUninstallJSON(out, opts.HostID, rows)
						} else {
							_, _ = fmt.Fprintln(out, "Uninstallation cancelled.")
						}
						return nil
					}
				}
			}

			executed, execErr := cliinstall.ExecuteUninstall(ctx, plan, uninstallOpts)
			if execErr != nil {
				return mapFailure(opts.Errors, execErr)
			}

			// Update rows with executed outcomes
			for i, r := range executed.Results {
				rows[i].Result = r
			}

			if format == "json" {
				_ = cliui.WriteUninstallJSON(out, opts.HostID, rows)
			} else {
				_ = cliui.WriteUninstallReport(out, errOut, opts.HostID, rows, false)
			}

			if executed.Failed() {
				return mapFailure(opts.Errors, executed.Failure())
			}

			return nil
		},
	}

	cmd.Flags().Bool("all", false, "uninstall all installed catalog CLIs")
	cmd.Flags().Bool("dry-run", false, "report what would be uninstalled without deleting anything")
	cmd.Flags().BoolP("yes", "y", false, "skip interactive confirmation prompt")
	cmd.Flags().Bool("purge", false, "remove associated agent skills and configuration")
	cmd.Flags().String("format", "table", "output format: table or json")

	return cmd
}
