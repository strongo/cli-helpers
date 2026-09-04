// Package snapshot encodes one verified skillsync bundle as a reproducible tar
// artifact and exposes the same descriptor/content pair for embedding.
package snapshot

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"
	"time"

	"github.com/strongo/cli-helpers/skillsync"
)

// DefaultAssetName is the stable GitHub Release asset name for packed bundles.
const DefaultAssetName = "skillsync-bundle.tar"
const DefaultDescriptorAssetName = "skillsync-bundle.json"

const descriptorName = "bundle.json"
const contentPrefix = "content/"

// Limits bounds untrusted release artifact processing.
type Limits struct {
	MaxFiles int
	MaxBytes int64
}

func (l Limits) normalized() Limits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = 256
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = 16 << 20
	}
	return l
}

// Pack emits byte-for-byte reproducible tar content. HTTPS transport and the
// descriptor digest provide integrity; this format does not claim signatures.
func Pack(descriptor skillsync.BundleDescriptor, content fs.FS) ([]byte, error) {
	var out bytes.Buffer
	if err := Write(&out, descriptor, content); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DescriptorJSON returns canonical descriptor metadata for a companion release
// asset and normalizes executable modes discovered in local source content.
func DescriptorJSON(descriptor skillsync.BundleDescriptor, content fs.FS) ([]byte, error) {
	descriptor, err := normalizedDescriptor(descriptor, content)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(descriptor)
	return raw, nil
}

// Write emits a reproducible archive to w. It is useful to release producers
// that upload an artifact without first duplicating it on disk.
func Write(w io.Writer, descriptor skillsync.BundleDescriptor, content fs.FS) error {
	var err error
	descriptor, err = normalizedDescriptor(descriptor, content)
	if err != nil {
		return err
	}
	files, err := sourceFiles(content)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(descriptor) // BundleDescriptor contains only marshalable value fields.
	tw := tar.NewWriter(w)
	if err := writeEntry(tw, descriptorName, 0o644, raw); err != nil {
		return err
	}
	for _, file := range files {
		mode := int64(0o644)
		if executable(descriptor.ExecutablePaths, file.name) {
			mode = 0o755
		}
		if err := writeEntry(tw, contentPrefix+file.name, mode, file.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return nil
}

func normalizedDescriptor(descriptor skillsync.BundleDescriptor, content fs.FS) (skillsync.BundleDescriptor, error) {
	executables, err := skillsync.NormalizeExecutablePaths(content, descriptor.ExecutablePaths)
	if err != nil {
		return skillsync.BundleDescriptor{}, err
	}
	descriptor.ExecutablePaths = executables
	if _, err := skillsync.EmbeddedBundle(descriptor, content); err != nil {
		return skillsync.BundleDescriptor{}, err
	}
	return descriptor, nil
}

type sourceFile struct {
	name string
	data []byte
}

func sourceFiles(content fs.FS) ([]sourceFile, error) {
	var files []sourceFile
	err := fs.WalkDir(content, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !validArchivePath(name) || entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("%w: unsafe source entry %q", skillsync.ErrInvalidConfig, name)
		}
		data, err := fs.ReadFile(content, name)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{name: name, data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func writeEntry(tw *tar.Writer, name string, mode int64, data []byte) error {
	head := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{}, Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(head); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// Unpack validates an artifact completely before returning a bundle.
func Unpack(artifact []byte, limits Limits) (skillsync.BundleDescriptor, fs.FS, error) {
	limits = limits.normalized()
	if int64(len(artifact)) > limits.MaxBytes {
		return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: archive exceeds size limit", skillsync.ErrInvalidConfig)
	}
	tr := tar.NewReader(bytes.NewReader(artifact))
	files := fstest.MapFS{}
	var rawDescriptor []byte
	seen := map[string]bool{}
	var count int
	var total int64
	for {
		head, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: read archive: %v", skillsync.ErrInvalidConfig, err)
		}
		count++
		if count > limits.MaxFiles || head.Size < 0 || head.Size > limits.MaxBytes-total {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: archive limits exceeded", skillsync.ErrInvalidConfig)
		}
		if (head.Typeflag != tar.TypeReg && head.Typeflag != tar.TypeRegA) || head.Linkname != "" || !validArchivePath(head.Name) || seen[head.Name] {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: unsafe archive entry %q", skillsync.ErrInvalidConfig, head.Name)
		}
		seen[head.Name] = true
		data, err := io.ReadAll(io.LimitReader(tr, head.Size+1))
		if err != nil || int64(len(data)) != head.Size {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: invalid archive entry %q", skillsync.ErrInvalidConfig, head.Name)
		}
		total += int64(len(data))
		if head.Name == descriptorName {
			rawDescriptor = data
			continue
		}
		if !strings.HasPrefix(head.Name, contentPrefix) {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: unknown archive entry %q", skillsync.ErrInvalidConfig, head.Name)
		}
		name := strings.TrimPrefix(head.Name, contentPrefix)
		for existing := range files {
			if strings.HasPrefix(name, existing+"/") || strings.HasPrefix(existing, name+"/") {
				return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: file-directory archive collision", skillsync.ErrInvalidConfig)
			}
		}
		mode := fs.FileMode(0o644)
		if head.Mode&0o111 != 0 {
			mode = 0o755
		}
		files[name] = &fstest.MapFile{Data: data, Mode: mode}
	}
	if rawDescriptor == nil {
		return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: archive descriptor missing", skillsync.ErrInvalidConfig)
	}
	var descriptor skillsync.BundleDescriptor
	decoder := json.NewDecoder(bytes.NewReader(rawDescriptor))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: invalid archive descriptor", skillsync.ErrInvalidConfig)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: invalid archive descriptor", skillsync.ErrInvalidConfig)
	}
	if _, err := skillsync.EmbeddedBundle(descriptor, files); err != nil {
		return skillsync.BundleDescriptor{}, nil, err
	}
	return descriptor, files, nil
}

// WriteEmbedDirectory materializes the exact descriptor/content pair for a
// caller's go:embed directory. It is intentionally separate from archive IO.
func WriteEmbedDirectory(dir string, descriptor skillsync.BundleDescriptor, content fs.FS) error {
	var err error
	descriptor, err = normalizedDescriptor(descriptor, content)
	if err != nil {
		return err
	}
	files, err := sourceFiles(content)
	if err != nil {
		return err
	}
	// A producer owns a freshly-created output only. Reusing an existing tree
	// could retain stale content, follow a symlink, or preserve the wrong mode.
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(descriptor, "", "  ") // BundleDescriptor contains only marshalable value fields.
	if err := os.WriteFile(filepath.Join(dir, descriptorName), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	for _, file := range files {
		path := filepath.Join(dir, filepath.FromSlash(contentPrefix), filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if executable(descriptor.ExecutablePaths, file.name) {
			mode = 0o755
		}
		if err := os.WriteFile(path, file.data, mode); err != nil {
			return err
		}
	}
	return nil
}

func executable(paths []string, name string) bool {
	for _, path := range paths {
		if path == name {
			return true
		}
	}
	return false
}

func validArchivePath(name string) bool {
	return name != "." && fs.ValidPath(name) && !path.IsAbs(name) && !strings.Contains(name, "\\") && path.Clean(name) == name
}
