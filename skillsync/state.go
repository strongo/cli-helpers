package skillsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StateFileName is deliberately provider-neutral and stores only verified
// ownership/provenance, never mutable source content.
const StateFileName = ".cli-helpers-skills-sync.json"
const stateSchema = 1

type state struct {
	Schema     int                    `json:"schema"`
	RecoveryID string                 `json:"recovery_id,omitempty"`
	Plugins    map[string]pluginState `json:"plugins"`
}
type pluginState struct {
	Revision  string            `json:"revision"`
	Digest    string            `json:"digest"`
	CLI       string            `json:"cli"`
	Legacy    bool              `json:"legacy,omitempty"`
	Suppliers map[string]string `json:"suppliers,omitempty"`
	// SupplierCLIVersions records each successful supplier's running CLI
	// version separately from Source, which remains the sole bundle source.
	// Missing entries are intentionally unknown for markers written before this
	// provenance field existed.
	SupplierCLIVersions map[string]string `json:"supplier_cli_versions,omitempty"`
	Source              Source            `json:"source"`
	Skills              map[string]string `json:"skills"`
	SyncedAt            time.Time         `json:"synced_at"`
}

// statePublishedError reports a failure after the replacement marker has
// already become visible. Callers must retain matching target files and the
// recovery journal so a later Sync can verify and durably finalize it.
type statePublishedError struct{ err error }

func (e statePublishedError) Error() string { return e.err.Error() }
func (e statePublishedError) Unwrap() error { return e.err }

var stateDirectorySync = syncDirectory

// durableFileOperations keeps failure injection private to the package tests.
// Production callers always use the os-backed defaults below.
type durableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type durableFileOperationSet struct {
	mkdirAll   func(string, fs.FileMode) error
	mkdirTemp  func(string, string) (string, error)
	createTemp func(string, string) (durableFile, error)
	createFile func(string, fs.FileMode) (durableFile, error)
	remove     func(string) error
}

var durableFileOperations = durableFileOperationSet{
	mkdirAll:  func(path string, mode fs.FileMode) error { return os.MkdirAll(path, mode) },
	mkdirTemp: os.MkdirTemp,
	createTemp: func(dir, pattern string) (durableFile, error) {
		return os.CreateTemp(dir, pattern)
	},
	createFile: func(path string, mode fs.FileMode) (durableFile, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	},
	remove: os.Remove,
}

func writeAndSync(file durableFile, data []byte) error {
	n, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return err
	}
	if n != len(data) {
		_ = file.Close()
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// writeAtomically publishes a complete file only after its bytes reached stable
// storage. The returned flag means the replacement rename has succeeded, so a
// caller can retain recovery evidence when only parent persistence failed.
func writeAtomically(dir, pattern, destination string, data []byte, directorySync func(string) error) (published bool, err error) {
	tmp, err := durableFileOperations.createTemp(dir, pattern)
	if err != nil {
		return false, err
	}
	name := tmp.Name()
	defer func() { _ = durableFileOperations.remove(name) }()
	if err := writeAndSync(tmp, data); err != nil {
		return false, err
	}
	if err := rootedRename(dir, name, destination); err != nil {
		return false, err
	}
	if err := directorySync(dir); err != nil {
		return true, err
	}
	return true, nil
}

func statePath(dir string) string { return filepath.Join(dir, StateFileName) }
func readState(dir string) (state, error) {
	if info, err := os.Lstat(statePath(dir)); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return state{}, fmt.Errorf("%w: state marker is a symlink", ErrStateCorrupt)
	} else if err != nil {
		return state{}, err
	}
	raw, err := os.ReadFile(statePath(dir))
	if err != nil {
		return state{}, err
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return state{}, fmt.Errorf("%w: parse %s: %v", ErrStateCorrupt, statePath(dir), err)
	}
	if s.Schema > stateSchema || s.Schema < 1 {
		return state{}, fmt.Errorf("%w: unsupported schema %d", ErrStateCorrupt, s.Schema)
	}
	if s.Plugins == nil {
		s.Plugins = map[string]pluginState{}
	}
	owners := map[string]string{}
	for plugin, entry := range s.Plugins {
		if !validIdentityParts(plugin) || entry.Skills == nil {
			return state{}, fmt.Errorf("%w: invalid plugin state", ErrStateCorrupt)
		}
		if entry.Legacy {
			if entry.Revision != "" || entry.Digest != "" || entry.CLI != "" || entry.Source != (Source{}) || len(entry.Suppliers) != 0 {
				return state{}, fmt.Errorf("%w: invalid legacy plugin state", ErrStateCorrupt)
			}
		} else if !validIdentityParts(entry.CLI) || !validSource(entry.Source) || entry.Revision != entry.Source.Revision || entry.Digest != entry.Source.Digest {
			return state{}, fmt.Errorf("%w: invalid plugin provenance", ErrStateCorrupt)
		}
		if entry.Suppliers == nil {
			entry.Suppliers = map[string]string{}
			if entry.CLI != "" && entry.Revision != "" {
				entry.Suppliers[entry.CLI] = entry.Revision
			}
			s.Plugins[plugin] = entry
		}
		for cli, revision := range entry.Suppliers {
			if !validIdentityParts(cli) || !validRevision(revision) || revision != entry.Source.Revision {
				return state{}, fmt.Errorf("%w: invalid supplier", ErrStateCorrupt)
			}
		}
		for cli, version := range entry.SupplierCLIVersions {
			if !validIdentityParts(cli) || !validCurrentCLIVersion(version) {
				return state{}, fmt.Errorf("%w: invalid supplier CLI version", ErrStateCorrupt)
			}
			if !entry.Legacy && entry.Suppliers[cli] == "" {
				return state{}, fmt.Errorf("%w: supplier CLI version has no supplier", ErrStateCorrupt)
			}
		}
		for name, digest := range entry.Skills {
			if !validSkillName(name) || !validDigest(digest) {
				return state{}, fmt.Errorf("%w: invalid owned skill", ErrStateCorrupt)
			}
			if prior := owners[name]; prior != "" {
				return state{}, fmt.Errorf("%w: %s claimed by both %s and %s", ErrStateCorrupt, name, prior, plugin)
			}
			owners[name] = plugin
		}
	}
	return s, nil
}
func validIdentityParts(serialized string) bool {
	parts := strings.Split(serialized, "/")
	return len(parts) == 2 && validIdentityPart(parts[0]) && validIdentityPart(parts[1])
}
func writeState(dir string, s state) error {
	s.Schema = stateSchema
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := durableFileOperations.mkdirAll(dir, 0o755); err != nil {
		return err
	}
	published, err := writeAtomically(dir, ".cli-helpers-skills-*.tmp", statePath(dir), raw, stateDirectorySync)
	if err != nil && published {
		return statePublishedError{err: err}
	}
	return err
}
func emptyOrExistingState(dir string) (state, error) {
	s, err := readState(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return state{Schema: stateSchema, Plugins: map[string]pluginState{}}, nil
	}
	return s, err
}

