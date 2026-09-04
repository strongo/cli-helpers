// Package skillsync installs immutable, CLI-pinned Agent Skills bundles into
// supported harness directories. It owns no command framework or product
// policy: callers supply their identity, embedded bundle snapshots, targets,
// and any host-specific error mapping.
package skillsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"
)

var (
	ErrInvalidConfig     = errors.New("invalid skills sync configuration")
	ErrDigestMismatch    = errors.New("bundle digest mismatch")
	ErrStateCorrupt      = errors.New("skills sync state is corrupt")
	ErrNoNewerCompatible = errors.New("no newer compatible bundle")
)

// Identity identifies the CLI that supplied a bundle. It is recorded as
// provenance only; plugin ownership always uses PluginIdentity.
type Identity struct{ Publisher, Name string }

func (i Identity) String() string {
	return i.Publisher + "/" + i.Name
}

// PluginIdentity is globally stable and deliberately separate from a skill
// directory name, which avoids flat-directory collisions between products.
type PluginIdentity struct{ Publisher, Name string }

func (p PluginIdentity) String() string {
	return p.Publisher + "/" + p.Name
}

// Compatibility limits the CLI versions a bundle can be installed by. Empty
// bounds are open; values use numeric dot-separated versions, optionally with
// a leading "v". Pre-release selection is intentionally host policy.
type Compatibility struct{ MinCLI, MaxCLI string }

// Source is reproducible bundle provenance. Repository, Path, Revision, and
// Digest identify exact bytes; Version names the plugin release for people
// and plugin hosts. It is persisted with ownership state for diagnostics.
type Source struct {
	Repository    string        `json:"repository"`
	Path          string        `json:"path"`
	Revision      string        `json:"revision"`
	Version       string        `json:"version"`
	Digest        string        `json:"digest"`
	Compatibility Compatibility `json:"compatibility,omitempty"`
}

// Bundle is an immutable source snapshot. Source is its sole provenance and
// version authority. ExecutablePaths preserves the executable bits that
// embed.FS cannot represent.
type Bundle struct {
	Plugin          PluginIdentity
	Source          Source
	FS              fs.FS
	ExecutablePaths []string
}

// BundleDescriptor is serializable build metadata. EmbeddedBundle is the
// common loader for go:embed snapshots, so hosts need not duplicate fs.Sub,
// source-provenance validation, or digest checks.
type BundleDescriptor struct {
	Plugin          PluginIdentity
	Source          Source
	ExecutablePaths []string
}

// EmbeddedBundle binds one canonical embedded tree to its immutable metadata.
func EmbeddedBundle(d BundleDescriptor, content fs.FS) (Bundle, error) {
	b := Bundle{Plugin: d.Plugin, Source: d.Source, FS: content, ExecutablePaths: append([]string(nil), d.ExecutablePaths...)}
	if _, err := validateBundle(b, ""); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// Config declares the installed CLI and its offline matched snapshots. A
// development build may use an undetermined CurrentVersion only when its
// embedded matched bundles declare no compatibility bounds; it never selects a
// newer release by default.
type Config struct {
	CLI            Identity
	CurrentVersion string
	Bundles        []Bundle
}

// Options supplies a target and controls whether Sync changes it. Resolver is
// reserved for explicit newer-compatible bundle selection; normal sync never
// calls it and therefore never requires network access.
type Options struct {
	Dir                   string
	DryRun                bool
	PreferNewerCompatible bool
	Resolver              Resolver
	LockTimeout           time.Duration
	Legacy                LegacyImport
}

// LegacyImport enables only a host's explicit one-time marker migration.
// MarkerFile is relative to the target directory and Plugin is the identity
// that will own matching, verified legacy skills.
type LegacyImport struct {
	MarkerFile string
	Plugin     PluginIdentity
}

// Resolver resolves an explicitly requested newer compatible bundle. It must
// return a complete, digest-pinned Bundle; Sync verifies it before planning.
type Resolver interface {
	Resolve(context.Context, Bundle) (Bundle, error)
}

// ReleaseSource retrieves one newer source snapshot chosen against a CLI
// version. Implementations may read a signed release archive or a local
// cache; the shared resolver still validates its immutable descriptor and
// content digest before it reaches Sync.
type ReleaseSource interface {
	NewerCompatible(context.Context, Source, string) (BundleDescriptor, fs.FS, error)
}

// ReleaseResolver is the standard explicit newer-compatible adapter. Put it
// in Options.Resolver at CLI wiring time; ordinary sync does not invoke it.
type ReleaseResolver struct {
	Source         ReleaseSource
	CurrentVersion string
}

func (r ReleaseResolver) Resolve(ctx context.Context, matched Bundle) (Bundle, error) {
	if r.Source == nil {
		return Bundle{}, fmt.Errorf("%w: release source is required", ErrInvalidConfig)
	}
	version := r.CurrentVersion
	if version == "" {
		return Bundle{}, fmt.Errorf("%w: current CLI version is required", ErrInvalidConfig)
	}
	descriptor, content, err := r.Source.NewerCompatible(ctx, matched.Source, version)
	if errors.Is(err, ErrNoNewerCompatible) {
		return matched, nil
	}
	if err != nil {
		return Bundle{}, err
	}
	if descriptor.Plugin != matched.Plugin || descriptor.Source.Repository != matched.Source.Repository || descriptor.Source.Path != matched.Source.Path {
		return Bundle{}, fmt.Errorf("%w: newer release changed canonical plugin source", ErrInvalidConfig)
	}
	if !validVersion(descriptor.Source.Version) || !validVersion(matched.Source.Version) || versionCompare(descriptor.Source.Version, matched.Source.Version) <= 0 {
		return Bundle{}, fmt.Errorf("%w: selected release %q is not newer than %q", ErrInvalidConfig, descriptor.Source.Version, matched.Source.Version)
	}
	return EmbeddedBundle(descriptor, content)
}

type Action string

const (
	Added     Action = "added"
	Updated   Action = "updated"
	Unchanged Action = "unchanged"
	Removed   Action = "removed"
	Conflict  Action = "conflict"
)

type Change struct {
	Plugin  PluginIdentity `json:"plugin"`
	Name    string         `json:"name"`
	Action  Action         `json:"action"`
	Outcome Outcome        `json:"outcome,omitempty"`
	Reason  string         `json:"reason,omitempty"`
}

// Outcome tells callers whether a planned mutation reached durable ownership.
// Conflict and unchanged entries deliberately have no mutation outcome.
type Outcome string

const (
	Planned    Outcome = "planned"
	Applied    Outcome = "applied"
	Restored   Outcome = "restored"
	Incomplete Outcome = "incomplete"
)

// Skill is one valid skill directory in a bundle.
type Skill struct{ Name, Digest string }
type Report struct {
	Dir     string           `json:"dir"`
	CLI     Identity         `json:"cli"`
	DryRun  bool             `json:"dry_run"`
	Bundles []ResolvedBundle `json:"bundles"`
	Changes []Change         `json:"changes"`
}
type ResolvedBundle struct {
	Plugin PluginIdentity `json:"plugin"`
	Source Source         `json:"source"`
}

// Status is the marker-only query hosts use for drift banners; it never walks
// installed skill trees or contacts a release source.
type Status struct {
	Installed bool
	Plugins   map[string]Source
}

func (r Report) Names(a Action) []string {
	var names []string
	for _, c := range r.Changes {
		if c.Action == a {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names
}
func (r Report) Changed() bool {
	for _, c := range r.Changes {
		if c.Action == Added || c.Action == Updated || c.Action == Removed {
			return true
		}
	}
	return false
}
