// Package producer builds one immutable skillsync release snapshot from an
// already-checked-out local Git repository. It never fetches or downloads: CI
// resolves branches and tags before invoking it, then this package reads the
// exact full commit named by the descriptor.
package producer

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/snapshot"
)

// Config names the committed plugin tree and a fresh destination directory.
// Descriptor.Source.Revision must be a full immutable SHA; Digest is optional
// and, when supplied, must match the captured committed bytes.
type Config struct {
	Descriptor    skillsync.BundleDescriptor
	RepositoryDir string
	OutputDir     string
}

// Result identifies the canonical files published into OutputDir.
type Result struct {
	Descriptor     skillsync.BundleDescriptor
	ArchivePath    string
	DescriptorPath string
	EmbedDir       string
}

type gitRunner interface {
	run(string, int64, ...string) ([]byte, error)
}

type systemGit struct{}

func (systemGit) run(dir string, limit int64, args ...string) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("git output limit must be positive")
	}
	command := exec.Command("git", append([]string{"--no-replace-objects", "-C", dir}, args...)...)
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: 4096, truncate: true}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	data     []byte
	limit    int64
	truncate bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - int64(len(b.data))
	if int64(len(data)) <= remaining {
		b.data = append(b.data, data...)
		return len(data), nil
	}
	if remaining > 0 {
		b.data = append(b.data, data[:remaining]...)
	}
	if b.truncate {
		return len(data), nil
	}
	return 0, fmt.Errorf("git output exceeds %d-byte bound", b.limit)
}

func (b *limitedBuffer) Bytes() []byte  { return b.data }
func (b *limitedBuffer) String() string { return string(b.data) }

type outputFS struct {
	lstat      func(string) (fs.FileInfo, error)
	mkdirTmp   func(string, string) (string, error)
	write      func(string, []byte, fs.FileMode) error
	writeEmbed func(snapshot.Captured, string, skillsync.BundleDescriptor) error
	rename     func(string, string) error
	remove     func(string) error
}

type snapshotOps struct {
	capture    func(fs.FS) (snapshot.Captured, error)
	pack       func(snapshot.Captured, skillsync.BundleDescriptor) ([]byte, error)
	validate   func([]byte) error
	descriptor func(snapshot.Captured, skillsync.BundleDescriptor) ([]byte, error)
}

