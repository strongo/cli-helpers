package skillsync

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func executableResourceBundle(t *testing.T, revision, content string) Bundle {
	t.Helper()
	root := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")},
		"alpha/run":      &fstest.MapFile{Data: []byte(content), Mode: 0o755},
	}
	digest, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	return Bundle{
		Plugin:          PluginIdentity{Publisher: "strongo", Name: "executable-plugin"},
		Source:          Source{Repository: "github.com/strongo/executable-plugin", Path: "skills", Revision: revisionForTest(revision), Version: "1.0.0", Digest: digest},
		FS:              root,
		ExecutablePaths: []string{"alpha/run"},
	}
}

func withWindowsInstalledDigestSemantics(t *testing.T) {
	t.Helper()
	previousModes, previousCreateFile := installedDigestNormalizesExecutableModes, durableFileOperations.createFile
	installedDigestNormalizesExecutableModes = true
	durableFileOperations.createFile = func(path string, mode fs.FileMode) (durableFile, error) {
		file, err := previousCreateFile(path, mode)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(file.Name(), mode&^0o111); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	t.Cleanup(func() {
		installedDigestNormalizesExecutableModes = previousModes
		durableFileOperations.createFile = previousCreateFile
	})
}

func assertInstalledExecutableModeNormalized(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "alpha", "run")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("execute bits remain on installed Windows resource: %v", info.Mode())
	}
}

func TestWindowsInstalledExecutableResourceJourney(t *testing.T) {
	withWindowsInstalledDigestSemantics(t)
	initial := executableResourceBundle(t, "windows-executable-initial", "initial")
	updated := executableResourceBundle(t, "windows-executable-updated", "updated")
	recovered := executableResourceBundle(t, "windows-executable-recovered", "recovered")

	t.Run("install no-op update recovery and removal", func(t *testing.T) {
		dir := t.TempDir()
		report, err := Sync(context.Background(), config(t, initial), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Added), ",") != "alpha" {
			t.Fatalf("install report=%#v err=%v", report, err)
		}
		assertInstalledExecutableModeNormalized(t, dir)

		report, err = Sync(context.Background(), config(t, initial), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Unchanged), ",") != "alpha" {
			t.Fatalf("no-op report=%#v err=%v", report, err)
		}

		report, err = Sync(context.Background(), config(t, updated), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Updated), ",") != "alpha" {
			t.Fatalf("content update report=%#v err=%v", report, err)
		}
		assertInstalledExecutableModeNormalized(t, dir)

		previous := stateDirectorySync
		t.Cleanup(func() { stateDirectorySync = previous })
		stateDirectorySync = func(string) error { return errors.New("injected state persistence failure") }
		report, err = Sync(context.Background(), config(t, recovered), Options{Dir: dir})
		if err == nil || len(report.Changes) != 1 || report.Changes[0].Outcome != Incomplete {
			t.Fatalf("interrupted executable update report=%#v err=%v", report, err)
		}
		assertInstalledExecutableModeNormalized(t, dir)
		if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); err != nil {
			t.Fatalf("recovery journal=%v", err)
		}
		stateDirectorySync = previous
		report, err = Sync(context.Background(), config(t, recovered), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Unchanged), ",") != "alpha" {
			t.Fatalf("recovery report=%#v err=%v", report, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, recoveryFileName)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("recovery journal remains: %v", err)
		}

		withoutExecutable := bundleWith(t, "executable-plugin", "windows-executable-removed", map[string]string{"beta": "replacement"})
		report, err = Sync(context.Background(), config(t, withoutExecutable), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Removed), ",") != "alpha" || strings.Join(report.Names(Added), ",") != "beta" {
			t.Fatalf("removal report=%#v err=%v", report, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "alpha")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("executable skill remains: %v", err)
		}
	})

	t.Run("local content conflict", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Sync(context.Background(), config(t, initial), Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		assertInstalledExecutableModeNormalized(t, dir)
		path := filepath.Join(dir, "alpha", "run")
		if err := os.WriteFile(path, []byte("local edit"), 0o644); err != nil {
			t.Fatal(err)
		}
		report, err := Sync(context.Background(), config(t, updated), Options{Dir: dir})
		if err != nil || strings.Join(report.Names(Conflict), ",") != "alpha" {
			t.Fatalf("conflict report=%#v err=%v", report, err)
		}
		content, err := os.ReadFile(path)
		if err != nil || string(content) != "local edit" {
			t.Fatalf("local content=%q err=%v", content, err)
		}
	})
}

func TestWindowsInstalledDigestNormalizationDoesNotWeakenCanonicalSourceDigest(t *testing.T) {
	withWindowsInstalledDigestSemantics(t)
	bundle := executableResourceBundle(t, "windows-canonical-mode", "body")
	withoutExecuteBit := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("skill")},
		"alpha/run":      &fstest.MapFile{Data: []byte("body"), Mode: 0o644},
	}
	plainDigest, err := Digest(withoutExecuteBit)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Source.Digest == plainDigest {
		t.Fatal("canonical digest ignored executable mode")
	}
	invalid := bundle
	invalid.FS = withoutExecuteBit
	invalid.ExecutablePaths = nil
	if _, err := validateBundle(invalid, "1.2.3"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mode-only source change validation error=%v", err)
	}
}
