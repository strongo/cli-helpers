// Package githubrelease resolves explicitly requested newer-compatible bundles
// from published GitHub Release assets. It trusts HTTPS to the configured API
// endpoint; the archive descriptor digest detects altered artifact content.
package githubrelease

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/snapshot"
)

// Source implements skillsync.ReleaseSource against the GitHub Releases API.
type Source struct {
	Client           *http.Client
	BaseURL          string
	AssetName        string
	MaxPages         int
	MaxMetadataBytes int64
	MaxAssetBytes    int64
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
	var releases []release
	for page := 1; page <= s.MaxPages; page++ {
		pageReleases, err := s.list(ctx, owner, repository, page)
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, err
		}
		releases = append(releases, pageReleases...)
		if len(pageReleases) == 0 {
			break
		}
	}
	sort.Slice(releases, func(i, j int) bool {
		cmp, err := skillsync.CompareVersions(versionForTag(releases[i].TagName), versionForTag(releases[j].TagName))
		return err == nil && cmp > 0
	})
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		version := versionForTag(release.TagName)
		cmp, err := skillsync.CompareVersions(version, matched.Version)
		if err != nil || cmp <= 0 {
			continue
		}
		asset, ok := releaseAsset(release.Assets, s.AssetName)
		if !ok {
			continue
		}
		if asset.Size < 0 || asset.Size > s.MaxAssetBytes {
			return skillsync.BundleDescriptor{}, nil, fmt.Errorf("%w: release asset exceeds size limit", skillsync.ErrInvalidConfig)
		}
		raw, err := s.download(ctx, asset.URL)
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, err
		}
		descriptor, content, err := snapshot.Unpack(raw, snapshot.Limits{MaxBytes: s.MaxAssetBytes})
		if err != nil {
			return skillsync.BundleDescriptor{}, nil, err
		}
		if descriptor.Source.Repository != matched.Repository || descriptor.Source.Path != matched.Path || descriptor.Source.Version != version || !skillsync.Compatible(current, descriptor.Source.Compatibility) {
			continue
		}
		return descriptor, content, nil
	}
	return skillsync.BundleDescriptor{}, nil, skillsync.ErrNoNewerCompatible
}

func (s Source) list(ctx context.Context, owner, repository string, page int) ([]release, error) {
	base, err := url.Parse(s.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("%w: invalid GitHub base URL", skillsync.ErrInvalidConfig)
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/repos/" + owner + "/" + repository + "/releases"
	query := base.Query()
	query.Set("per_page", "100")
	query.Set("page", fmt.Sprint(page))
	base.RawQuery = query.Encode()
	raw, err := s.get(ctx, base.String(), s.MaxMetadataBytes)
	if err != nil {
		return nil, err
	}
	var releases []release
	if err := json.Unmarshal(raw, &releases); err != nil {
		return nil, fmt.Errorf("%w: invalid GitHub releases response", skillsync.ErrInvalidConfig)
	}
	return releases, nil
}

func (s Source) download(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	base, baseErr := url.Parse(s.BaseURL)
	if err != nil || baseErr != nil || base.Scheme != "https" || base.Host == "" || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid release asset URL", skillsync.ErrInvalidConfig)
	}
	return s.get(ctx, rawURL, s.MaxAssetBytes)
}

func (s Source) get(ctx context.Context, target string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.Scheme != "https" {
		return nil, fmt.Errorf("%w: GitHub request left HTTPS", skillsync.ErrInvalidConfig)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub request %s: %s", target, response.Status)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("%w: HTTP response exceeds size limit", skillsync.ErrInvalidConfig)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: HTTP response exceeds size limit", skillsync.ErrInvalidConfig)
	}
	return raw, nil
}

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