func defaultSnapshotOps() snapshotOps {
	return snapshotOps{capture: snapshot.Capture, pack: func(c snapshot.Captured, d skillsync.BundleDescriptor) ([]byte, error) { return c.Pack(d) }, validate: func(archive []byte) error { _, _, err := snapshot.Unpack(archive, snapshot.Limits{}); return err }, descriptor: func(c snapshot.Captured, d skillsync.BundleDescriptor) ([]byte, error) { return c.DescriptorJSON(d) }}
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Produce creates skillsync-bundle.tar, skillsync-bundle.json, and
// embed/{bundle.json,content/...} from the same verified captured tree. The
// output path must not already exist. On any failure it returns no Result and
// removes its unpublished staging directory.
func Produce(config Config) (Result, error) {
	return produce(config, systemGit{}, outputFS{
		lstat: os.Lstat, mkdirTmp: os.MkdirTemp, write: os.WriteFile, writeEmbed: func(c snapshot.Captured, dir string, d skillsync.BundleDescriptor) error {
			return c.WriteEmbedDirectory(dir, d)
		}, rename: os.Rename, remove: os.RemoveAll,
	})
}

func produce(config Config, git gitRunner, output outputFS) (Result, error) {
	return produceWith(config, git, output, defaultSnapshotOps())
}

func produceWith(config Config, git gitRunner, output outputFS, snapshots snapshotOps) (Result, error) {
	if err := validateInput(config); err != nil {
		return Result{}, err
	}
	repository := filepath.Clean(config.RepositoryDir)
	if err := verifyRepository(git, repository, config.Descriptor.Source); err != nil {
		return Result{}, err
	}
	tree, err := readCommittedTree(git, repository, config.Descriptor.Source)
	if err != nil {
		return Result{}, err
	}
	captured, err := snapshots.capture(tree)
	if err != nil {
		return Result{}, fmt.Errorf("capture committed tree: %w", err)
	}
	descriptor, err := bindDescriptor(captured, config.Descriptor)
	if err != nil {
		return Result{}, err
	}
	archive, err := snapshots.pack(captured, descriptor)
	if err != nil {
		return Result{}, fmt.Errorf("pack snapshot: %w", err)
	}
	if err := snapshots.validate(archive); err != nil {
		return Result{}, fmt.Errorf("validate bounded snapshot: %w", err)
	}
	rawDescriptor, err := snapshots.descriptor(captured, descriptor)
	if err != nil {
		return Result{}, fmt.Errorf("encode descriptor: %w", err)
	}
	return publish(config.OutputDir, descriptor, archive, rawDescriptor, captured, output)
}

func validateInput(config Config) error {
	if config.RepositoryDir == "" || config.OutputDir == "" {
		return fmt.Errorf("%w: repository and output directories are required", skillsync.ErrInvalidConfig)
	}
	source := config.Descriptor.Source
	if source.Repository == "" || !fullSHA.MatchString(source.Revision) || !fs.ValidPath(source.Path) {
		return fmt.Errorf("%w: repository, full lowercase revision, and safe source path are required", skillsync.ErrInvalidConfig)
	}
	return nil
}

func verifyRepository(git gitRunner, repository string, source skillsync.Source) error {
	inside, err := git.run(repository, 1024, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return fmt.Errorf("%w: source repository is not a Git work tree", skillsync.ErrInvalidConfig)
	}
	resolved, err := git.run(repository, 1024, "rev-parse", source.Revision+"^{commit}")
	if err != nil || strings.TrimSpace(string(resolved)) != source.Revision {
		return fmt.Errorf("%w: source revision %q is not an available exact commit", skillsync.ErrInvalidConfig, source.Revision)
	}
	// An origin is common in CI. When it is present, reject descriptor
	// repository metadata that disagrees with it. Repositories intentionally
	// created without an origin remain useful offline test/build inputs.
	if origin, err := git.run(repository, 4096, "remote", "get-url", "origin"); err == nil && strings.TrimSpace(string(origin)) != "" && canonicalRepository(string(origin)) != source.Repository {
		return fmt.Errorf("%w: descriptor repository %q disagrees with origin %q", skillsync.ErrInvalidConfig, source.Repository, strings.TrimSpace(string(origin)))
	}
	return nil
}

func canonicalRepository(origin string) string {
	origin = strings.TrimSpace(strings.TrimSuffix(origin, "/"))
	origin = strings.TrimSuffix(origin, ".git")
	if strings.HasPrefix(origin, "git@") {
		return strings.Replace(strings.TrimPrefix(origin, "git@"), ":", "/", 1)
	}
	for _, prefix := range []string{"https://", "http://", "ssh://"} {
		origin = strings.TrimPrefix(origin, prefix)
	}
	return strings.TrimPrefix(origin, "git@")
}

func readCommittedTree(git gitRunner, repository string, source skillsync.Source) (fstest.MapFS, error) {
	treeRef := source.Revision + ":" + source.Path
	if source.Path == "." {
		treeRef = source.Revision + "^{tree}"
	}
	typeName, err := git.run(repository, 1024, "cat-file", "-t", treeRef)
	if err != nil || strings.TrimSpace(string(typeName)) != "tree" {
		return nil, fmt.Errorf("%w: source path %q is not a committed tree", skillsync.ErrInvalidConfig, source.Path)
	}
	raw, err := git.run(repository, snapshot.DefaultMaxBytes, "ls-tree", "-r", "-z", "--full-tree", source.Revision, "--", source.Path)
	if err != nil {
		return nil, fmt.Errorf("read committed source tree: %w", err)
	}
	result := fstest.MapFS{}
	var total int64
	prefix := ""
	if source.Path != "." {
		prefix = source.Path + "/"
	}
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(result) >= snapshot.DefaultMaxFiles-1 {
			return nil, fmt.Errorf("%w: committed tree exceeds %d content files", skillsync.ErrInvalidConfig, snapshot.DefaultMaxFiles-1)
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%w: malformed Git tree entry", skillsync.ErrInvalidConfig)
		}
		metadata := strings.Fields(string(fields[0]))
		if len(metadata) != 3 || metadata[1] != "blob" || !fullSHA.MatchString(metadata[2]) {
			return nil, fmt.Errorf("%w: unsafe Git tree entry", skillsync.ErrInvalidConfig)
		}
		mode, err := strconv.ParseUint(metadata[0], 8, 32)
		if err != nil || mode != 0o100644 && mode != 0o100755 {
			return nil, fmt.Errorf("%w: non-regular committed entry", skillsync.ErrInvalidConfig)
		}
		name := string(fields[1])
		if !strings.HasPrefix(name, prefix) {
			return nil, fmt.Errorf("%w: source tree path escaped declared prefix", skillsync.ErrInvalidConfig)
		}
		name = strings.TrimPrefix(name, prefix)
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("%w: unsafe committed path %q", skillsync.ErrInvalidConfig, name)
		}
		remaining := snapshot.DefaultMaxBytes - total
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: committed tree exceeds %d bytes", skillsync.ErrInvalidConfig, snapshot.DefaultMaxBytes)
		}
		data, err := git.run(repository, remaining, "cat-file", "blob", metadata[2])
		if err != nil {
			return nil, fmt.Errorf("read committed blob %q: %w", name, err)
		}
		total += int64(len(data))
		result[name] = &fstest.MapFile{Data: data, Mode: fs.FileMode(mode & 0o777)}
	}
	return result, nil
}

