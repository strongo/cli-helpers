// Package githubrelease resolves explicitly requested newer-compatible bundles
// from published GitHub Release assets. It trusts HTTPS to the configured API
// endpoint; the archive descriptor digest detects altered artifact content.
package githubrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/snapshot"
)

// Source implements skillsync.ReleaseSource against the GitHub Releases API.
type Source struct {
	Client              *http.Client
	BaseURL             string
	AssetName           string
	DescriptorAssetName string
	MaxPages            int
	MaxMetadataBytes    int64
	MaxAssetBytes       int64
}

type release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

func (s Source) defaults() Source {
	if s.Client == nil {
		s.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if s.BaseURL == "" {
		s.BaseURL = "https://api.github.com"
	}
	if s.AssetName == "" {
		s.AssetName = snapshot.DefaultAssetName
	}
	if s.DescriptorAssetName == "" {
		s.DescriptorAssetName = snapshot.DefaultDescriptorAssetName
	}
	if s.MaxPages <= 0 {
		s.MaxPages = 10
	}
	if s.MaxMetadataBytes <= 0 {
		s.MaxMetadataBytes = 1 << 20
	}
	if s.MaxAssetBytes <= 0 {
		s.MaxAssetBytes = 16 << 20
	}
	return s
}

// NewerCompatible returns ErrNoNewerCompatible when the matched bundle remains
// the newest compatible stable choice; callers then retain their embed.
func (s Source) NewerCompatible(ctx context.Context, matched skillsync.Source, current string) (skillsync.BundleDescriptor, fs.FS, error) {
	s = s.defaults()
	owner, repository, err := githubRepository(matched.Repository)
	if err != nil {
		return skillsync.BundleDescriptor{}, nil, err
	}
	var candidates []candidate
	for page := 1; page <= s.MaxPages; page++ {
		pageReleases, next, err := s.list(ctx, owner, repository, page)
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, err
		}
		for _, release := range pageReleases {
			if release.Draft || release.Prerelease {
				continue
			}
			descriptorAsset, ok := releaseAsset(release.Assets, s.DescriptorAssetName)
			if !ok {
				continue
			}
			if descriptorAsset.Size < 0 || descriptorAsset.Size > s.MaxMetadataBytes {
				return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: release descriptor exceeds size limit", skillsync.ErrInvalidConfig)
			}
			raw, err := s.download(ctx, descriptorAsset.URL, s.MaxMetadataBytes)
			if err != nil {
				return skillsync.BundleDescriptor{}, nil, err
			}
			descriptor, err := decodeDescriptor(raw)
			if err != nil {
				return skillsync.BundleDescriptor{}, nil, err
			}
			archive, ok := releaseAsset(release.Assets, s.AssetName)
			if !ok || descriptor.Source.Repository != matched.Repository || descriptor.Source.Path != matched.Path || !skillsync.Compatible(current, descriptor.Source.Compatibility) {
				continue
			}
			cmp, err := skillsync.CompareVersions(descriptor.Source.Version, matched.Version)
			if err != nil || cmp <= 0 {
				continue
			}
			candidates = append(candidates, candidate{descriptor: descriptor, archive: archive})
		}
		if !next {
			break
		}
		if page == s.MaxPages {
			return skillsync.BundleDescriptor{}, nil, skillsync.ErrSearchIncomplete
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		cmp, _ := skillsync.CompareVersions(candidates[i].descriptor.Source.Version, candidates[j].descriptor.Source.Version)
		return cmp > 0
	})
	if len(candidates) > 0 {
		candidate := candidates[0]
		if candidate.archive.Size < 0 || candidate.archive.Size > s.MaxAssetBytes {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: release asset exceeds size limit", skillsync.ErrInvalidConfig)
		}
		raw, err := s.download(ctx, candidate.archive.URL, s.MaxAssetBytes)
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, err
		}
		descriptor, content, err := snapshot.Unpack(raw, snapshot.Limits{MaxBytes: s.MaxAssetBytes})
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, err
		}
		if !reflect.DeepEqual(descriptor, candidate.descriptor) {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: archive descriptor differs from companion descriptor", skillsync.ErrInvalidConfig)
		}
		return descriptor, content, nil
	}
	return skillsync.BundleDescriptor{}, nil, skillsync.ErrNoNewerCompatible
}

