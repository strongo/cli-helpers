package skillsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

type skill struct{ Name, Digest string }

// Discover lists valid skill directories and their deterministic digests.
func Discover(source fs.FS) ([]Skill, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, err
	}
	var result []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		info, err := fs.Stat(source, name+"/SKILL.md")
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s/SKILL.md is not a regular file", ErrInvalidConfig, name)
		}
		digest, err := subtreeDigest(source, name)
		if err != nil {
			return nil, err
		}
		result = append(result, Skill{Name: name, Digest: digest})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func validIdentity(i Identity) bool {
	return validIdentityPart(i.Publisher) && validIdentityPart(i.Name)
}
func validPlugin(p PluginIdentity) bool {
	return validIdentityPart(p.Publisher) && validIdentityPart(p.Name)
}
func validIdentityPart(part string) bool {
	return part != "" && strings.TrimSpace(part) == part && !strings.ContainsAny(part, `/\\:`)
}

func validateBundle(b Bundle, current string) ([]skill, error) {
	if !validPlugin(b.Plugin) || !validSource(b.Source) || b.FS == nil {
		return nil, fmt.Errorf("%w: bundle requires plugin, source, and FS", ErrInvalidConfig)
	}
	executables, err := executableSet(b.FS, b.ExecutablePaths)
	if err != nil {
		return nil, err
	}
	if (b.Source.Compatibility.MinCLI != "" && !validVersion(b.Source.Compatibility.MinCLI)) || (b.Source.Compatibility.MaxCLI != "" && !validVersion(b.Source.Compatibility.MaxCLI)) {
		return nil, fmt.Errorf("%w: invalid CLI compatibility bounds", ErrInvalidConfig)
	}
	if current != "" && !compatible(current, b.Source.Compatibility) {
		return nil, fmt.Errorf("%w: %s is incompatible with CLI %s", ErrInvalidConfig, b.Plugin.String(), current)
	}
	digest, err := digestTree(b.FS, ".", executables)
	if err != nil {
		return nil, err
	}
	if digest != b.Source.Digest {
		return nil, fmt.Errorf("%w for %s: got %s, want %s", ErrDigestMismatch, b.Plugin.String(), digest, b.Source.Digest)
	}
	entries, err := fs.ReadDir(b.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read bundle %s: %w", b.Plugin.String(), err)
	}
	var skills []skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, "/") {
			return nil, fmt.Errorf("%w: invalid skill name %q", ErrInvalidConfig, name)
		}
		info, err := fs.Stat(b.FS, name+"/SKILL.md")
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s/SKILL.md is not a regular file", ErrInvalidConfig, name)
		}
		h, err := digestTree(b.FS, name, executables)
		if err != nil {
			return nil, fmt.Errorf("digest skill %s: %w", name, err)
		}
		skills = append(skills, skill{Name: name, Digest: h})
	}
	if len(skills) == 0 {
		return nil, fmt.Errorf("%w: bundle %s has no skills", ErrInvalidConfig, b.Plugin.String())
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

func validSource(s Source) bool {
	return s.Repository != "" && s.Path != "" && validRevision(s.Revision) && validVersion(s.Version) && validDigest(s.Digest) && fs.ValidPath(s.Path)
}

func validRevision(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	for _, r := range revision {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func executableSet(source fs.FS, paths []string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, path := range paths {
		if path == "." || !fs.ValidPath(path) || result[path] {
			return nil, fmt.Errorf("%w: invalid executable path %q", ErrInvalidConfig, path)
		}
		info, err := fs.Stat(source, path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: executable path %q is not a regular file", ErrInvalidConfig, path)
		}
		result[path] = true
	}
	if err := fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o111 != 0 {
			result[path] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// Digest returns a deterministic SHA-256 of source bytes and executable mode.
// For embed.FS callers, use DigestWithExecutables with the source descriptor's
// explicit executable paths.
func Digest(source fs.FS) (string, error) { return digestTree(source, ".", nil) }
func DigestWithExecutables(source fs.FS, executablePaths []string) (string, error) {
	executables, err := executableSet(source, executablePaths)
	if err != nil {
		return "", err
	}
	return digestTree(source, ".", executables)
}
func subtreeDigest(source fs.FS, root string) (string, error) {
	return digestTree(source, root, nil)
}
func digestTree(source fs.FS, root string, executables map[string]bool) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(source, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsafe non-regular source entry %q", path)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		rel := path
		if root != "." {
			rel = strings.TrimPrefix(path, root+"/")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if executables[path] || executables == nil && info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if _, err := fmt.Fprintf(h, "%s\x00%04o\x00", rel, mode.Perm()); err != nil {
			return err
		}
		if _, err := h.Write(data); err != nil {
			return err
		}
		_, err = h.Write([]byte{0})
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// legacyWBDigest preserves the historical WB marker contract: path, NUL,
// bytes, NUL, without permissions. It is migration-only and must not be used
// for new ownership records.
func legacyWBDigest(source fs.FS, root string) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(source, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsafe non-regular source entry %q", path)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		rel := path
		if root != "." {
			rel = strings.TrimPrefix(path, root+"/")
		}
		_, err = fmt.Fprintf(h, "%s\x00%s\x00", rel, data)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func compatible(current string, c Compatibility) bool {
	if (c.MinCLI != "" || c.MaxCLI != "") && !validVersion(current) {
		return false
	}
	if c.MinCLI != "" && versionCompare(current, c.MinCLI) < 0 {
		return false
	}
	return c.MaxCLI == "" || versionCompare(current, c.MaxCLI) <= 0
}

// Compatible reports whether a semantic CLI version satisfies compatibility.
func Compatible(current string, c Compatibility) bool { return compatible(current, c) }

// CompareVersions compares two validated semantic versions.
func CompareVersions(a, b string) (int, error) {
	if !validVersion(a) || !validVersion(b) {
		return 0, fmt.Errorf("%w: invalid semantic version", ErrInvalidConfig)
	}
	return versionCompare(a, b), nil
}
func validVersion(v string) bool {
	v = strings.TrimPrefix(v, "v")
	if v == "" || v == "unknown" || v == "(devel)" || v == "dev" {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if len(p) > 1 && p[0] == '0' {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
		if _, err := strconv.ParseUint(p, 10, 64); err != nil {
			return false
		}
	}
	return true
}
func versionCompare(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	aa := strings.Split(a, ".")
	bb := strings.Split(b, ".")
	for n := len(aa); n < len(bb); n++ {
		aa = append(aa, "0")
	}
	for n := len(bb); n < len(aa); n++ {
		bb = append(bb, "0")
	}
	for i := range aa {
		ai, _ := strconv.ParseUint(aa[i], 10, 64)
		bi, _ := strconv.ParseUint(bb[i], 10, 64)
		if ai == bi {
			continue
		}
		if ai < bi {
			return -1
		}
		return 1
	}
	return 0
}
