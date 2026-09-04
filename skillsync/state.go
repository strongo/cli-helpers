package skillsync

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Source    Source            `json:"source"`
	Skills    map[string]string `json:"skills"`
	SyncedAt  time.Time         `json:"synced_at"`
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
		if plugin == "" || !strings.Contains(plugin, "/") || entry.Skills == nil {
			return state{}, fmt.Errorf("%w: invalid plugin state", ErrStateCorrupt)
		}
		if entry.Legacy {
			if entry.Revision != "" || entry.Digest != "" || entry.CLI != "" || entry.Source != (Source{}) || len(entry.Suppliers) != 0 {
				return state{}, fmt.Errorf("%w: invalid legacy plugin state", ErrStateCorrupt)
			}
		} else if !validSource(entry.Source) || entry.Revision != entry.Source.Revision || entry.Digest != entry.Source.Digest {
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
			if !validIdentityParts(cli) || revision == "" {
				return state{}, fmt.Errorf("%w: invalid supplier", ErrStateCorrupt)
			}
		}
		for name, digest := range entry.Skills {
			if name == "" || !fs.ValidPath(name) || filepath.Base(name) != name || len(digest) != 64 {
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cli-helpers-skills-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err := rootedRename(dir, name, statePath(dir)); err != nil {
		return err
	}
	return syncDirectory(dir)
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
	status := Status{Installed: true, Plugins: map[string]Source{}}
	for plugin, entry := range s.Plugins {
		status.Plugins[plugin] = entry.Source
	}
	return status, nil
}

func importLegacy(dir string, legacy LegacyImport) (pluginState, error) {
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
	return pluginState{Legacy: true, Skills: marker.Skills}, nil
}
