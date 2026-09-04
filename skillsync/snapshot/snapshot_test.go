package snapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/strongo/cli-helpers/skillsync"
)

func fixture(t *testing.T) (skillsync.BundleDescriptor, fs.FS) {
	t.Helper()
	content := fstest.MapFS{
		"alpha/SKILL.md": &fstest.MapFile{Data: []byte("alpha")},
		"alpha/run":      &fstest.MapFile{Data: []byte("#!/bin/sh\n"), Mode: 0o644},
		"beta/SKILL.md":  &fstest.MapFile{Data: []byte("beta")},
	}
	digest, err := skillsync.DigestWithExecutables(content, []string{"alpha/run"})
	if err != nil {
		t.Fatal(err)
	}
	return skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: skillsync.Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: strings.Repeat("a", 40), Version: "1.2.3", Digest: digest}, ExecutablePaths: []string{"alpha/run"}}, content
}

func TestPackIsReproducibleAndRoundTripsModeAwareContent(t *testing.T) {
	descriptor, content := fixture(t)
	first, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Pack(descriptor, content)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("reproducible=%v err=%v", bytes.Equal(first, second), err)
	}
	decoded, unpacked, err := Unpack(first, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Source != descriptor.Source || len(decoded.ExecutablePaths) != 1 {
		t.Fatalf("descriptor = %#v", decoded)
	}
	digest, err := skillsync.DigestWithExecutables(unpacked, decoded.ExecutablePaths)
	if err != nil || digest != descriptor.Source.Digest {
		t.Fatalf("digest=%s err=%v", digest, err)
	}
	dir := filepath.Join(t.TempDir(), "embed")
	if err := WriteEmbedDirectory(dir, descriptor, content); err != nil {
		t.Fatal(err)
	}
	installed := os.DirFS(filepath.Join(dir, contentPrefix))
	installedDigest, err := skillsync.DigestWithExecutables(installed, descriptor.ExecutablePaths)
	if err != nil || installedDigest != descriptor.Source.Digest {
		t.Fatalf("installed digest=%s err=%v", installedDigest, err)
	}
	info, err := os.Stat(filepath.Join(dir, contentPrefix, "alpha", "run"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	bundle, err := skillsync.EmbeddedBundle(decoded, unpacked)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if _, err := skillsync.Sync(context.Background(), skillsync.Config{CLI: skillsync.Identity{Publisher: "strongo", Name: "tool"}, CurrentVersion: "1.2.3", Bundles: []skillsync.Bundle{bundle}}, skillsync.Options{Dir: target}); err != nil {
		t.Fatal(err)
	}
	installedFiles := fstest.MapFS{}
	if err := fs.WalkDir(content, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(target, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		installedFiles[name] = &fstest.MapFile{Data: data, Mode: info.Mode()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	installedDigest, err = skillsync.DigestWithExecutables(installedFiles, descriptor.ExecutablePaths)
	if err != nil || installedDigest != descriptor.Source.Digest {
		t.Fatalf("synced digest=%s err=%v", installedDigest, err)
	}
}

func TestUnpackAcceptsLegacyRegularTarEntries(t *testing.T) {
	descriptor, content := fixture(t)
	artifact, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	legacy := append([]byte(nil), artifact...)
	for offset := 0; offset+512 <= len(legacy); {
		if legacy[offset] == 0 {
			break
		}
		size, err := strconv.ParseInt(strings.Trim(string(legacy[offset+124:offset+136]), " \x00"), 8, 64)
		if err != nil {
			t.Fatal(err)
		}
		legacy[offset+156] = 0
		for i := offset + 148; i < offset+156; i++ {
			legacy[i] = ' '
		}
		checksum := 0
		for _, b := range legacy[offset : offset+512] {
			checksum += int(b)
		}
		copy(legacy[offset+148:offset+156], fmt.Sprintf("%06o\x00 ", checksum))
		offset += 512 + int((size+511)/512)*512
	}
	decoded, unpacked, err := Unpack(legacy, Limits{})
	if err != nil || decoded.Source != descriptor.Source {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if digest, err := skillsync.DigestWithExecutables(unpacked, decoded.ExecutablePaths); err != nil || digest != descriptor.Source.Digest {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
}

func TestIntrinsicExecutableModeIsPublishedInArchiveAndEmbedMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha", "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha", "run"), []byte("#!/bin/sh\nprintf snapshot-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := os.DirFS(root)
	digest, err := skillsync.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: skillsync.Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: strings.Repeat("b", 40), Version: "1.2.3", Digest: digest}}
	raw, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	decoded, unpacked, err := Unpack(raw, Limits{})
	if err != nil || len(decoded.ExecutablePaths) != 1 || decoded.ExecutablePaths[0] != "alpha/run" {
		t.Fatalf("descriptor=%#v err=%v", decoded, err)
	}
	if got, err := skillsync.DigestWithExecutables(unpacked, decoded.ExecutablePaths); err != nil || got != digest {
		t.Fatalf("digest=%s err=%v", got, err)
	}
	embed := filepath.Join(t.TempDir(), "embed")
	if err := WriteEmbedDirectory(embed, descriptor, content); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(embed, contentPrefix, "alpha", "run")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	metadata, err := os.ReadFile(filepath.Join(embed, descriptorName))
	if err != nil {
		t.Fatal(err)
	}
	var embeddedDescriptor skillsync.BundleDescriptor
	if err := json.Unmarshal(metadata, &embeddedDescriptor); err != nil {
		t.Fatal(err)
	}
	// embed.FS drops executable bits; exercise the published metadata with
	// read-only content, then execute the installed script itself.
	embedded := fstest.MapFS{}
	if err := fs.WalkDir(os.DirFS(filepath.Join(embed, "content")), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(filepath.Join(embed, "content", filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		embedded[name] = &fstest.MapFile{Data: data, Mode: 0o444}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bundle, err := skillsync.EmbeddedBundle(embeddedDescriptor, embedded)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if _, err := skillsync.Sync(context.Background(), skillsync.Config{CLI: skillsync.Identity{Publisher: "strongo", Name: "tool"}, CurrentVersion: "1.2.3", Bundles: []skillsync.Bundle{bundle}}, skillsync.Options{Dir: target}); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(filepath.Join(target, "alpha", "run")).Output(); err != nil || string(output) != "snapshot-ok" {
		t.Fatalf("installed executable output=%q err=%v", output, err)
	}
}

func TestDescriptorJSONBindsTheSameNormalizedContentAsArchive(t *testing.T) {
	descriptor, content := fixture(t)
	raw, err := DescriptorJSON(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	var published skillsync.BundleDescriptor
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatal(err)
	}
	archive, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := Unpack(archive, Limits{})
	if err != nil || !bytes.Equal(mustJSON(t, decoded), mustJSON(t, published)) {
		t.Fatalf("companion descriptor differs from archive: %v", err)
	}
	descriptor.ExecutablePaths = []string{"missing"}
	if _, err := DescriptorJSON(descriptor, content); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("missing executable error=%v", err)
	}
}

func TestWriteEmbedDirectoryRequiresFreshNonSymlinkDestination(t *testing.T) {
	descriptor, content := fixture(t)
	parent := t.TempDir()
	stale := filepath.Join(parent, "stale")
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "leftover"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteEmbedDirectory(stale, descriptor, content); err == nil {
		t.Fatal("expected stale destination rejection")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteEmbedDirectory(link, descriptor, content); err == nil {
		t.Fatal("expected symlink destination rejection")
	}
	if err := WriteEmbedDirectory(filepath.Join(parent, "new"), descriptor, content); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackRejectsUnsafeAndOversizedInput(t *testing.T) {
	for _, name := range []string{"../escape", "content/../escape", "content\\escape"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			tw := tar.NewWriter(&out)
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Unpack(out.Bytes(), Limits{}); err == nil {
				t.Fatal("expected unsafe archive rejection")
			}
		})
	}
	descriptor, content := fixture(t)
	artifact, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Unpack(artifact, Limits{MaxBytes: 4}); err == nil {
		t.Fatal("expected size limit rejection")
	}
}

func TestUnpackRejectsDescriptorDigestMismatch(t *testing.T) {
	descriptor, content := fixture(t)
	artifact, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	artifact[len(artifact)-1] ^= 1
	if _, _, err := Unpack(artifact, Limits{}); err == nil {
		t.Fatal("expected corrupt archive rejection")
	}
}

func TestArchiveBoundaryFailuresAreRejected(t *testing.T) {
	descriptor, content := fixture(t)
	raw, err := Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		entries []tarEntry
		limits  Limits
	}{
		{"missing descriptor", []tarEntry{{name: "content/alpha/SKILL.md", data: []byte("x")}}, Limits{}},
		{"duplicate entry", []tarEntry{{name: descriptorName, data: mustJSON(t, descriptor)}, {name: descriptorName, data: mustJSON(t, descriptor)}}, Limits{}},
		{"file before child", []tarEntry{{name: "content/alpha", data: []byte("hidden")}, {name: "content/alpha/SKILL.md", data: []byte("skill")}}, Limits{}},
		{"child before file", []tarEntry{{name: "content/alpha/SKILL.md", data: []byte("skill")}, {name: "content/alpha", data: []byte("hidden")}}, Limits{}},
		{"unknown entry", []tarEntry{{name: descriptorName, data: mustJSON(t, descriptor)}, {name: "other", data: []byte("x")}}, Limits{}},
		{"symlink", []tarEntry{{name: descriptorName, data: mustJSON(t, descriptor)}, {name: "content/alpha/SKILL.md", typeflag: tar.TypeSymlink, linkname: "outside"}}, Limits{}},
		{"link name", []tarEntry{{name: descriptorName, data: mustJSON(t, descriptor)}, {name: "content/alpha/SKILL.md", data: []byte("x"), linkname: "outside"}}, Limits{}},
		{"file count", []tarEntry{{name: descriptorName, data: mustJSON(t, descriptor)}, {name: "content/alpha/SKILL.md", data: []byte("x")}}, Limits{MaxFiles: 1}},
		{"trailing descriptor json", []tarEntry{{name: descriptorName, data: append(mustJSON(t, descriptor), []byte(" {}")...)}}, Limits{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Unpack(tarBytes(t, tc.entries...), tc.limits); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if _, _, err := Unpack(tarBytes(t, tarEntry{name: descriptorName, data: []byte("{")}), Limits{}); err == nil {
		t.Fatal("expected malformed descriptor rejection")
	}
	if _, _, err := Unpack(tarBytes(t,
		tarEntry{name: descriptorName, data: mustJSON(t, descriptor)},
		tarEntry{name: "content/alpha/SKILL.md", data: []byte("altered")},
		tarEntry{name: "content/alpha/run", data: []byte("#!/bin/sh\n")},
		tarEntry{name: "content/beta/SKILL.md", data: []byte("beta")},
	), Limits{}); err == nil {
		t.Fatal("expected descriptor content mismatch rejection")
	}
	truncated := raw[:600]
	if _, _, err := Unpack(truncated, Limits{}); err == nil {
		t.Fatal("expected truncated archive rejection")
	}
}

func TestSourceAndEmbedDirectoryFailures(t *testing.T) {
	if _, err := sourceFiles(fstest.MapFS{"link": &fstest.MapFile{Mode: fs.ModeSymlink}}); err == nil {
		t.Fatal("expected symlink source rejection")
	}
	if _, err := sourceFiles(errorFS{}); err == nil {
		t.Fatal("expected read failure")
	}
	if _, err := sourceFiles(readFileErrorFS{FS: fstest.MapFS{"file": &fstest.MapFile{Data: []byte("x")}}}); err == nil {
		t.Fatal("expected file read failure")
	}
	descriptor, content := fixture(t)
	if _, err := Pack(skillsync.BundleDescriptor{}, content); err == nil {
		t.Fatal("expected descriptor validation")
	}
	if err := WriteEmbedDirectory(t.TempDir()+"/missing/child", skillsync.BundleDescriptor{}, content); err == nil {
		t.Fatal("expected descriptor validation")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteEmbedDirectory(file, descriptor, content); err == nil {
		t.Fatal("expected directory creation failure")
	}
	stable := &failAfterFS{FS: content, failAfter: 100}
	if _, err := normalizedDescriptor(descriptor, stable); err != nil {
		t.Fatal(err)
	}
	changed := &failAfterFS{FS: content, failAfter: stable.opens}
	if _, err := Pack(descriptor, changed); err == nil {
		t.Fatal("expected source mutation failure after validation")
	}
	changed = &failAfterFS{FS: content, failAfter: stable.opens}
	dir := filepath.Join(t.TempDir(), "embed")
	if err := WriteEmbedDirectory(dir, descriptor, changed); err == nil {
		t.Fatal("expected source mutation failure before embed output")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source failure created output: %v", err)
	}
}

func TestEmbedOutputFailuresAfterFreshDirectoryCreation(t *testing.T) {
	descriptor, content := fixture(t)
	for _, phase := range []string{"descriptor", "content-parent", "content-file"} {
		t.Run(phase, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "embed")
			failure := errors.New("injected " + phase + " write failure")
			reached := false
			output := embedOutput{mkdir: os.Mkdir, mkdirAll: os.MkdirAll, writeFile: os.WriteFile}
			if phase == "content-parent" {
				output.mkdirAll = func(string, fs.FileMode) error { reached = true; return failure }
			} else {
				output.writeFile = func(name string, data []byte, mode fs.FileMode) error {
					if (name == filepath.Join(dir, descriptorName)) == (phase == "descriptor") {
						reached = true
						return failure
					}
					return os.WriteFile(name, data, mode)
				}
			}
			if err := writeEmbedDirectory(dir, descriptor, content, output); !errors.Is(err, failure) || !reached {
				t.Fatalf("failure phase reached=%v, error=%v", reached, err)
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("failure occurred before fresh directory creation: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "content", "alpha", "SKILL.md")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("unexpected publication after output failure: %v", err)
			}
		})
	}
}

func TestWriteEntryReportsWriterFailures(t *testing.T) {
	if err := writeEntry(tar.NewWriter(alwaysFailWriter{}), "entry", 0o644, []byte("x")); err == nil {
		t.Fatal("expected header write failure")
	}
	if err := writeEntry(tar.NewWriter(&failAfterWriter{remaining: 512}), "entry", 0o644, []byte("x")); err == nil {
		t.Fatal("expected body write failure")
	}
}

func TestWriteReportsEachArchiveOutputFailure(t *testing.T) {
	descriptor, content := fixture(t)
	count := &countWriter{}
	if err := Write(count, descriptor, content); err != nil {
		t.Fatal(err)
	}
	for call := 1; call <= count.calls; call++ {
		t.Run("call-"+strconv.Itoa(call), func(t *testing.T) {
			if err := Write(&failOnCallWriter{call: call}, descriptor, content); err == nil {
				t.Fatalf("expected writer failure at call %d", call)
			}
		})
	}
}

type tarEntry struct {
	name, linkname string
	data           []byte
	typeflag       byte
}

func tarBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Linkname: entry.linkname, Typeflag: typeflag, Mode: 0o644, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type errorFS struct{}

func (errorFS) Open(string) (fs.File, error) { return nil, errors.New("unavailable") }

type readFileErrorFS struct{ fs.FS }

func (readFileErrorFS) ReadFile(string) ([]byte, error) { return nil, errors.New("unavailable") }

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type failAfterWriter struct{ remaining int }

func (w *failAfterWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, errors.New("write failed")
	}
	w.remaining -= len(data)
	return len(data), nil
}

type countWriter struct{ calls int }

func (w *countWriter) Write(data []byte) (int, error) {
	w.calls++
	return len(data), nil
}

type failOnCallWriter struct {
	call, calls int
}

func (w *failOnCallWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.calls == w.call {
		return 0, errors.New("write failed")
	}
	return len(data), nil
}

type failAfterFS struct {
	fs.FS
	failAfter, opens int
}

func (f *failAfterFS) Open(name string) (fs.File, error) {
	f.opens++
	if f.opens > f.failAfter {
		return nil, errors.New("source changed")
	}
	return f.FS.Open(name)
}
