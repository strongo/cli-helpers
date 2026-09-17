package cobracmd

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cliui"
	"github.com/strongo/cli-helpers/selfupdate"
	selfcliui "github.com/strongo/cli-helpers/selfupdate/cliui"
)

// UpgradeCommandOptions configures the command NewUpgrade builds. Use and
// Short default to "upgrade [name...]" and a generic short description when
// left empty. There is deliberately no Aliases-based "update" alias field
// beyond the generic Aliases slice: REQ: update-alias-policy forbids adding
// one to upgrade at all, so a host that passes []string{"update"} here is
// making its own policy violation, not this package's.
type UpgradeCommandOptions struct {
	// Use is the command name, including any argument hint shown in help
	// text. Defaults to "upgrade [name...]".
	Use string
	// Short is the one-line help text. Defaults to a generic description.
	Short string
	// Aliases are additional names the command responds to. REQ: update-
	// alias-policy: never pass "update" here.
	Aliases []string
	// HostID is the running host's own catalog id
	// (cli-install#req:host-identity-from-catalog). NewUpgrade panics if it
	// is not a valid catalog id, matching install's own New.
	HostID string
	// Errors maps outcomes to the host's own error type. Implement
	// UpgradeErrorMapper (not just ErrorMapper) to receive the upgrades-
	// available signal cli-install#req:upgrade-check requires; a plain
	// ErrorMapper, or a nil Errors, simply never gets that call.
	Errors ErrorMapper
	// Interactive reports whether the process is attached to an interactive
	// terminal, used to implement REQ: non-interactive-refusal for the
	// batch confirmation prompt. Passed straight through to
	// cliui.ConfirmOptions.Interactive; nil means that package's own
	// default. Tests should always override this.
	Interactive func() bool
	// ConfigureRelease optionally overrides a non-host target's resolved
	// selfupdate.Config before it is used to look up or apply that target's
	// upgrade — the release-endpoint injection point cli-install#req:no-
	// network-in-tests requires. Nil keeps the catalog's own defaults.
	ConfigureRelease func(target cliinstall.Entry, cfg selfupdate.Config) selfupdate.Config
	// Env carries every side-effecting dependency status probing and
	// upgrading use. Left unset (a zero cliinstall.InstallEnv), NewUpgrade
	// uses cliinstall.DefaultInstallEnv() — the real host. Tests always set
	// this.
	Env cliinstall.InstallEnv
	// ProbeOptions tunes status-probe concurrency and per-target time
	// budget; the zero value is production-correct.
	ProbeOptions cliinstall.ProbeOptions

	// HostConfig is the host's OWN self-update selfupdate.Config — built the
	// identical way its `self-update` command builds one, including any
	// manager overrides or extra Managers it adds beyond its catalog entry
	// (cli-install#req:host-target-is-running-binary). Required whenever the
	// host is a candidate target: named explicitly, included under --all, or
	// part of the bare report's own --all set (i.e. essentially always — a
	// host that never wants itself offered has no way to opt out short of
	// naming every OTHER target explicitly and never using --all or the bare
	// report).
	HostConfig selfupdate.Config
	// HostAfterUpdate is the host's own after-update hook — the SAME closure
	// its `self-update` command configures, so `upgrade <self>` runs the
	// identical hook `self-update` does
	// (cli-install#req:self-update-equals-upgrade-self).
	HostAfterUpdate selfupdate.AfterUpdateFunc
	// DetectHost overrides how the host's own install is classified,
	// passed straight through to cliinstall.UpgradeOptions.DetectHost. Nil
	// (the production default) uses opts.HostConfig.DetectSelf, exactly
	// what the host's own `self-update` command calls (cli-install#req:
	// host-target-is-running-binary; task-22 review S1). Tests inject a
	// fake here so they never depend on the real running test binary's own
	// path.
	DetectHost func() (selfupdate.Detection, error)
	// VerifyManaged probes an executable managed target after its manager
	// command completes, passed straight through to cliinstall.
	// UpgradeOptions.VerifyManaged. Nil defaults to
	// selfcliui.VerifyManagedBinary — the SAME verifier the host's own
	// `self-update` command uses, which filters PATH candidates by the
	// detected manager's own markers (task-22 review S2: a bespoke,
	// manager-blind probe could hand AfterUpdate the wrong executable's
	// identity).
	VerifyManaged selfupdate.ManagedBinaryVerifier

	// LookupConcurrency and LookupTimeout tune PlanUpgrade's own release-
	// lookup bounds (cli-install#req:upgrade-release-lookups-bounded); zero
	// keeps cliinstall.UpgradeOptions' own defaults (4, 15s).
	LookupConcurrency int
	LookupTimeout     time.Duration
}