func bindDescriptor(captured snapshot.Captured, descriptor skillsync.BundleDescriptor) (skillsync.BundleDescriptor, error) {
	digest, err := captured.Digest(descriptor.ExecutablePaths)
	if err != nil {
		return skillsync.BundleDescriptor{}, fmt.Errorf("digest captured source: %w", err)
	}
	if descriptor.Source.Digest != "" && descriptor.Source.Digest != digest {
		return skillsync.BundleDescriptor{}, fmt.Errorf("%w: declared digest does not match committed source", skillsync.ErrDigestMismatch)
	}
	descriptor.Source.Digest = digest
	descriptor, err = captured.NormalizeDescriptor(descriptor)
	if err != nil {
		return skillsync.BundleDescriptor{}, err
	}
	return descriptor, nil
}

func publish(outputDir string, descriptor skillsync.BundleDescriptor, archive, rawDescriptor []byte, captured snapshot.Captured, output outputFS) (Result, error) {
	abs := filepath.Clean(outputDir)
	if _, err := output.lstat(abs); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return Result{}, fmt.Errorf("%w: output directory already exists", skillsync.ErrInvalidConfig)
		}
		return Result{}, fmt.Errorf("inspect output directory: %w", err)
	}
	stage, err := output.mkdirTmp(filepath.Dir(abs), "."+filepath.Base(abs)+".tmp-")
	if err != nil {
		return Result{}, fmt.Errorf("create output staging directory: %w", err)
	}
	fail := func(err error) (Result, error) {
		if removeErr := output.remove(stage); removeErr != nil {
			return Result{}, fmt.Errorf("%w (staging cleanup: %v)", err, removeErr)
		}
		return Result{}, err
	}
	if err := output.write(filepath.Join(stage, snapshot.DefaultAssetName), archive, 0o644); err != nil {
		return fail(fmt.Errorf("write archive: %w", err))
	}
	if err := output.write(filepath.Join(stage, snapshot.DefaultDescriptorAssetName), append(rawDescriptor, '\n'), 0o644); err != nil {
		return fail(fmt.Errorf("write companion descriptor: %w", err))
	}
	if err := output.writeEmbed(captured, filepath.Join(stage, "embed"), descriptor); err != nil {
		return fail(fmt.Errorf("write embed directory: %w", err))
	}
	if _, err := output.lstat(abs); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return fail(fmt.Errorf("%w: output directory appeared during publication", skillsync.ErrInvalidConfig))
		}
		return fail(fmt.Errorf("reinspect output directory: %w", err))
	}
	if err := output.rename(stage, abs); err != nil {
		return fail(fmt.Errorf("publish output directory: %w", err))
	}
	return Result{Descriptor: descriptor, ArchivePath: filepath.Join(abs, snapshot.DefaultAssetName), DescriptorPath: filepath.Join(abs, snapshot.DefaultDescriptorAssetName), EmbedDir: filepath.Join(abs, "embed")}, nil
}
