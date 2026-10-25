package producer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/snapshot"
)

func TestProduceReadsPinnedCommitAndPublishesOneSnapshot(t *testing.T) {
	repo, first, second := testRepository(t)
	config := Config{RepositoryDir: repo, OutputDir: filepath.Join(t.TempDir(), "out"), Descriptor: descriptor(first)}
	result, err := Produce(config)
	if err != nil {
		t.Fatal(err)
	}
	if result.Descriptor.Source.Digest == "" || result.Descriptor.Source.Revision != first || len(result.Descriptor.ExecutablePaths) != 1 || result.Descriptor.ExecutablePaths[0] != "demo/run" {
		t.Fatalf("descriptor = %#v", result.Descriptor)
	}
	archive, err := os.ReadFile(result.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, packed, err := snapshot.Unpack(archive, snapshot.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, result.Descriptor) {
		t.Fatalf("archive descriptor = %#v, want %#v", decoded, result.Descriptor)
	}
	if got, err := fs.ReadFile(packed, "demo/SKILL.md"); err != nil || string(got) != "first" {
		t.Fatalf("pinned content = %q, err=%v", got, err)
	}
	if got, err := fs.ReadFile(packed, ".plugin-resource"); err != nil || string(got) != "dotfile" {
		t.Fatalf("dotfile = %q, err=%v", got, err)
	}
	if info, err := fs.Stat(packed, "demo/run"); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("packed mode = %v, err=%v", info.Mode(), err)
	}
	embedded := os.DirFS(filepath.Join(result.EmbedDir, "content"))
	if digest, err := skillsync.DigestWithExecutables(embedded, decoded.ExecutablePaths); err != nil || digest != decoded.Source.Digest {
		t.Fatalf("embed digest=%q err=%v", digest, err)
	}
	if companion, err := os.ReadFile(result.DescriptorPath); err != nil || !bytes.Equal(bytes.TrimSpace(companion), mustJSON(t, decoded)) {
		t.Fatalf("companion=%q err=%v", companion, err)
	}
	bundle, err := skillsync.EmbeddedBundle(decoded, packed)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if _, err := skillsync.Sync(context.Background(), skillsync.Config{CLI: skillsync.Identity{Publisher: "example", Name: "tool"}, CurrentVersion: "1.0.0", Bundles: []skillsync.Bundle{bundle}}, skillsync.Options{Dir: target}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "demo", "SKILL.md")); err != nil || string(got) != "first" {
		t.Fatalf("installed content=%q err=%v", got, err)
	}
	if info, err := os.Stat(filepath.Join(target, "demo", "run")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode=%v err=%v", info.Mode(), err)
	}

	secondConfig := config
	secondConfig.OutputDir = filepath.Join(t.TempDir(), "out")
	secondConfig.Descriptor = descriptor(second)
	secondResult, err := Produce(secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	secondArchive, err := os.ReadFile(secondResult.ArchivePath)
	if err != nil || bytes.Equal(archive, secondArchive) {
		t.Fatalf("second archive differs=%v err=%v", !bytes.Equal(archive, secondArchive), err)
	}

	repeated := config
	repeated.OutputDir = filepath.Join(t.TempDir(), "out")
	repeatedResult, err := Produce(repeated)
	if err != nil {
		t.Fatal(err)
	}
	repeatedArchive, err := os.ReadFile(repeatedResult.ArchivePath)
	if err != nil || !bytes.Equal(archive, repeatedArchive) {
		t.Fatalf("reproducible=%v err=%v", bytes.Equal(archive, repeatedArchive), err)
	}
}