// NewUpgrade builds the "upgrade" command for opts.HostID. It registers
// --all, --check, --yes/-y, --dry-run and --format text|json — no --dir
// (upgrade always acts on the copy status-probing already located, never a
// caller-chosen destination) and no aliases beyond opts.Aliases (REQ:
// update-alias-policy: no "update" alias here, ever).
//
// With no target names and without --all, RunE runs REQ: upgrade-no-args-
// reports' own bare report: a full plan over the --all target set, printed
// and exited successfully whenever every lookup succeeded — whether or not
// an upgrade is available — closing with the next-step line. That report
// NEVER calls the error mapper's upgrades-available method; only an
// explicit --check does (cli-install#req:upgrade-check), over whatever
// target set names/--all select. With one or more names, or --all, and
// neither --check nor --dry-run, RunE plans, shows the SAME preview
// (details-before-install) once, confirms, and executes — exactly install's
// own Plan/confirm/Execute shape, reused here for UpgradeOptions.
func NewUpgrade(opts UpgradeCommandOptions) *cobra.Command {
	if _, ok := cliinstall.ByID(opts.HostID); !ok {
		panic("cliinstall/cobracmd: host id " + opts.HostID + " is not in the compiled catalog")
	}

	use := opts.Use
	if use == "" {
		use = "upgrade [name...]"
	}
	short := opts.Short
	if short == "" {
		short = "Upgrade installed fleet CLIs, including this one"
	}

	cmd := &cobra.Command{
		Use:     use,
		Aliases: opts.Aliases,
		Short:   short,
		Args:    cobra.ArbitraryArgs,
		// See install's own New for why usage printing is handled by hand
		// here instead of left to Cobra's default: a runtime failure (a
		// failed lookup, a refusal, an unknown target) is not a flag-
		// parsing mistake.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, _ := cmd.Flags().GetString("format")
			if format != "text" && format != "json" {
				return usageFailure(cmd, opts.Errors, fmt.Errorf("invalid --format %q: expected text or json", format))
			}
			all, _ := cmd.Flags().GetBool("all")
			if all && len(args) > 0 {
				return usageFailure(cmd, opts.Errors, fmt.Errorf("--all takes no target names"))
			}
			check, _ := cmd.Flags().GetBool("check")
			yes, _ := cmd.Flags().GetBool("yes")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			// REQ: upgrade-no-args-reports: "no names and no --all" is the
			// bare report, unconditionally — it takes priority over --check
			// and --dry-run (both are no-ops on top of it: the report is
			// already fully read-only) and, unlike --check, never signals
			// upgrades-available.
			if len(args) == 0 && !all {
				return runUpgradeReport(cmd, opts, nil, false, format)
			}
			if check {
				return runUpgradeReport(cmd, opts, args, all, format)
			}
			return runUpgrade(cmd, opts, args, all, yes, dryRun, format)
		},
	}

	cmd.Flags().Bool("all", false, "target every installed catalog CLI, plus this one, not just relevant ones")
	cmd.Flags().Bool("check", false, "report upgrade availability without applying it")
	cmd.Flags().BoolP("yes", "y", false, "skip the interactive confirmation prompt")
	cmd.Flags().Bool("dry-run", false, "report what would happen without replacing or running a manager command")
	cmd.Flags().String("format", "text", "output format: text|json")
	return cmd
}

