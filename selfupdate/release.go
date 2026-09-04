package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// releaseJSON mirrors the subset of the GitHub release JSON this package
// consumes.
type releaseJSON struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// fetchReleases lists c.Repository's releases from c.ReleasesAPIURL, newest
// first (GitHub's own ordering). Callers must invoke this on an already-
// withDefaults'd Config.
func (c Config) fetchReleases(ctx context.Context) ([]releaseJSON, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ReleasesAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github releases request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var releases []releaseJSON
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode github releases: %w", err)
	}
	return releases, nil
}

// latestStableTag returns the tag of the newest release that is neither a
// draft nor a prerelease (REQ: latest-release-source), and — when
// c.TagPrefix is set — that belongs to this product at all. Releases come
// back newest-first, so this returns the first entry that passes both
// filters. When c.TagPrefix is empty every release's tag trivially carries
// it, so this is unchanged from before TagPrefix existed.
func (c Config) latestStableTag(ctx context.Context) (string, error) {
	releases, err := c.fetchReleases(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range releases {
		if r.Prerelease || r.Draft {
			continue
		}
		if !strings.HasPrefix(r.TagName, c.TagPrefix) {
			continue
		}
		return r.TagName, nil
	}
	return "", fmt.Errorf("no stable release found")
}

// resolveTag finds the exact published tag matching a pinned version,
// ignoring a leading "v" on either side so "1.2.3" and "v1.2.3" resolve to
// the same release (REQ: version-pin). Unlike latestStableTag, this does
// NOT filter out prereleases or drafts — a pin names an exact release
// regardless of its status (REQ: pinned-exact-tag): a caller who typed the
// tag explicitly gets exactly what they asked for, not the stable subset.
//
// When c.TagPrefix is set, only releases whose tag carries it are even
// considered, so a bare-version pin (e.g. "0.15.1") can never resolve to
// another product's release published in the same repository, even if that
// other release happens to share the same version number. Within that
// filtered set, pinned may be either the bare version ("0.15.1") or the full
// prefixed tag ("cli-v0.15.1") — the latter matches because it is compared
// against the tag as published, unstripped, alongside the prefix-stripped
// comparison.
//
// The returned tag is the string exactly as GitHub published it (whatever
// "v" convention that repository uses), because that exact string is what
// the download URL's path segment must be.
func (c Config) resolveTag(ctx context.Context, pinned string) (string, error) {
	releases, err := c.fetchReleases(ctx)
	if err != nil {
		return "", &Failure{Kind: KindReleaseLookup, Err: err}
	}
	want := normalize(pinned)
	for _, r := range releases {
		if !strings.HasPrefix(r.TagName, c.TagPrefix) {
			continue
		}
		if c.versionFromTag(r.TagName) == want || normalize(r.TagName) == want {
			return r.TagName, nil
		}
	}
	return "", &Failure{Kind: KindUnknownTag, Err: fmt.Errorf("no release found for tag %q", pinned)}
}