func TestProduceIgnoresLocalReplacementObjects(t *testing.T) {
	repo, original, replacement := testRepository(t)
	gitCommand(t, repo, "replace", original, replacement)
	result, err := Produce(Config{RepositoryDir: repo, OutputDir: filepath.Join(t.TempDir(), "out"), Descriptor: descriptor(original)})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(result.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	_, content, err := snapshot.Unpack(archive, snapshot.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := fs.ReadFile(content, "demo/SKILL.md"); err != nil || string(body) != "first" {
		t.Fatalf("descriptor revision %s published %q from replacement %s: %v", original, body, replacement, err)
	}
}

func TestProduceRejectsConsumerBoundedInputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files int
		bytes int
	}{
		{"file count", snapshot.DefaultMaxFiles, 0},
		{"archive bytes", 0, int(snapshot.DefaultMaxBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitCommand(t, repo, "init")
			gitCommand(t, repo, "config", "user.email", "test@example.com")
			gitCommand(t, repo, "config", "user.name", "Test")
			gitCommand(t, repo, "remote", "add", "origin", "https://github.com/example/plugin.git")
			root := filepath.Join(repo, "skills", "demo")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("skill"), 0o644); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.files; i++ {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("resource-%03d", i)), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bytes > 0 {
				if err := os.WriteFile(filepath.Join(root, "large"), bytes.Repeat([]byte{'x'}, tc.bytes), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gitCommand(t, repo, "add", ".")
			gitCommand(t, repo, "commit", "-m", tc.name)
			revision := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
			out := filepath.Join(t.TempDir(), "out")
			if _, err := Produce(Config{RepositoryDir: repo, OutputDir: out, Descriptor: descriptor(revision)}); err == nil {
				t.Fatal("expected bounded input rejection")
			}
			if _, err := os.Lstat(out); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("output exists after rejected input: %v", err)
			}
		})
	}
}

func TestBoundedGitOutputAndBuffers(t *testing.T) {
	repo, _, _ := testRepository(t)
	if _, err := (systemGit{}).run(repo, 0, "rev-parse", "HEAD"); err == nil {
		t.Fatal("expected non-positive Git bound rejection")
	}
	if _, err := (systemGit{}).run(repo, 1, "rev-parse", "HEAD"); err == nil {
		t.Fatal("expected stdout bound rejection")
	}
	if _, err := (systemGit{}).run(repo, 1024, "cat-file", "-t", "missing"); err == nil {
		t.Fatal("expected Git stderr failure")
	}
	for _, tc := range []struct {
		name     string
		buffer   limitedBuffer
		input    string
		wantErr  bool
		wantBody string
	}{
		{"within", limitedBuffer{limit: 3}, "abc", false, "abc"},
		{"bounded", limitedBuffer{limit: 2}, "abc", true, "ab"},
		{"truncated", limitedBuffer{limit: 2, truncate: true}, "abc", false, "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.buffer.Write([]byte(tc.input))
			if (err != nil) != tc.wantErr || tc.buffer.String() != tc.wantBody {
				t.Fatalf("err=%v body=%q", err, tc.buffer.String())
			}
		})
	}
}