// upgradeOptionsFrom builds the cliinstall.UpgradeOptions common to every
// RunE branch, wiring env's RunManaged the same way install's runInstall
// wires it for its own Options.Env, and the host's Config/AfterUpdate
// straight from opts (cli-install#req:host-target-is-running-binary: "The
// host MUST use its own self-update Config and options").
func upgradeOptionsFrom(cmd *cobra.Command, opts UpgradeCommandOptions, all bool, previewOut, errOut io.Writer) cliinstall.UpgradeOptions {
	env := resolveEnv(opts.Env)
	env.RunManaged = selfcliui.ManagedCommandRunner(cmd.InOrStdin(), previewOut, errOut)
	verifyManaged := opts.VerifyManaged
	if verifyManaged == nil {
		verifyManaged = selfcliui.VerifyManagedBinary
	}
	return cliinstall.UpgradeOptions{
		HostID:            opts.HostID,
		All:               all,
		Env:               env,
		ConfigureRelease:  opts.ConfigureRelease,
		ProbeOptions:      opts.ProbeOptions,
		HostConfig:        opts.HostConfig,
		HostAfterUpdate:   opts.HostAfterUpdate,
		DetectHost:        opts.DetectHost,
		VerifyManaged:     verifyManaged,
		LookupConcurrency: opts.LookupConcurrency,
		LookupTimeout:     opts.LookupTimeout,
	}
}

// upgradeRows builds one cliui.UpgradeRow per BatchResult entry, adding each
// target's catalog entry and relevance so cliui's writers can render
// description and relevance alongside the plan or outcome — mirroring
// install's own resultRows exactly, over cliinstall.UpgradeResult instead of
// cliinstall.Result. The host row's own Relevant/Relevance are always
// false/empty: a target is never relevant to itself, and relevanceIndex
// never contains hostID as a key (cli-install#req:relevance-matrix is
// defined only between DISTINCT targets).
func upgradeRows(hostID string, results []cliinstall.UpgradeResult) []cliui.UpgradeRow {
	idx := relevanceIndex(hostID)
	rows := make([]cliui.UpgradeRow, len(results))
	for i, r := range results {
		e, _ := cliinstall.ByID(r.Target)
		text, relevant := idx[r.Target]
		rows[i] = cliui.UpgradeRow{Entry: e, Relevant: relevant, Relevance: text, Result: r}
	}
	return rows
}

// runUpgradeReport implements the read-only report: the bare, no-argument
// invocation (names nil, all false — REQ: upgrade-no-args-reports) and an
// explicit --check over named targets or --all (REQ: upgrade-check). Both
// call cliinstall.CheckUpgrades — self-update's own read-only Check call,
// never selfupdate.Config.UpdateAt — so neither ever downloads, writes,
// confirms, runs a manager command, or invokes an AfterUpdate hook.
//
// bare distinguishes the two: only the bare report prints REQ: upgrade-no-
// args-reports' own next-step line and skips the upgrades-available signal.
// Per REQ: upgrade-no-args-reports ("MUST exit successfully... whether or
// not upgrades are available") the bare report also never fails merely
// because a target is refused as ambiguous — self-update's own --check
// never fails for that either (task-22 review B1) — only a genuine lookup
// failure (UpgradeOutcomeFailed) fails either shape; reportLookupFailed
// below is deliberately narrower than UpgradeBatchResult.Failed(), which
// also counts a refused/ambiguous row for the EXECUTE path's own exit code.
func runUpgradeReport(cmd *cobra.Command, opts UpgradeCommandOptions, names []string, all bool, format string) error {
	bare := names == nil && !all
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	upOpts := upgradeOptionsFrom(cmd, opts, all, out, errOut)
	plan, err := cliinstall.CheckUpgrades(cmd.Context(), names, upOpts)
	rows := upgradeRows(opts.HostID, plan.Results)

	if err != nil {
		if format == "json" {
			if werr := cliui.WriteUpgradeReportJSON(out, errOut, opts.HostID, rows, err); werr != nil {
				return mapFailure(opts.Errors, werr)
			}
			return mapFailure(opts.Errors, err)
		}
		cliui.WriteUpgradeReport(out, errOut, rows, err)
		return mapFailure(opts.Errors, err)
	}

	if format == "json" {
		if werr := cliui.WriteUpgradeReportJSON(out, errOut, opts.HostID, rows, nil); werr != nil {
			return mapFailure(opts.Errors, werr)
		}
	} else {
		cliui.WriteUpgradeReport(out, errOut, rows, nil)
		if bare {
			cliui.WriteUpgradeNextStep(out, opts.HostID)
		}
	}

	// REQ: upgrade-batch-semantics / upgrade-release-lookups-bounded: a
	// failed lookup fails the command regardless of report shape, taking
	// precedence over the upgrades-available signal below (REQ: upgrade-
	// check).
	if failure := reportLookupFailure(plan.Results); failure != nil {
		return mapFailure(opts.Errors, failure)
	}
	if bare {
		// REQ: upgrade-no-args-reports: "MUST exit successfully... whether
		// or not upgrades are available" — the bare report never consults
		// UpgradesAvailable at all.
		return nil
	}

	if um, ok := opts.Errors.(UpgradeErrorMapper); ok {
		var available []cliinstall.UpgradeResult
		for _, r := range plan.Results {
			if r.Latest == "" {
				continue
			}
			if r.Verdict == selfupdate.UpdateAvailable || r.Verdict == selfupdate.Undetermined {
				available = append(available, r)
			}
		}
		if len(available) > 0 {
			return um.UpgradesAvailable(available)
		}
	}
	return nil
}

