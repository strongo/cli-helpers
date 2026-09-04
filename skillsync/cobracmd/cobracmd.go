// Package cobracmd exposes optional Cobra wiring for skillsync. It owns
// command input, target selection, aggregation, and host error mapping; the
// core package remains command-framework free.
package cobracmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/cliui"
)

// UsageError identifies invalid Cobra input before source preparation or any
// target write. It preserves its cause for hosts using errors.Is/As.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// ErrorMapper lets a host retain its own exit-code and error convention.
type ErrorMapper interface {
	Failure(error) error
	Conflict(skillsync.Report) error
}

// Harness describes one Agent Skills configuration root.
type Harness struct {
	ID        string
	Aliases   []string
	ConfigRel string
	ConfigEnv string
}

func (h Harness) SkillsDir(home string, getenv func(string) string) string {
	root := filepath.Join(home, h.ConfigRel)
	if h.ConfigEnv != "" {
		if value := strings.TrimSpace(getenv(h.ConfigEnv)); value != "" {
			root = value
		}
	}
	return filepath.Join(root, "skills")
}

func (h Harness) Present(home string, getenv func(string) string) bool {
	info, err := os.Stat(filepath.Dir(h.SkillsDir(home, getenv)))
	return err == nil && info.IsDir()
}

// DefaultHarnesses preserves WB's Claude, Cursor, and Codex conventions.
var DefaultHarnesses = []Harness{
	{ID: "claude", Aliases: []string{"claude-code"}, ConfigRel: ".claude", ConfigEnv: "CLAUDE_CONFIG_DIR"},
	{ID: "cursor", ConfigRel: ".cursor"},
	{ID: "codex", ConfigRel: ".codex", ConfigEnv: "CODEX_HOME"},
}

// TargetResult retains both an attempted target's complete core report and its
// error. A host renderer can preserve a legacy JSON contract without
// reimplementing selection or synchronization.
type TargetResult struct {
	Harness string
	Dir     string
	Report  skillsync.Report
	Err     error
}

// Renderer receives all completed target outcomes after attempts finish.
type Renderer func(io.Writer, []TargetResult, string) error

type CommandOptions struct {
	Use, Short string
	Harnesses  []Harness
	Home       func() (string, error)
	Getenv     func(string) string
	Errors     ErrorMapper
	Resolver   skillsync.Resolver
	Renderer   Renderer
}

// New builds a convenience `skills` parent containing NewSync.
func New(cfg skillsync.Config, opts CommandOptions) *cobra.Command {
	use := opts.Use
	if use == "" {
		use = "skills"
	}
	short := opts.Short
	if short == "" {
		short = "Install CLI-matched Agent Skills"
	}
	root := &cobra.Command{Use: use, Short: short, Args: noArgs(opts)}
	root.AddCommand(NewSync(cfg, opts))
	return root
}

// NewSync builds only the reusable sync leaf, allowing a host such as WB to
// retain its existing `skills` parent and sibling hook command.
func NewSync(cfg skillsync.Config, opts CommandOptions) *cobra.Command {
	var dir, format string
	var harnesses []string
	var dryRun, newer bool
	cmd := &cobra.Command{
		Use: "sync", Short: "Install or update CLI-matched Agent Skills", Args: noArgs(opts), SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return mapFailure(opts, &UsageError{Err: fmt.Errorf("invalid --format %q: expected text or json", format)})
			}
			targets, err := resolveTargets(dir, harnesses, configuredHarnesses(opts), homeFunc(opts), getenvFunc(opts))
			if err != nil {
				return mapFailure(opts, err)
			}
			prepared, err := skillsync.Prepare(cmd.Context(), cfg, skillsync.Options{PreferNewerCompatible: newer, Resolver: opts.Resolver})
			if err != nil {
				return mapFailure(opts, err)
			}
			results, canceled := syncTargets(cmd.Context(), prepared, targets, dryRun)
			renderer := opts.Renderer
			if renderer == nil {
				renderer = renderDefault
			}
			if err := renderer(cmd.OutOrStdout(), results, format); err != nil {
				return mapFailure(opts, err)
			}
			if canceled != nil {
				return mapFailure(opts, canceled)
			}
			return resultsError(opts, results)
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return mapFailure(opts, &UsageError{Err: err}) })
	cmd.Flags().StringVar(&dir, "dir", "", "explicit harness skills directory (mutually exclusive with --harness)")
	cmd.Flags().StringArrayVar(&harnesses, "harness", nil, "harness: claude, cursor, codex, or all (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report changes without writing")
	cmd.Flags().BoolVar(&newer, "newer-compatible", false, "explicitly select a newer compatible plugin release")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	return cmd
}

func noArgs(opts CommandOptions) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return mapFailure(opts, &UsageError{Err: errors.New("accepts no positional arguments")})
		}
		return nil
	}
}

type target struct{ Harness, Dir string }

var (
	absolutePath = filepath.Abs
	syncPrepared = func(ctx context.Context, prepared skillsync.Prepared, opts skillsync.Options) (skillsync.Report, error) {
		return prepared.Sync(ctx, opts)
	}
)

func configuredHarnesses(opts CommandOptions) []Harness {
	if len(opts.Harnesses) != 0 {
		return opts.Harnesses
	}
	return DefaultHarnesses
}
func homeFunc(opts CommandOptions) func() (string, error) {
	if opts.Home != nil {
		return opts.Home
	}
	return os.UserHomeDir
}
func getenvFunc(opts CommandOptions) func(string) string {
	if opts.Getenv != nil {
		return opts.Getenv
	}
	return os.Getenv
}

