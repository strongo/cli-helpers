package skillsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// Sync validates all bundles before touching a target, then applies a
// plugin-scoped plan under a target lock. It defaults to the embedded bundle
// snapshots supplied in Config and never contacts a Resolver unless the
// caller explicitly selected PreferNewerCompatible.
func Sync(ctx context.Context, cfg Config, opts Options) (Report, error) {
	prepared, err := Prepare(ctx, cfg, opts)
	if err != nil {
		return Report{Dir: opts.Dir, CLI: cfg.CLI, CLIVersion: cfg.CurrentVersion, DryRun: opts.DryRun}, err
	}
	return prepared.Sync(ctx, opts)
}

// Prepared is one validated, immutable source selection. A host that syncs
// multiple harnesses prepares it once, then uses Sync for every target so an
// explicit newer-compatible resolver cannot select different releases midway
// through a command.
type Prepared struct {
	cfg     Config
	bundles []resolvedBundle
}

// Prepare validates the host configuration and resolves any explicitly
// requested newer-compatible bundles once. It performs no target I/O.
func Prepare(ctx context.Context, cfg Config, opts Options) (Prepared, error) {
	if !validIdentity(cfg.CLI) || !validCurrentCLIVersion(cfg.CurrentVersion) || len(cfg.Bundles) == 0 {
		return Prepared{}, fmt.Errorf("%w: CLI, current version, and bundles are required", ErrInvalidConfig)
	}
	bundles, err := resolvedBundles(ctx, cfg, opts)
	if err != nil {
		return Prepared{}, err
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	return Prepared{cfg: cfg, bundles: bundles}, nil
}

// Sync applies this already-prepared source set to one target. Bundle content
// is revalidated before target classification and mutation, so preparation is
// a consistency boundary rather than a trust bypass.
func (p Prepared) Sync(ctx context.Context, opts Options) (Report, error) {
	report := Report{Dir: opts.Dir, CLI: p.cfg.CLI, CLIVersion: p.cfg.CurrentVersion, DryRun: opts.DryRun}
	if opts.Dir == "" || len(p.bundles) == 0 {
		return report, fmt.Errorf("%w: target directory and prepared bundles are required", ErrInvalidConfig)
	}
	bundles, err := revalidatePrepared(p.cfg, p.bundles)
	if err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	for _, bundle := range bundles {
		report.Bundles = append(report.Bundles, ResolvedBundle{Plugin: bundle.Bundle.Plugin, Source: bundle.Bundle.Source})
	}
	if opts.DryRun {
		if err := validateTargetAncestry(opts.Dir); err != nil {
			return report, err
		}
		if err := recoveryPending(opts.Dir); err != nil {
			return report, err
		}
		return syncLocked(ctx, p.cfg, bundles, opts, report)
	}
	// Reject an existing unsafe marker before lock() creates its parent-level
	// lock file. syncLocked reads it again after locking, so this preflight does
	// not weaken the transaction's race protection.
	if err := validateTargetAncestry(opts.Dir); err != nil {
		return report, err
	}
	if _, err := emptyOrExistingState(opts.Dir); err != nil {
		return report, err
	}
	unlock, err := lock(ctx, opts.Dir, opts.LockTimeout)
	if err != nil {
		return report, err
	}
	defer unlock()
	return syncLocked(ctx, p.cfg, bundles, opts, report)
}

type resolvedBundle struct {
	Bundle Bundle
	Skills []skill
}

type operation struct {
	source      fs.FS
	executables []string
	plugin      PluginIdentity
	name        string
	remove      bool
	old         string
	new         string
}

func resolvedBundles(ctx context.Context, cfg Config, opts Options) ([]resolvedBundle, error) {
	seen := map[string]bool{}
	result := make([]resolvedBundle, 0, len(cfg.Bundles))
	for _, b := range cfg.Bundles {
		if opts.PreferNewerCompatible {
			if opts.Resolver == nil {
				return nil, fmt.Errorf("%w: newer-compatible selection requires a resolver", ErrInvalidConfig)
			}
			plugin := b.Plugin
			var err error
			b, err = opts.Resolver.Resolve(ctx, b)
			if err != nil {
				return nil, fmt.Errorf("resolve newer %s: %w", plugin.String(), err)
			}
		}
		key := b.Plugin.String()
		if seen[key] {
			return nil, fmt.Errorf("%w: plugin %s is declared more than once", ErrInvalidConfig, key)
		}
		seen[key] = true
		skills, err := validateBundle(b, cfg.CurrentVersion)
		if err != nil {
			return nil, err
		}
		result = append(result, resolvedBundle{b, skills})
	}
	return result, nil
}

func revalidatePrepared(cfg Config, prepared []resolvedBundle) ([]resolvedBundle, error) {
	result := make([]resolvedBundle, 0, len(prepared))
	for _, item := range prepared {
		skills, err := validateBundle(item.Bundle, cfg.CurrentVersion)
		if err != nil {
			return nil, err
		}
		result = append(result, resolvedBundle{Bundle: item.Bundle, Skills: skills})
	}
	return result, nil
}

func syncLocked(ctx context.Context, cfg Config, bundles []resolvedBundle, opts Options, report Report) (Report, error) {
	if err := rejectSymlink(opts.Dir); err != nil {
		return report, err
	}
	if !opts.DryRun {
		if err := recoverTransaction(opts.Dir); err != nil {
			return report, err
		}
	}
	current, err := emptyOrExistingState(opts.Dir)
	if err != nil {
		return report, err
	}
	for i := range report.Bundles {
		if prior, ok := current.Plugins[report.Bundles[i].Plugin.String()]; ok {
			report.Bundles[i].PriorCLIVersion = prior.SupplierCLIVersions[cfg.CLI.String()]
		}
	}
	legacyImported := false
	if opts.Legacy.MarkerFile != "" && current.Plugins[opts.Legacy.Plugin.String()].Skills == nil {
		legacy, err := importLegacy(opts.Dir, opts.Legacy, cfg.CLI)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return report, err
		}
		if err == nil {
			for name := range legacy.Skills {
				if owner := ownersOf(current)[name]; owner != "" && owner != opts.Legacy.Plugin.String() {
					return report, fmt.Errorf("%w: legacy skill %s is already owned by %s", ErrStateCorrupt, name, owner)
				}
			}
			current.Plugins[opts.Legacy.Plugin.String()] = legacy
			for i := range report.Bundles {
				if report.Bundles[i].Plugin == opts.Legacy.Plugin {
					report.Bundles[i].PriorCLIVersion = legacy.SupplierCLIVersions[cfg.CLI.String()]
				}
			}
			legacyImported = true
		}
	}
	next := cloneState(current)
	owners := ownersOf(current)
	tx := newTransaction(opts.Dir)
	// Discover every requested name before looking at the target. This makes a
	// collision between two bundles deterministic in both dry-run and apply.
	desiredNames := map[string]int{}
	for _, rb := range bundles {
		for _, item := range rb.Skills {
			desiredNames[item.Name]++
		}
	}
	var operations []operation
	for _, rb := range bundles {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		key := rb.Bundle.Plugin.String()
		prior := current.Plugins[key]
		changeStart, operationStart := len(report.Changes), len(operations)
		bundleConflict := false
		conflictingSupplier := false
		for supplier := range prior.Suppliers {
			if supplier != cfg.CLI.String() && prior.Source != rb.Bundle.Source {
				conflictingSupplier = true
				break
			}
		}
		// A CLI may advance the plugin source it is the sole supplier of. A
		// second supplier protects the complete immutable source it recorded:
		// repository, path, revision, digest, version, and compatibility bounds.
		if conflictingSupplier {
			for _, item := range rb.Skills {
				report.Changes = append(report.Changes, Change{Plugin: rb.Bundle.Plugin, Name: item.Name, Action: Conflict, Reason: "plugin immutable source already owned by another CLI"})
			}
			continue
		}
		desired := map[string]string{}
		for _, item := range rb.Skills {
			desired[item.Name] = item.Digest
			action, reason, err := classify(opts.Dir, item, prior, owners, key)
			if desiredNames[item.Name] > 1 {
				action, reason = Conflict, "requested by multiple plugins"
			}
			if err != nil {
				return report, err
			}
			report.Changes = append(report.Changes, Change{Plugin: rb.Bundle.Plugin, Name: item.Name, Action: action, Reason: reason})
			if action == Conflict {
				bundleConflict = true
				continue
			}
			if action == Added || action == Updated {
				report.Changes[len(report.Changes)-1].Outcome = Planned
			}
			if action == Added || action == Updated {
				operations = append(operations, operation{source: rb.Bundle.FS, executables: rb.Bundle.ExecutablePaths, plugin: rb.Bundle.Plugin, name: item.Name, old: prior.Skills[item.Name], new: item.Digest})
			}
		}
		for name, oldDigest := range prior.Skills {
			if _, still := desired[name]; still {
				continue
			}
			action, reason, err := classifyRemoval(opts.Dir, name, oldDigest, owners, key)
			if err != nil {
				return report, err
			}
			report.Changes = append(report.Changes, Change{Plugin: rb.Bundle.Plugin, Name: name, Action: action, Reason: reason})
			if action == Removed {
				report.Changes[len(report.Changes)-1].Outcome = Planned
				operations = append(operations, operation{plugin: rb.Bundle.Plugin, name: name, remove: true, old: oldDigest})
			}
			if action == Conflict {
				bundleConflict = true
			}
		}
		// A plugin state describes one verified revision. Do not publish a
		// mixture of old and new ownership when any skill in that revision is
		// unresolved: retain the last complete revision and leave every target
		// mutation for this plugin for a later safe retry.
		if bundleConflict {
			for i := changeStart; i < len(report.Changes); i++ {
				if report.Changes[i].Action != Conflict {
					report.Changes[i].Action = Conflict
					report.Changes[i].Outcome = ""
					report.Changes[i].Reason = "plugin has unresolved conflicts"
				}
			}
			operations = operations[:operationStart]
			if prior.Revision != "" {
				next.Plugins[key] = prior
			} else {
				delete(next.Plugins, key)
			}
			continue
		}
		// State records only skills we have actually verified as owned. A
		// conflict stays out of it, so the later retry cannot claim it.
		owned := map[string]string{}
		for _, item := range rb.Skills {
			a := actionFor(report.Changes, key, item.Name)
			if a != Conflict {
				owned[item.Name] = item.Digest
			}
		}
		if len(owned) == 0 {
			delete(next.Plugins, key)
		} else {
			suppliers := map[string]string{}
			for cli, revision := range prior.Suppliers {
				suppliers[cli] = revision
			}
			suppliers[cfg.CLI.String()] = rb.Bundle.Source.Revision
			supplierVersions := map[string]string{}
			for cli, version := range prior.SupplierCLIVersions {
				supplierVersions[cli] = version
			}
			supplierVersions[cfg.CLI.String()] = cfg.CurrentVersion
			// CLI is the original primary supplier retained by old markers. A
			// registered supplier with identical immutable source must not flip
			// that field back and forth or rewrite an otherwise unchanged marker.
			primaryCLI := cfg.CLI.String()
			if prior.CLI != "" && prior.Source == rb.Bundle.Source {
				primaryCLI = prior.CLI
			}
			next.Plugins[key] = pluginState{Revision: rb.Bundle.Source.Revision, Digest: rb.Bundle.Source.Digest, CLI: primaryCLI, Suppliers: suppliers, SupplierCLIVersions: supplierVersions, Source: rb.Bundle.Source, Skills: owned, SyncedAt: time.Now().UTC()}
		}
	}
	sort.Slice(report.Changes, func(i, j int) bool {
		if report.Changes[i].Name == report.Changes[j].Name {
			return report.Changes[i].Plugin.String() < report.Changes[j].Plugin.String()
		}
		return report.Changes[i].Name < report.Changes[j].Name
	})
	if opts.DryRun {
		return report, nil
	}
	// The old WB marker is enough to verify content but is not sufficient for
	// crash recovery. Publish that verified ownership before the first rename,
	// so a fresh process can prove which original a legacy upgrade may restore.
	if legacyImported && len(operations) > 0 {
		if err := writeState(opts.Dir, current); err != nil {
			return report, fmt.Errorf("persist imported legacy ownership: %w", err)
		}
	}
	// Only after every bundle, collision, removal and state transition has
	// been classified do we make the first target mutation.
	for _, op := range operations {
		if err := ctx.Err(); err != nil {
			if rollbackErr := tx.rollback(); rollbackErr != nil {
				markOutcomes(&report, operations, tx, Restored, Incomplete)
				return report, fmt.Errorf("apply canceled: %w; rollback: %v", err, rollbackErr)
			}
			markOutcomes(&report, operations, tx, Restored, Incomplete)
			return report, err
		}
		var err error
		if op.remove {
			err = tx.remove(op.name, op.old)
		} else {
			err = tx.replace(op.source, op.executables, op.name, op.old, op.new)
		}
		if err != nil {
			if rollbackErr := tx.rollback(); rollbackErr != nil {
				markOutcomes(&report, operations, tx, Restored, Incomplete)
				return report, fmt.Errorf("apply %s: %w; rollback: %v", op.name, err, rollbackErr)
			}
			markOutcomes(&report, operations, tx, Restored, Incomplete)
			return report, fmt.Errorf("apply %s: %w", op.name, err)
		}
		setOutcome(&report, op, Applied)
	}
	if statesEqual(current, next) {
		if err := tx.commit(); err != nil {
			if errors.Is(err, ErrStateCorrupt) {
				markOutcomes(&report, operations, tx, Restored, Incomplete)
			}
			return report, fmt.Errorf("finalize skills transaction: %w", err)
		}
		return report, nil
	}
	if tx.id != "" {
		next.RecoveryID = strings.TrimPrefix(tx.id, transactionPrefix)
	}
	if err := writeState(opts.Dir, next); err != nil {
		var published statePublishedError
		if errors.As(err, &published) {
			// The marker now names this transaction. Preserve the matching target
			// and journal; a later Sync retries durable persistence before cleanup.
			markOutcomes(&report, operations, tx, Restored, Incomplete)
			return report, fmt.Errorf("persist skills ownership: %w", err)
		}
		if rollbackErr := tx.rollback(); rollbackErr != nil {
			markOutcomes(&report, operations, tx, Restored, Incomplete)
			return report, fmt.Errorf("persist skills ownership: %w; rollback: %v", err, rollbackErr)
		}
		markOutcomes(&report, operations, tx, Restored, Incomplete)
		return report, fmt.Errorf("persist skills ownership: %w", err)
	}
	transactionBoundary("state")
	if err := tx.commit(); err != nil {
		if errors.Is(err, ErrStateCorrupt) {
			markOutcomes(&report, operations, tx, Restored, Incomplete)
		}
		return report, fmt.Errorf("finalize skills transaction: %w", err)
	}
	return report, nil
}