func TestProduceRejectsUnsafeAndStaleMetadataWithoutOutput(t *testing.T) {
	repo, revision, _ := testRepository(t)
	captured, err := snapshot.Capture(fstest.MapFS{"skill/SKILL.md": &fstest.MapFile{Data: []byte("skill")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"short revision", func(c *Config) { c.Descriptor.Source.Revision = revision[:12] }},
		{"unsafe source path", func(c *Config) { c.Descriptor.Source.Path = "../skills" }},
		{"origin mismatch", func(c *Config) { c.Descriptor.Source.Repository = "github.com/other/plugin" }},
		{"stale digest", func(c *Config) { c.Descriptor.Source.Digest = strings.Repeat("0", 64) }},
		{"missing committed path", func(c *Config) { c.Descriptor.Source.Path = "missing" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := Config{RepositoryDir: repo, OutputDir: filepath.Join(t.TempDir(), "out"), Descriptor: descriptor(revision)}
			tc.mutate(&config)
			if _, err := Produce(config); err == nil {
				t.Fatal("expected rejection")
			}
			if _, err := os.Lstat(config.OutputDir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("failed publication left output: %v", err)
			}
		})
	}
	config := Config{RepositoryDir: repo, OutputDir: filepath.Join(t.TempDir(), "out"), Descriptor: descriptor(revision)}
	if err := os.Mkdir(config.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Produce(config); err == nil {
		t.Fatal("expected existing output rejection")
	}
	for _, tc := range []struct {
		name   string
		output func() outputFS
	}{
		{"initial inspect", func() outputFS {
			o := normalOutput()
			o.lstat = func(string) (fs.FileInfo, error) { return nil, os.ErrPermission }
			return o
		}},
		{"companion", func() outputFS {
			o := normalOutput()
			o.write = func(name string, _ []byte, _ fs.FileMode) error {
				if strings.HasSuffix(name, snapshot.DefaultDescriptorAssetName) {
					return errors.New("descriptor")
				}
				return os.WriteFile(name, []byte("archive"), 0o644)
			}
			return o
		}},
		{"cleanup", func() outputFS {
			o := normalOutput()
			o.write = func(string, []byte, fs.FileMode) error { return errors.New("archive") }
			o.remove = func(string) error { return errors.New("cleanup") }
			return o
		}},
		{"appeared", func() outputFS {
			o := normalOutput()
			calls := 0
			existing, _ := os.Stat(t.TempDir())
			o.lstat = func(name string) (fs.FileInfo, error) {
				calls++
				if calls == 2 {
					return existing, nil
				}
				return os.Lstat(name)
			}
			return o
		}},
		{"reinspect", func() outputFS {
			o := normalOutput()
			calls := 0
			o.lstat = func(name string) (fs.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, os.ErrPermission
				}
				return os.Lstat(name)
			}
			return o
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := publish(filepath.Join(t.TempDir(), "out"), descriptorForCaptured(t, captured), []byte("archive"), []byte("{}"), captured, tc.output()); err == nil {
				t.Fatal("expected publication boundary failure")
			}
		})
	}
}

func TestProducerPrivateFailureSeams(t *testing.T) {
	config := Config{RepositoryDir: ".", OutputDir: filepath.Join(t.TempDir(), "out"), Descriptor: descriptor(strings.Repeat("a", 40))}
	if err := validateInput(Config{}); err == nil {
		t.Fatal("expected empty input rejection")
	}
	if got := canonicalRepository("git@github.com:example/plugin.git"); got != "github.com/example/plugin" {
		t.Fatalf("canonical SSH = %q", got)
	}
	if got := canonicalRepository("ssh://git@github.com/example/plugin.git/"); got != "github.com/example/plugin" {
		t.Fatalf("canonical SSH URL = %q", got)
	}
	if err := verifyRepository(fakeGit{responses: map[string]response{"rev-parse --is-inside-work-tree": {out: "false\n"}}}, ".", config.Descriptor.Source); err == nil {
		t.Fatal("expected non-repository rejection")
	}
	fake := fakeGit{responses: map[string]response{
		"rev-parse --is-inside-work-tree":                                              {out: "true\n"},
		"rev-parse aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa^{commit}":                  {out: strings.Repeat("a", 40) + "\n"},
		"remote get-url origin":                                                        {out: "https://github.com/example/plugin.git\n"},
		"cat-file -t aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:skills":                  {out: "tree\n"},
		"ls-tree -r -z --full-tree aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -- skills": {out: "100644 blob " + strings.Repeat("b", 40) + "\tskills/skill/SKILL.md\x00"},
		"cat-file blob " + strings.Repeat("b", 40):                                     {out: "skill"},
	}}
	tree, err := readCommittedTree(fake, ".", config.Descriptor.Source)
	if err != nil || string(tree["skill/SKILL.md"].Data) != "skill" {
		t.Fatalf("tree=%#v err=%v", tree, err)
	}
	root := fake.clone()
	root.responses["cat-file -t aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa^{tree}"] = response{out: "tree\n"}
	root.responses["ls-tree -r -z --full-tree aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -- ."] = response{out: "100644 blob " + strings.Repeat("b", 40) + "\tskill/SKILL.md\x00"}
	rootSource := config.Descriptor.Source
	rootSource.Path = "."
	if tree, err := readCommittedTree(root, ".", rootSource); err != nil || string(tree["skill/SKILL.md"].Data) != "skill" {
		t.Fatalf("root tree=%#v err=%v", tree, err)
	}
	wrongRevision := fake.clone()
	wrongRevision.responses["rev-parse aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa^{commit}"] = response{out: strings.Repeat("b", 40)}
	if err := verifyRepository(wrongRevision, ".", config.Descriptor.Source); err == nil {
		t.Fatal("expected revision verification rejection")
	}
	bad := fake.clone()
	bad.responses["ls-tree -r -z --full-tree aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -- skills"] = response{out: "120000 blob " + strings.Repeat("b", 40) + "\tlink\x00"}
	if _, err := readCommittedTree(bad, ".", config.Descriptor.Source); err == nil {
		t.Fatal("expected special entry rejection")
	}
	for _, key := range []string{"cat-file -t aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:skills", "ls-tree -r -z --full-tree aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -- skills"} {
		broken := fake.clone()
		broken.responses[key] = response{err: errors.New("broken")}
		if _, err := readCommittedTree(broken, ".", config.Descriptor.Source); err == nil {
			t.Fatalf("expected %s failure", key)
		}
	}
	for _, raw := range []string{"malformed\x00", "100644 tree " + strings.Repeat("b", 40) + "\tskills/file\x00", "100600 blob " + strings.Repeat("b", 40) + "\tskills/file\x00", "100644 blob " + strings.Repeat("b", 40) + "\tother/file\x00", "100644 blob " + strings.Repeat("b", 40) + "\tskills/../file\x00"} {
		broken := fake.clone()
		broken.responses["ls-tree -r -z --full-tree aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -- skills"] = response{out: raw}
		if _, err := readCommittedTree(broken, ".", config.Descriptor.Source); err == nil {
			t.Fatalf("expected unsafe tree rejection for %q", raw)
		}
	}
	broken := fake.clone()
	broken.responses["cat-file blob "+strings.Repeat("b", 40)] = response{err: errors.New("missing blob")}
	if _, err := readCommittedTree(broken, ".", config.Descriptor.Source); err == nil {
		t.Fatal("expected blob read rejection")
	}
	overLimit := fake.clone()
	firstBlob := strings.Repeat("x", int(snapshot.DefaultMaxBytes))
	overLimit.responses["ls-tree -r -z --full-tree aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -- skills"] = response{out: "100644 blob " + strings.Repeat("b", 40) + "\tskills/first\x00100644 blob " + strings.Repeat("c", 40) + "\tskills/second\x00"}
	overLimit.responses["cat-file blob "+strings.Repeat("b", 40)] = response{out: firstBlob}
	overLimit.responses["cat-file blob "+strings.Repeat("c", 40)] = response{out: "x"}
	if _, err := readCommittedTree(overLimit, ".", config.Descriptor.Source); err == nil {
		t.Fatal("expected aggregate committed-byte rejection")
	}
	captured, err := snapshot.Capture(fstest.MapFS{"skill/SKILL.md": &fstest.MapFile{Data: []byte("skill")}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := config.Descriptor
	invalid.Source.Version = "not-semver"
	if _, err := bindDescriptor(captured, invalid); err == nil {
		t.Fatal("expected invalid descriptor rejection")
	}
	mismatch := descriptorForCaptured(t, captured)
	mismatch.Source.Digest = strings.Repeat("0", 64)
	if _, err := bindDescriptor(captured, mismatch); err == nil {
		t.Fatal("expected digest mismatch")
	}
	badExecutable := config.Descriptor
	badExecutable.ExecutablePaths = []string{"missing"}
	if _, err := bindDescriptor(snapshot.Captured{}, badExecutable); err == nil {
		t.Fatal("expected captured digest failure")
	}
	for _, phase := range []string{"capture", "pack", "validate", "descriptor"} {
		t.Run("snapshot "+phase, func(t *testing.T) {
			ops := defaultSnapshotOps()
			switch phase {
			case "capture":
				ops.capture = func(fs.FS) (snapshot.Captured, error) { return snapshot.Captured{}, errors.New("capture") }
			case "pack":
				ops.pack = func(snapshot.Captured, skillsync.BundleDescriptor) ([]byte, error) { return nil, errors.New("pack") }
			case "validate":
				ops.validate = func([]byte) error { return errors.New("validate") }
			case "descriptor":
				ops.descriptor = func(snapshot.Captured, skillsync.BundleDescriptor) ([]byte, error) {
					return nil, errors.New("descriptor")
				}
			}
			if _, err := produceWith(Config{RepositoryDir: ".", OutputDir: filepath.Join(t.TempDir(), "out"), Descriptor: config.Descriptor}, fake, normalOutput(), ops); err == nil {
				t.Fatal("expected snapshot operation failure")
			}
		})
	}

	for _, tc := range []struct {
		name   string
		output outputFS
	}{
		{"stage", outputFS{lstat: os.Lstat, mkdirTmp: func(string, string) (string, error) { return "", errors.New("stage") }, write: os.WriteFile, writeEmbed: embedOK, rename: os.Rename, remove: os.RemoveAll}},
		{"archive", outputFS{lstat: os.Lstat, mkdirTmp: os.MkdirTemp, write: func(string, []byte, fs.FileMode) error { return errors.New("write") }, writeEmbed: embedOK, rename: os.Rename, remove: os.RemoveAll}},
		{"rename", outputFS{lstat: os.Lstat, mkdirTmp: os.MkdirTemp, write: os.WriteFile, writeEmbed: embedOK, rename: func(string, string) error { return errors.New("rename") }, remove: os.RemoveAll}},
		{"embed", outputFS{lstat: os.Lstat, mkdirTmp: os.MkdirTemp, write: os.WriteFile, writeEmbed: func(snapshot.Captured, string, skillsync.BundleDescriptor) error { return errors.New("embed") }, rename: os.Rename, remove: os.RemoveAll}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := publish(filepath.Join(t.TempDir(), "out"), descriptorForCaptured(t, captured), []byte("archive"), []byte("{}"), captured, tc.output); err == nil {
				t.Fatal("expected publish failure")
			}
		})
	}
}

func embedOK(captured snapshot.Captured, dir string, descriptor skillsync.BundleDescriptor) error {
	return captured.WriteEmbedDirectory(dir, descriptor)
}

func normalOutput() outputFS {
	return outputFS{lstat: os.Lstat, mkdirTmp: os.MkdirTemp, write: os.WriteFile, writeEmbed: embedOK, rename: os.Rename, remove: os.RemoveAll}
}

type response struct {
	out string
	err error
}

type fakeGit struct{ responses map[string]response }

func (g fakeGit) clone() fakeGit {
	result := fakeGit{responses: make(map[string]response, len(g.responses))}
	for key, value := range g.responses {
		result.responses[key] = value
	}
	return result
}

func (g fakeGit) run(_ string, _ int64, args ...string) ([]byte, error) {
	r, ok := g.responses[strings.Join(args, " ")]
	if !ok {
		return nil, errors.New("unexpected git call")
	}
	return []byte(r.out), r.err
}

func descriptor(revision string) skillsync.BundleDescriptor {
	return skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "example", Name: "plugin"}, Source: skillsync.Source{Repository: "github.com/example/plugin", Path: "skills", Revision: revision, Version: "1.0.0"}}
}

func descriptorForCaptured(t *testing.T, captured snapshot.Captured) skillsync.BundleDescriptor {
	t.Helper()
	d := descriptor(strings.Repeat("a", 40))
	digest, err := captured.Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Source.Digest = digest
	return d
}

func testRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	gitCommand(t, repo, "init")
	gitCommand(t, repo, "config", "user.email", "test@example.com")
	gitCommand(t, repo, "config", "user.name", "Test")
	gitCommand(t, repo, "remote", "add", "origin", "https://github.com/example/plugin.git")
	write := func(name, body string, mode fs.FileMode) {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("skills/demo/SKILL.md", "first", 0o644)
	write("skills/demo/run", "#!/bin/sh\nprintf first\n", 0o755)
	write("skills/.plugin-resource", "dotfile", 0o644)
	gitCommand(t, repo, "add", ".")
	gitCommand(t, repo, "commit", "-m", "first")
	first := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	write("skills/demo/SKILL.md", "second", 0o644)
	gitCommand(t, repo, "add", ".")
	gitCommand(t, repo, "commit", "-m", "second")
	second := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	write("skills/demo/SKILL.md", "dirty", 0o644)
	return repo, first, second
}

func gitCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, raw)
	}
	return string(raw)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
