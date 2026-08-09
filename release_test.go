package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testConfig(baseURL string) Config {
	return Config{
		Repository:     "acme/tool",
		ReleasesAPIURL: baseURL,
		HTTPClient:     http.DefaultClient,
	}
}

// AC: only-verified-bytes-are-installed — the newest release is a
// prerelease; an older stable release exists. The resolved latest must be
// the stable one.
func TestLatestStableTag_SkipsPrerelease(t *testing.T) {
	body := `[
		{"tag_name": "v1.0.0-rc.1", "prerelease": true, "draft": false},
		{"tag_name": "v0.6.0", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)

	tag, err := testConfig(srv.URL).latestStableTag(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v0.6.0" {
		t.Fatalf("latest stable tag = %q, want %q", tag, "v0.6.0")
	}
}

func TestLatestStableTag_SkipsDraft(t *testing.T) {
	body := `[
		{"tag_name": "v2.0.0", "prerelease": false, "draft": true},
		{"tag_name": "v1.5.0", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)

	tag, err := testConfig(srv.URL).latestStableTag(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v1.5.0" {
		t.Fatalf("latest stable tag = %q, want %q", tag, "v1.5.0")
	}
}

func TestLatestStableTag_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	if _, err := testConfig(srv.URL).latestStableTag(context.Background()); err == nil {
		t.Fatal("expected error on non-200 response, got nil")
	}
}

func TestLatestStableTag_RequestError(t *testing.T) {
	cfg := testConfig("http://example.com/\x7f")
	if _, err := cfg.latestStableTag(context.Background()); err == nil {
		t.Fatal("expected request build error, got nil")
	}
}

func TestLatestStableTag_DoError(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1")
	if _, err := cfg.latestStableTag(context.Background()); err == nil {
		t.Fatal("expected transport error, got nil")
	}
}