func markOutcomes(report *Report, operations []operation, tx transaction, rolledBack, incomplete Outcome) {
	for _, op := range operations {
		if tx.restored(op.name) {
			setOutcome(report, op, rolledBack)
		} else {
			setOutcome(report, op, incomplete)
		}
	}
}

func setOutcome(report *Report, op operation, outcome Outcome) {
	for i := range report.Changes {
		change := &report.Changes[i]
		if change.Plugin == op.plugin && change.Name == op.name {
			change.Outcome = outcome
			return
		}
	}
}

func actionFor(changes []Change, plugin, name string) Action {
	for i := len(changes) - 1; i >= 0; i-- {
		if changes[i].Plugin.String() == plugin && changes[i].Name == name {
			return changes[i].Action
		}
	}
	return Conflict
}
func ownersOf(s state) map[string]string {
	owners := map[string]string{}
	for p, ps := range s.Plugins {
		for n := range ps.Skills {
			owners[n] = p
		}
	}
	return owners
}
func cloneState(s state) state {
	n := state{Schema: s.Schema, Plugins: map[string]pluginState{}}
	for k, v := range s.Plugins {
		skills := map[string]string{}
		for a, b := range v.Skills {
			skills[a] = b
		}
		v.Skills = skills
		suppliers := map[string]string{}
		for cli, revision := range v.Suppliers {
			suppliers[cli] = revision
		}
		v.Suppliers = suppliers
		supplierVersions := map[string]string{}
		for cli, version := range v.SupplierCLIVersions {
			supplierVersions[cli] = version
		}
		v.SupplierCLIVersions = supplierVersions
		n.Plugins[k] = v
	}
	return n
}
func statesEqual(a, b state) bool {
	if len(a.Plugins) != len(b.Plugins) {
		return false
	}
	for k, x := range a.Plugins {
		y, ok := b.Plugins[k]
		if !ok || x.Revision != y.Revision || x.Digest != y.Digest || x.CLI != y.CLI || x.Legacy != y.Legacy || x.Source != y.Source || len(x.Skills) != len(y.Skills) || len(x.Suppliers) != len(y.Suppliers) || len(x.SupplierCLIVersions) != len(y.SupplierCLIVersions) {
			return false
		}
		for n, h := range x.Skills {
			if y.Skills[n] != h {
				return false
			}
		}
		for cli, revision := range x.Suppliers {
			if y.Suppliers[cli] != revision {
				return false
			}
		}
		for cli, version := range x.SupplierCLIVersions {
			if y.SupplierCLIVersions[cli] != version {
				return false
			}
		}
	}
	return true
}