// resolveTargets preserves explicit request order. Every requested alias is
// checked before equivalent physical directories are deduplicated.
func resolveTargets(dir string, names []string, harnesses []Harness, home func() (string, error), getenv func(string) string) ([]target, error) {
	if strings.TrimSpace(dir) != "" && len(names) > 0 {
		return nil, &UsageError{Err: errors.New("--dir and --harness cannot be used together")}
	}
	if strings.TrimSpace(dir) != "" {
		path, err := normalizedTarget(dir)
		if err != nil {
			return nil, &UsageError{Err: err}
		}
		return []target{{Dir: path}}, nil
	}
	homeDir, err := home()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	byName := map[string]Harness{}
	for _, harness := range harnesses {
		if harness.ID == "" {
			return nil, &UsageError{Err: errors.New("configured skills harness has an empty ID")}
		}
		byName[strings.ToLower(harness.ID)] = harness
		for _, alias := range harness.Aliases {
			byName[strings.ToLower(strings.TrimSpace(alias))] = harness
		}
	}
	requested := make([]Harness, 0, len(names))
	if len(names) == 0 {
		for _, harness := range harnesses {
			if harness.Present(homeDir, getenv) {
				requested = append(requested, harness)
			}
		}
		if len(requested) == 0 && len(harnesses) > 0 {
			requested = append(requested, harnesses[0])
		}
	} else {
		for _, raw := range names {
			for _, name := range strings.Split(raw, ",") {
				name = strings.ToLower(strings.TrimSpace(name))
				if name == "" {
					continue
				}
				if name == "all" {
					requested = append(requested, harnesses...)
					continue
				}
				harness, ok := byName[name]
				if !ok {
					return nil, &UsageError{Err: fmt.Errorf("unknown skills harness %q", name)}
				}
				requested = append(requested, harness)
			}
		}
	}
	if len(requested) == 0 {
		return nil, &UsageError{Err: errors.New("at least one --harness value is required")}
	}
	all := make([]target, 0, len(requested))
	for _, harness := range requested {
		path, err := normalizedTarget(harness.SkillsDir(homeDir, getenv))
		if err != nil {
			return nil, &UsageError{Err: fmt.Errorf("invalid %s harness target: %w", harness.ID, err)}
		}
		all = append(all, target{Harness: harness.ID, Dir: path})
	}
	unique := make([]target, 0, len(all))
	for _, candidate := range all {
		equivalent := false
		for _, prior := range unique {
			if sameTarget(prior.Dir, candidate.Dir) {
				equivalent = true
				break
			}
		}
		if !equivalent {
			unique = append(unique, candidate)
		}
	}
	return unique, nil
}

func normalizedTarget(dir string) (string, error) {
	path, err := absolutePath(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("skills directory %s is a symlink", path)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("skills directory %s is not a directory", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}
func sameTarget(a, b string) bool {
	if a == b {
		return true
	}
	ai, aErr := os.Stat(a)
	bi, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(ai, bi)
}

func syncTargets(ctx context.Context, prepared skillsync.Prepared, targets []target, dryRun bool) ([]TargetResult, error) {
	results := make([]TargetResult, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		report, err := syncPrepared(ctx, prepared, skillsync.Options{Dir: target.Dir, DryRun: dryRun})
		results = append(results, TargetResult{Harness: target.Harness, Dir: target.Dir, Report: report, Err: err})
		if err != nil && errors.Is(err, context.Canceled) {
			return results, err
		}
	}
	return results, nil
}

// AggregateError exposes every target error through errors.Is/As.
type AggregateError struct{ Results []TargetResult }

func (e *AggregateError) Error() string { return "one or more skills targets failed" }
func (e *AggregateError) Unwrap() []error {
	errs := make([]error, 0, len(e.Results))
	for _, result := range e.Results {
		if result.Err != nil {
			errs = append(errs, result.Err)
		}
	}
	return errs
}

// ConflictError is returned even with no ErrorMapper, so conflicts do not
// accidentally become a successful command.
type ConflictError struct{ Report skillsync.Report }

func (e *ConflictError) Error() string { return "skills sync found conflicts" }

func resultsError(opts CommandOptions, results []TargetResult) error {
	for _, result := range results {
		if result.Err != nil {
			return mapFailure(opts, &AggregateError{Results: results})
		}
	}
	for _, result := range results {
		if len(result.Report.Names(skillsync.Conflict)) == 0 {
			continue
		}
		if opts.Errors != nil {
			if err := opts.Errors.Conflict(result.Report); err != nil {
				return err
			}
		}
		return mapFailure(opts, &ConflictError{Report: result.Report})
	}
	return nil
}
func renderDefault(out io.Writer, results []TargetResult, format string) error {
	items := make([]cliui.TargetReport, 0, len(results))
	for _, result := range results {
		items = append(items, cliui.TargetReport{Harness: result.Harness, Dir: result.Dir, Report: result.Report, Err: result.Err})
	}
	if format == "json" {
		return cliui.WriteTargetJSON(out, items)
	}
	return cliui.WriteTargetText(out, items)
}
func mapFailure(opts CommandOptions, err error) error {
	if opts.Errors != nil {
		return opts.Errors.Failure(err)
	}
	return err
}