// ReadStatus reads only the small ownership marker. A missing marker is the
// normal not-yet-synced state, while corrupt state remains a safe error.
func ReadStatus(dir string) (Status, error) {
	s, err := readState(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	status := Status{Installed: true, Plugins: map[string]Source{}, SupplierCLIVersions: map[string]map[string]string{}}
	for plugin, entry := range s.Plugins {
		status.Plugins[plugin] = entry.Source
		versions := map[string]string{}
		for cli, version := range entry.SupplierCLIVersions {
			versions[cli] = version
		}
		if len(versions) > 0 {
			status.SupplierCLIVersions[plugin] = versions
		}
	}
	return status, nil
}

func importLegacy(dir string, legacy LegacyImport, suppliedCLI ...Identity) (pluginState, error) {
	if legacy.MarkerFile == "" || filepath.Base(legacy.MarkerFile) != legacy.MarkerFile || !validPlugin(legacy.Plugin) {
		return pluginState{}, fmt.Errorf("%w: invalid legacy import", ErrInvalidConfig)
	}
	markerPath := filepath.Join(dir, legacy.MarkerFile)
	if info, err := os.Lstat(markerPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return pluginState{}, fmt.Errorf("%w: legacy marker is a symlink", ErrStateCorrupt)
	}
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		return pluginState{}, err
	}
	var marker struct {
		SchemaVersion int               `json:"schema_version"`
		Skills        map[string]string `json:"skills"`
		WBVersion     string            `json:"wb_version"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		return pluginState{}, fmt.Errorf("%w: parse legacy marker: %v", ErrStateCorrupt, err)
	}
	if marker.SchemaVersion != 1 || len(marker.Skills) == 0 {
		return pluginState{}, fmt.Errorf("%w: invalid legacy marker", ErrStateCorrupt)
	}
	for name, expected := range marker.Skills {
		if !validSkillName(name) || expected == "" {
			return pluginState{}, fmt.Errorf("%w: invalid legacy skill", ErrStateCorrupt)
		}
		actual, err := legacyWBDigest(os.DirFS(dir), name)
		if err != nil {
			return pluginState{}, fmt.Errorf("%w: validate legacy %s: %v", ErrStateCorrupt, name, err)
		}
		if actual != expected {
			return pluginState{}, fmt.Errorf("%w: legacy %s content differs", ErrStateCorrupt, name)
		}
		modern, err := installedDigest(dir, name)
		if err != nil {
			return pluginState{}, fmt.Errorf("%w: digest legacy %s: %v", ErrStateCorrupt, name, err)
		}
		marker.Skills[name] = modern
	}
	versions := map[string]string{}
	if marker.WBVersion != "" {
		if !validCurrentCLIVersion(marker.WBVersion) {
			return pluginState{}, fmt.Errorf("%w: invalid legacy wb_version", ErrStateCorrupt)
		}
		if len(suppliedCLI) > 0 {
			if !validIdentity(suppliedCLI[0]) {
				return pluginState{}, fmt.Errorf("%w: invalid legacy CLI", ErrStateCorrupt)
			}
			versions[suppliedCLI[0].String()] = marker.WBVersion
		}
	}
	return pluginState{Legacy: true, Skills: marker.Skills, SupplierCLIVersions: versions}, nil
}