func classify(dir string, item skill, prior pluginState, owners map[string]string, plugin string) (Action, string, error) {
	path := filepath.Join(dir, item.Name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Added, "", nil
	}
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Conflict, "symlinked target", nil
	}
	if !info.IsDir() {
		return Conflict, "non-directory target", nil
	}
	if owner := owners[item.Name]; owner != "" && owner != plugin {
		return Conflict, "owned by " + owner, nil
	}
	previous, known := prior.Skills[item.Name]
	if !known {
		return Conflict, "unmanaged target", nil
	}
	digest, err := installedDigest(dir, item.Name)
	if err != nil {
		return Conflict, "unsafe target", nil
	}
	if digest != previous {
		return Conflict, "modified target", nil
	}
	if digest == item.Digest {
		return Unchanged, "", nil
	}
	return Updated, "", nil
}
func classifyRemoval(dir, name, old string, owners map[string]string, plugin string) (Action, string, error) {
	if owners[name] != plugin {
		return Conflict, "owned by another plugin", nil
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Removed, "", nil
	}
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Conflict, "symlinked target", nil
	}
	if !info.IsDir() {
		return Conflict, "non-directory target", nil
	}
	digest, err := installedDigest(dir, name)
	if err != nil {
		return Conflict, "unsafe target", nil
	}
	if digest != old {
		return Conflict, "modified target", nil
	}
	return Removed, "", nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse symlinked skills directory %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("skills directory %s is not a directory", path)
	}
	return nil
}
func installedDigest(dir, name string) (string, error) {
	hfs := os.DirFS(dir)
	return subtreeDigest(hfs, name)
}

