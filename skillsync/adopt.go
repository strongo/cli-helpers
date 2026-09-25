package skillsync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// adoptedBackupDirName roots every durable adoption backup. It follows the
// package's existing ".cli-helpers-skills-*" convention so a host's own
// ignore rules and backup tooling recognize it the same way.
const adoptedBackupDirName = ".cli-helpers-skills-adopted-backup"

// classifyAdoption decides whether an existing target folder that no plugin
// has recorded owning can be adopted rather than refused as Conflict.
// Adoption requires positive proof the folder already is this skill: its
// SKILL.md frontmatter must declare the bundled skill's own name, and every
// file it contains must already be part of what the bundle ships for that
// skill. Differing bytes on a shared path are fine (the folder is about to
// be backed up and replaced); a path the bundle does not ship at all is not,
// because Sync would otherwise silently discard content it never verified.
//
// A folder that fails either check stays Conflict with a reason a person can
// act on directly: what was found, and what would let sync manage it.
func classifyAdoption(dir string, item skill, source fs.FS) (Action, string) {
	declared, err := targetFrontmatterName(dir, item.Name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Conflict, fmt.Sprintf("unmanaged target: %s has no SKILL.md; remove the folder to let sync manage it", item.Name)
	case err != nil:
		return Conflict, "unsafe target"
	case declared == "":
		return Conflict, fmt.Sprintf("unmanaged target: %s/SKILL.md has no frontmatter name; remove the folder to let sync manage it", item.Name)
	case declared != item.Name:
		return Conflict, fmt.Sprintf("unmanaged target: SKILL.md declares name %q, not %q; fix the frontmatter or remove the folder to let sync manage it", declared, item.Name)
	}
	foreign, err := foreignTargetFile(dir, item.Name, source)
	if err != nil {
		return Conflict, "unsafe target"
	}
	if foreign != "" {
		return Conflict, fmt.Sprintf("unmanaged target: %s is not part of this skill's bundle; remove it or back up the folder yourself before syncing", foreign)
	}
	return Adopted, ""
}

// targetFrontmatterName reads the leading YAML frontmatter "name" field from
// an existing target skill's own SKILL.md. It returns fs.ErrNotExist when the
// file is absent and "" (no error) when the file exists but declares no such
// field, which callers treat identically: not proof of adoption.
func targetFrontmatterName(dir, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
	if err != nil {
		return "", err
	}
	return frontmatterField(data, "name"), nil
}

// frontmatterField extracts a top-level scalar field from a leading
// "---"-delimited YAML frontmatter block. It intentionally implements only
// the minimal subset skillsync needs — a bare or quoted scalar on its own
// line — rather than taking a YAML dependency for one field.
func frontmatterField(data []byte, field string) string {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return ""
	}
	prefix := field + ":"
	for _, raw := range lines[1:] {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return ""
		}
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		value := strings.TrimSpace(trimmed[len(prefix):])
		return strings.Trim(value, `"'`)
	}
	return ""
}

// foreignTargetFile reports the first path under the target's existing skill
// directory that the bundle does not ship at that same relative path, or ""
// if every file is one the bundle already ships (a subset, adoptable). A
// symlink or other non-regular entry anywhere in the target is always
// foreign: adoption only ever replaces plain file content it can prove.
func foreignTargetFile(dir, name string, source fs.FS) (string, error) {
	bundleFiles := map[string]bool{}
	if err := fs.WalkDir(source, name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			bundleFiles[path] = true
		}
		return nil
	}); err != nil {
		return "", err
	}
	var foreign string
	err := fs.WalkDir(os.DirFS(dir), name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			foreign = path
			return fs.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		if !bundleFiles[path] {
			foreign = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return foreign, nil
}

// backupAdoptedSkill preserves the complete pre-adoption folder at a durable,
// timestamped path outside any transaction, so it survives that
// transaction's own commit-time cleanup and lets a caller recover exactly
// what adoption replaced. It writes and synchronizes the copy, the same
// durable primitives the transaction itself uses, before that transaction
// starts staging the bundle's replacement content — but it is not itself a
// crash-recovery boundary: the transaction/journal remains the sole
// authority for the target's own safety, and an interrupted backup simply
// leaves adoption unattempted for a later retry.
func backupAdoptedSkill(dir, name string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	root := filepath.Join(dir, adoptedBackupDirName, stamp)
	created, err := ensureDirectoryAncestry(root, transactionOperations.mkdirAll)
	if err != nil {
		return "", err
	}
	if err := syncCreatedDirectoryAncestry(created, transactionOperations.syncDirectory); err != nil {
		return "", err
	}
	if err := copyDurableBackup(os.DirFS(dir), name, root); err != nil {
		return "", err
	}
	dest := filepath.Join(root, name)
	if err := syncDirectoryChain(dest, root); err != nil {
		return "", err
	}
	return dest, nil
}