type candidate struct {
	descriptor skillsync.BundleDescriptor
	archive    asset
}

func (s Source) list(ctx context.Context, owner, repository string, page int) ([]release, bool, error) {
	base, err := url.Parse(s.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, false, fmt.Errorf("%w: invalid GitHub base URL", skillsync.ErrInvalidConfig)
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/repos/" + owner + "/" + repository + "/releases"
	query := base.Query()
	query.Set("per_page", "100")
	query.Set("page", fmt.Sprint(page))
	base.RawQuery = query.Encode()
	raw, headers, err := s.get(ctx, base.String(), s.MaxMetadataBytes)
	if err != nil {
		return nil, false, err
	}
	var releases []release
	if err := json.Unmarshal(raw, &releases); err != nil {
		return nil, false, fmt.Errorf("%w: invalid GitHub releases response", skillsync.ErrInvalidConfig)
	}
	return releases, hasNextPage(headers.Get("Link")), nil
}

func (s Source) download(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	base, baseErr := url.Parse(s.BaseURL)
	if err != nil || baseErr != nil || base.Scheme != "https" || base.Host == "" || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid release asset URL", skillsync.ErrInvalidConfig)
	}
	raw, _, err := s.get(ctx, rawURL, limit)
	return raw, err
}

var errHTTPDowngrade = errors.New("GitHub redirect left HTTPS")

func (s Source) get(ctx context.Context, target string, limit int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := *s.Client
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" {
			return errHTTPDowngrade
		}
		if previousRedirect != nil {
			return previousRedirect(next, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	response, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errHTTPDowngrade) {
			return nil, nil, fmt.Errorf("%w: GitHub request left HTTPS", skillsync.ErrInvalidConfig)
		}
		return nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.Request == nil || response.Request.URL.Scheme != "https" {
		return nil, nil, fmt.Errorf("%w: GitHub request left HTTPS", skillsync.ErrInvalidConfig)
	}
	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GitHub request %s: %s", target, response.Status)
	}
	if response.ContentLength > limit {
		return nil, nil, fmt.Errorf("%w: HTTP response exceeds size limit", skillsync.ErrInvalidConfig)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, nil, fmt.Errorf("%w: HTTP response exceeds size limit", skillsync.ErrInvalidConfig)
	}
	return raw, response.Header, nil
}

func decodeDescriptor(raw []byte) (skillsync.BundleDescriptor, error) {
	var descriptor skillsync.BundleDescriptor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return skillsync.BundleDescriptor{}, fmt.Errorf("%w: invalid release descriptor", skillsync.ErrInvalidConfig)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return skillsync.BundleDescriptor{}, fmt.Errorf("%w: invalid release descriptor", skillsync.ErrInvalidConfig)
	}
	if err := skillsync.ValidateDescriptor(descriptor); err != nil {
		return skillsync.BundleDescriptor{}, err
	}
	return descriptor, nil
}

func hasNextPage(link string) bool { return strings.Contains(link, "rel=\"next\"") }

func githubRepository(repository string) (string, string, error) {
	parts := strings.Split(strings.TrimPrefix(repository, "https://"), "/")
	if len(parts) != 3 || parts[0] != "github.com" || !validSegment(parts[1]) || !validSegment(parts[2]) {
		return "", "", fmt.Errorf("%w: GitHub repository must be github.com/owner/repository", skillsync.ErrInvalidConfig)
	}
	return parts[1], parts[2], nil
}

func validSegment(value string) bool  { return value != "" && !strings.ContainsAny(value, "/\\?&#") }
func versionForTag(tag string) string { return strings.TrimPrefix(tag, "v") }
func releaseAsset(assets []asset, name string) (asset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return asset{}, false
}