type transaction struct {
	dir           string
	id            string
	changes       []txChange
	restoredNames map[string]bool
}
type txChange struct {
	Name, Old, New string
	Existed        bool
	Phase          string
}

const recoveryFileName = ".cli-helpers-skills-recovery.json"
const transactionPrefix = ".cli-helpers-skills-txn-"

// transactionOperationSet names the filesystem boundaries whose failures
// change recovery behavior. It is internal so tests can deterministically
// exercise those boundaries without adding options or runtime switches.
type transactionOperationSet struct {
	rename        func(*os.Root, string, string) error
	removeAll     func(*os.Root, string) error
	mkdirAll      func(string, fs.FileMode) error
	remove        func(string) error
	syncDirectory func(string) error
}

var transactionOperations = transactionOperationSet{
	rename:        func(root *os.Root, old, new string) error { return root.Rename(old, new) },
	removeAll:     func(root *os.Root, name string) error { return root.RemoveAll(name) },
	mkdirAll:      func(path string, mode fs.FileMode) error { return os.MkdirAll(path, mode) },
	remove:        os.Remove,
	syncDirectory: syncDirectory,
}

var transactionBoundary = func(string) {}

func rootRelative(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: path escapes transaction root", ErrStateCorrupt)
	}
	return rel, nil
}

func rootedRename(rootPath, old, new string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	oldRel, err := rootRelative(rootPath, old)
	if err != nil {
		return err
	}
	newRel, err := rootRelative(rootPath, new)
	if err != nil {
		return err
	}
	return transactionOperations.rename(root, oldRel, newRel)
}

func rootedRemoveAll(rootPath, path string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rel, err := rootRelative(rootPath, path)
	if err != nil {
		return err
	}
	return transactionOperations.removeAll(root, rel)
}

