// Package cobracmd exposes the optional Cobra wiring for skillsync. The core
// package deliberately contains no command-framework imports.
package cobracmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/cliui"
)

// ErrorMapper lets each host retain its existing usage/failure/findings exit
// conventions. The adapter itself never chooses an exit code.
type ErrorMapper interface {
	Failure(error) error
	Conflict(skillsync.Report) error
}

// Harness describes a supported Agent Skills discovery location.
type Harness struct {
	ID        string
	Aliases   []string
	ConfigRel string
	ConfigEnv string
}

func (h Harness) SkillsDir(home string, getenv func(string) string) string {
	root := filepath.Join(home, h.ConfigRel)
	if h.ConfigEnv != "" {
		if v := strings.TrimSpace(getenv(h.ConfigEnv)); v != "" {
			root = v
		}
	}
	return filepath.Join(root, "skills")
}
func (h Harness) Present(home string, getenv func(string) string) bool {
	info, err := os.Stat(filepath.Dir(h.SkillsDir(home, getenv)))
	return err == nil && info.IsDir()
}

// DefaultHarnesses preserves WB's Claude, Cursor, and Codex target semantics.
var DefaultHarnesses = []Harness{{ID: "claude", Aliases: []string{"claude-code"}, ConfigRel: ".claude", ConfigEnv: "CLAUDE_CONFIG_DIR"}, {ID: "cursor", ConfigRel: ".cursor"}, {ID: "codex", ConfigRel: ".codex", ConfigEnv: "CODEX_HOME"}}

type CommandOptions struct {
	Use, Short string
	Harnesses  []Harness
	Home       func() (string, error)
	Getenv     func(string) string
	Errors     ErrorMapper
	Resolver   skillsync.Resolver
}

// New builds a `skills` parent with the reusable `sync` child.
func New(cfg skillsync.Config, opts CommandOptions) *cobra.Command {
	use := opts.Use
	if use == "" {
		use = "skills"
	}
	short := opts.Short
	if short == "" {
		short = "Install CLI-matched Agent Skills"
	}
	harnesses := opts.Harnesses
	if len(harnesses) == 0 {
		harnesses = DefaultHarnesses
	}
	home := opts.Home
	if home == nil {
		home = os.UserHomeDir
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	root := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs}
	var dir, format string
	var harness []string
	var dryRun, newer bool
	sync := &cobra.Command{Use: "sync", Short: "Install or update CLI-matched Agent Skills", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if format != "text" && format != "json" {
			return mapFailure(opts, fmt.Errorf("invalid --format %q: want text or json", format))
		}
		targets, err := resolveTargets(dir, harness, harnesses, home, getenv)
		if err != nil {
			return mapFailure(opts, err)
		}
		reports := make([]skillsync.Report, 0, len(targets))
		for _, target := range targets {
			report, err := skillsync.Sync(cmd.Context(), cfg, skillsync.Options{Dir: target.Dir, DryRun: dryRun, PreferNewerCompatible: newer, Resolver: opts.Resolver})
			if err != nil {
				return mapFailure(opts, err)
			}
			reports = append(reports, report)
		}
		var conflict error
		if opts.Errors != nil {
			for _, report := range reports {
				if len(report.Names(skillsync.Conflict)) > 0 {
					conflict = opts.Errors.Conflict(report)
					break
				}
			}
		}
		if format == "json" {
			if err := cliui.WriteJSON(cmd.OutOrStdout(), reports); err != nil {
				return err
			}
			return conflict
		}
		for _, report := range reports {
			if err := cliui.WriteText(cmd.OutOrStdout(), report); err != nil {
				return err
			}
		}
		return conflict
	}}
	sync.Flags().StringVar(&dir, "dir", "", "explicit harness skills directory (mutually exclusive with --harness)")
	sync.Flags().StringArrayVar(&harness, "harness", nil, "harness: claude, cursor, codex, or all (repeatable)")
	sync.Flags().BoolVar(&dryRun, "dry-run", false, "report changes without writing")
	sync.Flags().BoolVar(&newer, "newer-compatible", false, "explicitly select a newer compatible plugin release")
	sync.Flags().StringVar(&format, "format", "text", "output format: text|json")
	root.AddCommand(sync)
	return root
}

type target struct{ Harness, Dir string }

func resolveTargets(dir string, names []string, harnesses []Harness, home func() (string, error), getenv func(string) string) ([]target, error) {
	if strings.TrimSpace(dir) != "" && len(names) > 0 {
		return nil, fmt.Errorf("--dir and --harness cannot be used together")
	}
	if strings.TrimSpace(dir) != "" {
		return []target{{Dir: dir}}, nil
	}
	h, err := home()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	byName := map[string]Harness{}
	for _, x := range harnesses {
		byName[x.ID] = x
		for _, a := range x.Aliases {
			byName[a] = x
		}
	}
	selected := map[string]bool{}
	add := func(x Harness) { selected[x.ID] = true }
	if len(names) > 0 {
		for _, raw := range names {
			for _, name := range strings.Split(raw, ",") {
				name = strings.ToLower(strings.TrimSpace(name))
				if name == "" {
					continue
				}
				if name == "all" {
					for _, x := range harnesses {
						add(x)
					}
					continue
				}
				x, ok := byName[name]
				if !ok {
					return nil, fmt.Errorf("unknown skills harness %q", name)
				}
				add(x)
			}
		}
	} else {
		for _, x := range harnesses {
			if x.Present(h, getenv) {
				add(x)
			}
		}
		if len(selected) == 0 && len(harnesses) > 0 {
			add(harnesses[0])
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no skills harnesses configured")
	}
	var result []target
	seen := map[string]bool{}
	for _, x := range harnesses {
		if !selected[x.ID] {
			continue
		}
		d := filepath.Clean(x.SkillsDir(h, getenv))
		if seen[d] {
			continue
		}
		seen[d] = true
		result = append(result, target{Harness: x.ID, Dir: d})
	}
	return result, nil
}
func mapFailure(opts CommandOptions, err error) error {
	if opts.Errors != nil {
		return opts.Errors.Failure(err)
	}
	return err
}