func TestLatestStableTag_DecodeError(t *testing.T) {
	srv := newReleasesServer(t, "{not json")
	if _, err := testConfig(srv.URL).latestStableTag(context.Background()); err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestLatestStableTag_NoStableRelease(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v1.0.0-rc.1","prerelease":true,"draft":false}]`)
	if _, err := testConfig(srv.URL).latestStableTag(context.Background()); err == nil {
		t.Fatal("expected no-stable-release error, got nil")
	}
}

// --- TagPrefix: multi-product repository filtering ---

// The real synchestra-releases case: one repository, two products, the
// product encoded as a tag prefix. Each binary's latest resolution must
// ignore the other product's releases entirely, including when the other
// product's release is newer (REQ: tag-prefix — latest-stable resolution
// ignores non-matching tags).
func TestLatestStableTag_FiltersByTagPrefix(t *testing.T) {
	body := `[
		{"tag_name": "servers-v0.26.1", "prerelease": false, "draft": false},
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": false},
		{"tag_name": "cli-v0.14.0", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := testConfig(srv.URL)

	cfg.TagPrefix = "cli-"
	tag, err := cfg.latestStableTag(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "cli-v0.15.1" {
		t.Fatalf("latest stable tag = %q, want %q (must ignore the servers- product)", tag, "cli-v0.15.1")
	}

	cfg.TagPrefix = "servers-"
	tag, err = cfg.latestStableTag(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "servers-v0.26.1" {
		t.Fatalf("latest stable tag = %q, want %q (must ignore the cli- product)", tag, "servers-v0.26.1")
	}
}

// Prefix filtering and draft/prerelease filtering both apply: the newest
// same-product release is a draft, so the next-newest same-product stable
// release must win — never a newer OTHER product's release, and never the
// draft.
func TestLatestStableTag_PrefixAndDraftPrereleaseFilteringCombine(t *testing.T) {
	body := `[
		{"tag_name": "cli-v0.16.0-rc.1", "prerelease": true, "draft": false},
		{"tag_name": "servers-v9.9.9", "prerelease": false, "draft": false},
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": true},
		{"tag_name": "cli-v0.14.0", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := testConfig(srv.URL)
	cfg.TagPrefix = "cli-"

	tag, err := cfg.latestStableTag(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "cli-v0.14.0" {
		t.Fatalf("latest stable tag = %q, want %q", tag, "cli-v0.14.0")
	}
}

// A TagPrefix that matches no release fails the same way "no stable release"
// fails today, even though stable releases exist for another product.
func TestLatestStableTag_TagPrefixMatchesNothing(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"servers-v0.26.1","prerelease":false,"draft":false}]`)
	cfg := testConfig(srv.URL)
	cfg.TagPrefix = "cli-"

	_, err := cfg.latestStableTag(context.Background())
	if err == nil {
		t.Fatal("expected no-stable-release error when the prefix matches nothing, got nil")
	}
	if err.Error() != "no stable release found" {
		t.Errorf("error = %q, want the same message the no-stable-release case uses today", err.Error())
	}
}

// An empty TagPrefix (the single-product default) is exactly the pre-
// TagPrefix behavior: every release belongs to this binary.
func TestLatestStableTag_EmptyTagPrefixUnchanged(t *testing.T) {
	body := `[
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": false},
		{"tag_name": "v0.6.0", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := testConfig(srv.URL)

	tag, err := cfg.latestStableTag(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "cli-v0.15.1" {
		t.Fatalf("latest stable tag = %q, want %q (newest release, prefix filtering is a no-op)", tag, "cli-v0.15.1")
	}
}

// --- resolveTag (pinned) ---

// A pin resolves ignoring a leading "v" on either side.
func TestResolveTag_IgnoresLeadingV(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v0.0.3","prerelease":false,"draft":false}]`)
	tag, err := testConfig(srv.URL).resolveTag(context.Background(), "0.0.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v0.0.3" {
		t.Fatalf("resolveTag = %q, want the published tag %q", tag, "v0.0.3")
	}
}

// A pin matches a prerelease or draft exactly — unlike latestStableTag, it
// does not filter them out (REQ: pinned-exact-tag).
func TestResolveTag_MatchesPrereleaseAndDraft(t *testing.T) {
	srv := newReleasesServer(t, `[
		{"tag_name": "v1.0.0", "prerelease": false, "draft": false},
		{"tag_name": "v0.9.0-rc.1", "prerelease": true, "draft": false},
		{"tag_name": "v0.8.0", "prerelease": false, "draft": true}
	]`)
	cfg := testConfig(srv.URL)

	tag, err := cfg.resolveTag(context.Background(), "v0.9.0-rc.1")
	if err != nil {
		t.Fatalf("unexpected error resolving prerelease pin: %v", err)
	}
	if tag != "v0.9.0-rc.1" {
		t.Fatalf("resolveTag(prerelease) = %q, want %q", tag, "v0.9.0-rc.1")
	}

	tag, err = cfg.resolveTag(context.Background(), "0.8.0")
	if err != nil {
		t.Fatalf("unexpected error resolving draft pin: %v", err)
	}
	if tag != "v0.8.0" {
		t.Fatalf("resolveTag(draft) = %q, want %q", tag, "v0.8.0")
	}
}

// An unpublished tag is a typed KindUnknownTag failure naming the tag.
func TestResolveTag_UnknownTag(t *testing.T) {
	srv := newReleasesServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`)
	_, err := testConfig(srv.URL).resolveTag(context.Background(), "v9.9.9")
	if err == nil {
		t.Fatal("expected error for an unpublished tag, got nil")
	}
	if KindOf(err) != KindUnknownTag {
		t.Errorf("KindOf(err) = %v, want KindUnknownTag", KindOf(err))
	}
	if !strings.Contains(err.Error(), "v9.9.9") {
		t.Errorf("error %q does not name the requested tag", err.Error())
	}
}

func TestResolveTag_ReleaseLookupFailureIsTyped(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1")
	_, err := cfg.resolveTag(context.Background(), "v1.0.0")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if KindOf(err) != KindReleaseLookup {
		t.Errorf("KindOf(err) = %v, want KindReleaseLookup", KindOf(err))
	}
}

// --- resolveTag + TagPrefix ---

// A bare-version pin resolves to the full prefixed tag for THIS product,
// even though another product in the same repository publishes a release
// carrying the exact same bare version (REQ: tag-prefix — a pin must never
// resolve to another product's release).
func TestResolveTag_PrefixedPinResolvesToFullTag(t *testing.T) {
	body := `[
		{"tag_name": "servers-v0.15.1", "prerelease": false, "draft": false},
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := testConfig(srv.URL)
	cfg.TagPrefix = "cli-"

	tag, err := cfg.resolveTag(context.Background(), "0.15.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "cli-v0.15.1" {
		t.Fatalf("resolveTag(%q) = %q, want the cli- product's own tag %q", "0.15.1", tag, "cli-v0.15.1")
	}

	// The leading "v" is optional on a prefixed pin too.
	tag, err = cfg.resolveTag(context.Background(), "v0.15.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "cli-v0.15.1" {
		t.Fatalf("resolveTag(%q) = %q, want %q", "v0.15.1", tag, "cli-v0.15.1")
	}
}

// The fully-qualified tag also resolves, without accidentally matching the
// other product's release of the same bare version.
func TestResolveTag_FullyQualifiedPrefixedTagResolves(t *testing.T) {
	body := `[
		{"tag_name": "servers-v0.15.1", "prerelease": false, "draft": false},
		{"tag_name": "cli-v0.15.1", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := testConfig(srv.URL)
	cfg.TagPrefix = "cli-"

	tag, err := cfg.resolveTag(context.Background(), "cli-v0.15.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "cli-v0.15.1" {
		t.Fatalf("resolveTag(%q) = %q, want %q", "cli-v0.15.1", tag, "cli-v0.15.1")
	}
}

// A pin that only exists for another product must not resolve at all — it
// is reported the same way a wholly unknown tag is (REQ: tag-prefix — never
// resolve to another product's release).
func TestResolveTag_PinDoesNotCrossProducts(t *testing.T) {
	body := `[
		{"tag_name": "servers-v0.26.1", "prerelease": false, "draft": false}
	]`
	srv := newReleasesServer(t, body)
	cfg := testConfig(srv.URL)
	cfg.TagPrefix = "cli-"

	_, err := cfg.resolveTag(context.Background(), "0.26.1")
	if err == nil {
		t.Fatal("expected error: a servers- release must not satisfy a cli- pin, got nil")
	}
	if KindOf(err) != KindUnknownTag {
		t.Errorf("KindOf(err) = %v, want KindUnknownTag", KindOf(err))
	}

	// Even the fully-qualified other product's tag must not resolve.
	_, err = cfg.resolveTag(context.Background(), "servers-v0.26.1")
	if err == nil {
		t.Fatal("expected error: the fully-qualified servers- tag must not satisfy a cli- pin, got nil")
	}
	if KindOf(err) != KindUnknownTag {
		t.Errorf("KindOf(err) = %v, want KindUnknownTag", KindOf(err))
	}
}