func syncTransactionDirectories(paths ...string) error {
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if err := transactionOperations.syncDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectoryChain(path, stop string) error {
	for {
		if err := syncTransactionDirectories(path); err != nil {
			return err
		}
		if path == stop {
			return nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return fmt.Errorf("%w: directory sync escaped transaction root", ErrStateCorrupt)
		}
		path = parent
	}
}

// ensureDirectoryAncestry creates each missing directory separately so callers
// can durably persist every newly-created entry without syncing beyond the
// first pre-existing ancestor.
func ensureDirectoryAncestry(path string, mkdir func(string, fs.FileMode) error) ([]string, error) {
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("directory ancestor %s is not a directory", current)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, err
		}
		missing = append(missing, current)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := mkdir(missing[i], 0o755); err != nil {
			return nil, err
		}
	}
	return missing, nil
}

func syncCreatedDirectoryAncestry(created []string, directorySync func(string) error) error {
	seen := map[string]bool{}
	for _, path := range created {
		for _, candidate := range []string{path, filepath.Dir(path)} {
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			if err := directorySync(candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

type recoveryChange struct {
	Name    string `json:"name"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
	Existed bool   `json:"existed"`
	Phase   string `json:"phase"`
}
type recoveryJournal struct {
	Schema      int              `json:"schema"`
	ID          string           `json:"id"`
	Transaction string           `json:"transaction"`
	Changes     []recoveryChange `json:"changes"`
}

func newTransaction(dir string) transaction {
	return transaction{dir: dir, restoredNames: map[string]bool{}}
}

func validSkillName(name string) bool {
	return name != "" && name != "." && !strings.HasPrefix(name, ".cli-helpers-skills-") && fs.ValidPath(name) && filepath.Base(name) == name
}

func validDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, r := range digest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (t *transaction) transactionDir() string { return filepath.Join(t.dir, t.id) }
func (t *transaction) backup(name string) string {
	return filepath.Join(t.transactionDir(), "backup", name)
}

func (t *transaction) start() error {
	if t.id != "" {
		return nil
	}
	created, err := ensureDirectoryAncestry(t.dir, transactionOperations.mkdirAll)
	if err != nil {
		return err
	}
	if err := syncCreatedDirectoryAncestry(created, transactionOperations.syncDirectory); err != nil {
		return err
	}
	dir, err := durableFileOperations.mkdirTemp(t.dir, transactionPrefix)
	if err != nil {
		return err
	}
	t.id = filepath.Base(dir)
	return syncTransactionDirectories(t.dir)
}

func (t *transaction) record(c txChange) error {
	if err := t.start(); err != nil {
		return err
	}
	if !validSkillName(c.Name) || (c.Old != "" && !validDigest(c.Old)) || (c.New != "" && !validDigest(c.New)) || (c.Existed && c.Old == "") || (!c.Existed && c.Old != "") {
		return fmt.Errorf("%w: invalid transaction change", ErrStateCorrupt)
	}
	c.Phase = "prepared"
	t.changes = append(t.changes, c)
	if err := t.writeJournal(); err != nil {
		t.changes = t.changes[:len(t.changes)-1]
		return err
	}
	return nil
}

func (t *transaction) writeJournal() error {
	changes := make([]recoveryChange, 0, len(t.changes))
	for _, change := range t.changes {
		changes = append(changes, recoveryChange(change))
	}
	raw, err := json.Marshal(recoveryJournal{Schema: 2, ID: strings.TrimPrefix(t.id, transactionPrefix), Transaction: t.id, Changes: changes})
	if err != nil {
		return err
	}
	if _, err := writeAtomically(t.dir, ".cli-helpers-skills-recovery-*", filepath.Join(t.dir, recoveryFileName), raw, transactionOperations.syncDirectory); err != nil {
		return err
	}
	transactionBoundary("journal")
	return nil
}

func (t *transaction) setPhase(index int, phase string) error {
	t.changes[index].Phase = phase
	return t.writeJournal()
}

func recoverTransaction(dir string) error {
	journal, path, txDir, err := readRecoveryJournal(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := readState(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil && state.RecoveryID == journal.ID {
		for _, c := range journal.Changes {
			if err := verifyCommittedChange(dir, c); err != nil {
				return err
			}
			if err := verifyBackupChange(txDir, c); err != nil {
				return err
			}
		}
		if err := stateDirectorySync(dir); err != nil {
			return fmt.Errorf("persist committed skills ownership: %w", err)
		}
		return finalizeJournal(path, txDir)
	}
	for _, c := range journal.Changes {
		if c.Existed && !stateOwnsDigest(state, c.Name, c.Old) {
			return fmt.Errorf("%w: recovery target %s lacks prior ownership", ErrStateCorrupt, c.Name)
		}
	}
	for i := len(journal.Changes) - 1; i >= 0; i-- {
		if err := restoreChange(dir, txDir, journal.Changes[i]); err != nil {
			return err
		}
	}
	return finalizeJournal(path, txDir)
}

// recoveryPending checks a journal without touching any target or transaction
// content. A dry-run cannot safely classify an interrupted target against the
// pre-transaction marker, so callers must retry with a real Sync first.
func recoveryPending(dir string) error {
	_, _, _, err := readRecoveryJournal(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: rerun sync without dry-run", ErrRecoveryPending)
}

func readRecoveryJournal(dir string) (recoveryJournal, string, string, error) {
	path := filepath.Join(dir, recoveryFileName)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return recoveryJournal{}, "", "", fmt.Errorf("%w: recovery journal is a symlink", ErrStateCorrupt)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return recoveryJournal{}, "", "", err
	} else if errors.Is(err, fs.ErrNotExist) {
		return recoveryJournal{}, "", "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return recoveryJournal{}, "", "", err
	}
	var journal recoveryJournal
	if err := json.Unmarshal(raw, &journal); err != nil || !validJournal(journal) {
		return recoveryJournal{}, "", "", fmt.Errorf("%w: invalid recovery journal", ErrStateCorrupt)
	}
	txDir := filepath.Join(dir, journal.Transaction)
	if err := validTransactionDir(txDir); err != nil {
		return recoveryJournal{}, "", "", err
	}
	return journal, path, txDir, nil
}

func stateOwnsDigest(s state, name, digest string) bool {
	for _, plugin := range s.Plugins {
		if plugin.Skills[name] == digest {
			return true
		}
	}
	return false
}

func validJournal(j recoveryJournal) bool {
	if j.Schema != 2 || j.ID == "" || j.Transaction != transactionPrefix+j.ID || filepath.Base(j.Transaction) != j.Transaction || len(j.Changes) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range j.Changes {
		if !validSkillName(c.Name) || seen[c.Name] || (c.Old != "" && !validDigest(c.Old)) || (c.New != "" && !validDigest(c.New)) || (c.Existed && c.Old == "") || (!c.Existed && c.Old != "") || (c.New == "" && !c.Existed) || (c.Phase != "prepared" && c.Phase != "backed_up" && c.Phase != "published") {
			return false
		}
		seen[c.Name] = true
	}
	return true
}

func validTransactionDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // A completed cleanup can leave a journal whose target proves recovery safe.
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: transaction directory is unsafe", ErrStateCorrupt)
	}
	return nil
}

func checkTransactionSubdir(root, name string) error {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: transaction %s directory is unsafe", ErrStateCorrupt, name)
	}
	return nil
}

func digestAt(root, name string) (exists bool, digest string, err error) {
	dir, err := os.OpenRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return true, "", fmt.Errorf("%w: unsafe transaction content", ErrStateCorrupt)
	}
	digest, err = subtreeDigest(dir.FS(), name)
	if err != nil {
		return true, "", fmt.Errorf("%w: digest transaction content: %v", ErrStateCorrupt, err)
	}
	return true, digest, nil
}

func verifyCommittedChange(dir string, c recoveryChange) error {
	exists, digest, err := digestAt(dir, c.Name)
	if err != nil {
		return err
	}
	if c.New == "" {
		if exists {
			return fmt.Errorf("%w: committed removal %s reappeared", ErrStateCorrupt, c.Name)
		}
		return nil
	}
	if !exists || digest != c.New {
		return fmt.Errorf("%w: committed target %s differs", ErrStateCorrupt, c.Name)
	}
	return nil
}

// verifyBackupChange preserves a copy captured after classification until the
// transaction is conclusively finalized. A target can change in the small
// interval between its last digest check and the rename into backup; accepting
// that different backup would silently discard user content during cleanup.
func verifyBackupChange(txDir string, c recoveryChange) error {
	if !c.Existed {
		return nil
	}
	if err := checkTransactionSubdir(txDir, "backup"); err != nil {
		return err
	}
	exists, digest, err := digestAt(filepath.Join(txDir, "backup"), c.Name)
	if err != nil {
		return err
	}
	if exists && digest != c.Old {
		return fmt.Errorf("%w: backup %s changed after capture", ErrStateCorrupt, c.Name)
	}
	return nil
}

func restoreChange(dir, txDir string, c recoveryChange) error {
	targetExists, targetDigest, err := digestAt(dir, c.Name)
	if err != nil {
		return err
	}
	if !c.Existed {
		if !targetExists {
			return syncTransactionDirectories(dir)
		}
		if err := checkTransactionSubdir(txDir, "proof"); err != nil {
			return err
		}
		proofExists, proofDigest, err := digestAt(filepath.Join(txDir, "proof"), c.Name)
		if err != nil {
			return err
		}
		if !proofExists || proofDigest != c.New {
			return fmt.Errorf("%w: added target %s lacks transaction proof", ErrStateCorrupt, c.Name)
		}
		if targetDigest != c.New {
			return fmt.Errorf("%w: added target %s is not transaction content", ErrStateCorrupt, c.Name)
		}
		if err := rootedRemoveAll(dir, filepath.Join(dir, c.Name)); err != nil {
			return err
		}
		return syncTransactionDirectories(dir)
	}
	if err := checkTransactionSubdir(txDir, "backup"); err != nil {
		return err
	}
	backupRoot := filepath.Join(txDir, "backup")
	backupExists, backupDigest, err := digestAt(backupRoot, c.Name)
	if err != nil {
		return err
	}
	if !backupExists {
		if targetExists && targetDigest == c.Old {
			return syncTransactionDirectories(dir)
		}
		return fmt.Errorf("%w: original %s is not recoverable", ErrStateCorrupt, c.Name)
	}
	if backupDigest != c.Old {
		return fmt.Errorf("%w: backup %s differs", ErrStateCorrupt, c.Name)
	}
	if targetExists {
		if targetDigest == c.Old {
			return syncTransactionDirectories(dir)
		}
		if targetDigest != c.New {
			return fmt.Errorf("%w: target %s differs", ErrStateCorrupt, c.Name)
		}
		if err := rootedRemoveAll(dir, filepath.Join(dir, c.Name)); err != nil {
			return err
		}
	}
	target := filepath.Join(dir, c.Name)
	if err := transactionOperations.mkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	backup := filepath.Join(backupRoot, c.Name)
	if err := rootedRename(dir, backup, target); err != nil {
		return err
	}
	return syncTransactionDirectories(filepath.Dir(backup), filepath.Dir(target))
}

func finalizeJournal(path, txDir string) error {
	root := filepath.Dir(txDir)
	if path != filepath.Join(root, recoveryFileName) || filepath.Base(txDir) == transactionPrefix || !strings.HasPrefix(filepath.Base(txDir), transactionPrefix) {
		return fmt.Errorf("%w: recovery cleanup paths are unsafe", ErrStateCorrupt)
	}
	if err := validTransactionDir(txDir); err != nil {
		return err
	}
	if err := rootedRemoveAll(root, txDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := syncTransactionDirectories(root); err != nil {
		return err
	}
	if err := transactionOperations.remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncTransactionDirectories(root)
}

func (t *transaction) syncRenameParents(from, to string) error {
	return syncTransactionDirectories(filepath.Dir(from), filepath.Dir(to))
}

func (t *transaction) replace(source fs.FS, executablePaths []string, name, old, new string) error {
	if err := t.start(); err != nil {
		return err
	}
	stageRoot := filepath.Join(t.transactionDir(), "stage")
	if err := checkTransactionSubdir(t.transactionDir(), "stage"); err != nil {
		return err
	}
	executables, err := executableSet(source, executablePaths)
	if err != nil {
		return err
	}
	if err := copySkill(source, name, stageRoot, executables); err != nil {
		return err
	}
	stageExists, stageDigest, err := digestAt(stageRoot, name)
	if err != nil || !stageExists || stageDigest != new {
		if err == nil {
			err = fmt.Errorf("%w: staged skill digest differs", ErrStateCorrupt)
		}
		return err
	}
	proofRoot := filepath.Join(t.transactionDir(), "proof")
	if err := checkTransactionSubdir(t.transactionDir(), "proof"); err != nil {
		return err
	}
	if err := copySkill(source, name, proofRoot, executables); err != nil {
		return err
	}
	proofExists, proofDigest, err := digestAt(proofRoot, name)
	if err != nil || !proofExists || proofDigest != new {
		if err == nil {
			err = fmt.Errorf("%w: transaction proof digest differs", ErrStateCorrupt)
		}
		return err
	}
	targetExists, targetDigest, err := digestAt(t.dir, name)
	if err != nil {
		return err
	}
	if old == "" && targetExists || old != "" && (!targetExists || targetDigest != old) {
		return fmt.Errorf("%w: target %s changed after planning", ErrStateCorrupt, name)
	}
	c := txChange{Name: name, Old: old, New: new, Existed: old != ""}
	if err := t.record(c); err != nil {
		return err
	}
	index := len(t.changes) - 1
	target := filepath.Join(t.dir, name)
	if c.Existed {
		backup := t.backup(name)
		if err := checkTransactionSubdir(t.transactionDir(), "backup"); err != nil {
			return err
		}
		created, err := ensureDirectoryAncestry(filepath.Dir(backup), transactionOperations.mkdirAll)
		if err != nil {
			return err
		}
		if err := syncCreatedDirectoryAncestry(created, transactionOperations.syncDirectory); err != nil {
			return err
		}
		if err := rootedRename(t.dir, target, backup); err != nil {
			return err
		}
		if err := t.syncRenameParents(target, backup); err != nil {
			return err
		}
		if err := verifyBackupChange(t.transactionDir(), recoveryChange(c)); err != nil {
			return err
		}
		transactionBoundary("backup")
		if err := t.setPhase(index, "backed_up"); err != nil {
			return err
		}
	}
	if err := rootedRename(t.dir, filepath.Join(stageRoot, name), target); err != nil {
		return err
	}
	if err := t.syncRenameParents(filepath.Join(stageRoot, name), target); err != nil {
		return err
	}
	transactionBoundary("publish")
	return t.setPhase(index, "published")
}
func (t *transaction) remove(name, old string) error {
	if err := t.start(); err != nil {
		return err
	}
	targetExists, targetDigest, err := digestAt(t.dir, name)
	if err != nil {
		return err
	}
	if !targetExists {
		return nil
	}
	if targetDigest != old {
		return fmt.Errorf("%w: target %s changed after planning", ErrStateCorrupt, name)
	}
	c := txChange{Name: name, Old: old, Existed: true}
	if err := t.record(c); err != nil {
		return err
	}
	backup := t.backup(name)
	if err := checkTransactionSubdir(t.transactionDir(), "backup"); err != nil {
		return err
	}
	created, err := ensureDirectoryAncestry(filepath.Dir(backup), transactionOperations.mkdirAll)
	if err != nil {
		return err
	}
	if err := syncCreatedDirectoryAncestry(created, transactionOperations.syncDirectory); err != nil {
		return err
	}
	if err := rootedRename(t.dir, filepath.Join(t.dir, name), backup); err != nil {
		return err
	}
	if err := t.syncRenameParents(filepath.Join(t.dir, name), backup); err != nil {
		return err
	}
	if err := verifyBackupChange(t.transactionDir(), recoveryChange(c)); err != nil {
		return err
	}
	transactionBoundary("backup")
	return t.setPhase(len(t.changes)-1, "backed_up")
}
func (t *transaction) rollback() error {
	// Planning never starts a transaction. In particular, an unstarted
	// transaction's transactionDir is t.dir, so finalizing it would otherwise
	// treat the caller's complete target as disposable recovery evidence.
	if t.id == "" {
		return nil
	}
	var rollbackErr error
	for i := len(t.changes) - 1; i >= 0; i-- {
		c := t.changes[i]
		if err := restoreChange(t.dir, t.transactionDir(), recoveryChange(c)); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		t.restoredNames[c.Name] = true
	}
	if rollbackErr == nil {
		rollbackErr = finalizeJournal(filepath.Join(t.dir, recoveryFileName), t.transactionDir())
	}
	return rollbackErr
}
func (t *transaction) restored(name string) bool { return t.restoredNames[name] }
func (t *transaction) commit() error {
	if t.id == "" {
		return nil
	}
	for _, change := range t.changes {
		if err := verifyBackupChange(t.transactionDir(), recoveryChange(change)); err != nil {
			return err
		}
	}
	return finalizeJournal(filepath.Join(t.dir, recoveryFileName), t.transactionDir())
}
func copySkill(source fs.FS, name, stage string, executables map[string]bool) error {
	created := map[string]bool{}
	makeDir := func(path string) error {
		if err := transactionOperations.mkdirAll(path, 0o755); err != nil {
			return err
		}
		created[path] = true
		return nil
	}
	err := fs.WalkDir(source, name, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlink source %s", path)
		}
		dest := filepath.Join(stage, filepath.FromSlash(path))
		if e.IsDir() {
			return makeDir(dest)
		}
		if !e.Type().IsRegular() {
			return fmt.Errorf("refuse non-regular source %s", path)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		if err := makeDir(filepath.Dir(dest)); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if executables[path] {
			mode = 0o755
		}
		file, err := durableFileOperations.createFile(dest, mode)
		if err != nil {
			return err
		}
		return writeAndSync(file, data)
	})
	if err != nil {
		return err
	}
	dirs := make([]string, 0, len(created))
	for path := range created {
		dirs = append(dirs, path)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	if err := syncTransactionDirectories(dirs...); err != nil {
		return err
	}
	return syncDirectoryChain(stage, filepath.Dir(stage))
}

func lock(ctx context.Context, dir string, timeout time.Duration) (func(), error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs, err = canonicalSystemAlias(abs)
	if err != nil {
		return nil, err
	}
	if err := validateExistingAncestry(abs); err != nil {
		return nil, err
	}
	parent := filepath.Dir(abs)
	created, err := ensureDirectoryAncestry(parent, func(path string, mode fs.FileMode) error { return os.Mkdir(path, mode) })
	if err != nil {
		return nil, err
	}
	if err := syncCreatedDirectoryAncestry(created, syncDirectory); err != nil {
		return nil, err
	}
	// One parent-level lock deliberately trades sibling parallelism for a stable
	// identity across relative, absolute, symlink-normalized, and case-folded
	// spellings of the same filesystem target.
	path := filepath.Join(parent, ".cli-helpers-skills-lock")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refuse symlinked skills lock %s", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file := flock.New(path)
	deadline := time.Now().Add(timeout)
	for {
		locked, err := file.TryLock()
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() { _ = file.Unlock(); _ = file.Close() }, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("skills target lock timed out: %s", dir)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// macOS exposes /tmp and /var as system aliases. Canonicalize only those
// platform-owned aliases before enforcing the no-user-symlink target rule.
func canonicalSystemAlias(path string) (string, error) {
	for alias, expected := range map[string]string{"/tmp": "/private/tmp", "/var": "/private/var"} {
		if path != alias && !strings.HasPrefix(path, alias+string(filepath.Separator)) {
			continue
		}
		info, err := os.Lstat(alias)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
		resolved, err := filepath.EvalSymlinks(alias)
		if err != nil {
			return "", err
		}
		if resolved != expected {
			return "", fmt.Errorf("refuse non-system symlinked skills target ancestor %s", alias)
		}
		return filepath.Join(resolved, strings.TrimPrefix(path, alias+string(filepath.Separator))), nil
	}
	return path, nil
}

func validateTargetAncestry(dir string) error {
	_, err := ValidateTarget(dir)
	return err
}

// ValidateTarget normalizes a target path and rejects symlinked or non-
// directory existing ancestors. It permits only the verified macOS /tmp and
// /var system aliases, returning their canonical path for callers that must
// deduplicate targets before writing them.
func ValidateTarget(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err = canonicalSystemAlias(abs)
	if err != nil {
		return "", err
	}
	if err := validateExistingAncestry(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func validateExistingAncestry(path string) error {
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlinked skills target ancestor %s", current)
		}
		if !info.IsDir() {
			if current == path {
				return fmt.Errorf("skills target %s is not a directory", current)
			}
			return fmt.Errorf("skills target ancestor %s is not a directory", current)
		}
	}
	return nil
}