// reportLookupFailure returns a *cliinstall.BatchFailure over every row
// whose Outcome is UpgradeOutcomeFailed — a genuine release-lookup failure
// — deliberately excluding UpgradeOutcomeRefused (ambiguous) rows, which
// UpgradeBatchResult.Failed()/Failure() count for the EXECUTE path's exit
// code but which self-update's own --check never fails for
// (task-22 review B1). Returns nil when nothing genuinely failed.
func reportLookupFailure(results []cliinstall.UpgradeResult) error {
	var failures []*selfupdate.Failure
	for _, r := range results {
		if r.Outcome == cliinstall.UpgradeOutcomeFailed && r.Failure != nil {
			failures = append(failures, r.Failure)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &cliinstall.BatchFailure{Failures: failures}
}

// runUpgrade implements `upgrade <name>...`/`upgrade --all` without --check:
// Plan, confirm, Execute — install's own runInstall shape, reused here for
// UpgradeOptions. The preview (details-before-install) is the SAME terse
// cliui.WriteUpgradeReport view the bare report and --check use; it is
// shown once, to stdout in text format and to stderr in --format json,
// before any confirmation, exactly mirroring runInstall's own convention.
func runUpgrade(cmd *cobra.Command, opts UpgradeCommandOptions, names []string, all, yes, dryRun bool, format string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	previewOut := out
	if format == "json" {
		previewOut = errOut
	}

	upOpts := upgradeOptionsFrom(cmd, opts, all, previewOut, errOut)
	plan, planErr := cliinstall.PlanUpgrade(cmd.Context(), names, upOpts)
	rows := upgradeRows(opts.HostID, plan.Results)

	if format == "text" {
		cliui.WriteUpgradeReport(out, errOut, rows, planErr)
	}

	if planErr != nil {
		if format == "json" {
			if werr := cliui.WriteUpgradeReportJSON(out, errOut, opts.HostID, rows, planErr); werr != nil {
				return mapFailure(opts.Errors, werr)
			}
		}
		return mapFailure(opts.Errors, planErr)
	}

	if dryRun {
		if format == "json" {
			if werr := cliui.WriteUpgradeReportJSON(out, errOut, opts.HostID, rows, nil); werr != nil {
				return mapFailure(opts.Errors, werr)
			}
		}
		return mapFailure(opts.Errors, plan.Failure())
	}

	if format == "json" {
		cliui.WriteUpgradeReport(previewOut, errOut, rows, nil)
	}

	upOpts.Yes = yes
	upOpts.Confirm = cliui.UpgradeConfirm(cliui.ConfirmOptions{
		In:          cmd.InOrStdin(),
		Out:         previewOut,
		Interactive: opts.Interactive,
	})

	result, execErr := cliinstall.ExecuteUpgrade(cmd.Context(), plan, upOpts)
	finalRows := upgradeRows(opts.HostID, result.Results)
	if format == "json" {
		if werr := cliui.WriteUpgradeReportJSON(out, errOut, opts.HostID, finalRows, execErr); werr != nil {
			return mapFailure(opts.Errors, werr)
		}
	} else {
		cliui.WriteUpgradeReport(out, errOut, finalRows, execErr)
	}
	if execErr != nil {
		return mapFailure(opts.Errors, execErr)
	}
	return mapFailure(opts.Errors, result.Failure())
}
