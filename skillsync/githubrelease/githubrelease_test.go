package githubrelease

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/strongo/cli-helpers/skillsync"
	"github.com/strongo/cli-helpers/skillsync/snapshot"
)

func descriptor(t *testing.T, version string, compatibility skillsync.Compatibility) (skillsync.BundleDescriptor, fs.FS) {
	t.Helper()
	content := fstest.MapFS{"alpha/SKILL.md": &fstest.MapFile{Data: []byte(version)}}
	digest, err := skillsync.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	return skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "strongo", Name: "plugin"}, Source: skillsync.Source{Repository: "github.com/strongo/plugin", Path: "skills", Revision: strings.Repeat(version[:1], 40), Version: version, Digest: digest, Compatibility: compatibility}}, content
}

func artifact(t *testing.T, version string, compatibility skillsync.Compatibility) []byte {
	t.Helper()
	descriptor, content := descriptor(t, version, compatibility)
	raw, err := snapshot.Pack(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNewerCompatiblePaginatesSkipsIneligibleAndValidatesAsset(t *testing.T) {
	compatible := artifact(t, "1.2.0", skillsync.Compatibility{MinCLI: "1.0.0", MaxCLI: "2.0.0"})
	incompatible := artifact(t, "2.0.0", skillsync.Compatibility{MinCLI: "9.0.0"})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/strongo/plugin/releases":
			page := r.URL.Query().Get("page")
			if page == "1" {
				_ = json.NewEncoder(w).Encode([]release{{TagName: "v3.0.0", Prerelease: true}, {TagName: "v2.0.0", Assets: []asset{{Name: "skillsync-bundle.tar", URL: serverURL(r, "/assets/incompatible"), Size: int64(len(incompatible))}}}})
				return
			}
			if page == "2" {
				_ = json.NewEncoder(w).Encode([]release{{TagName: "v1.2.0", Assets: []asset{{Name: "skillsync-bundle.tar", URL: serverURL(r, "/assets/compatible"), Size: int64(len(compatible))}}}})
				return
			}
			_ = json.NewEncoder(w).Encode([]release{})
		case "/assets/incompatible":
			_, _ = w.Write(incompatible)
		case "/assets/compatible":
			_, _ = w.Write(compatible)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	resolved, content, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 3}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source.Version != "1.2.0" {
		t.Fatalf("resolved=%#v", resolved)
	}
	data, err := fs.ReadFile(content, "alpha/SKILL.md")
	if err != nil || string(data) != "1.2.0" {
		t.Fatalf("content=%q err=%v", data, err)
	}
}

func TestNewerCompatibleNoMatchCorruptAndLimits(t *testing.T) {
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	for _, tc := range []struct {
		name  string
		body  []byte
		size  int64
		limit int64
		want  error
	}{
		{name: "no-match", body: artifact(t, "1.1.0", skillsync.Compatibility{MinCLI: "9.0.0"}), want: skillsync.ErrNoNewerCompatible},
		{name: "corrupt", body: []byte("not an archive"), want: skillsync.ErrInvalidConfig},
		{name: "size", body: artifact(t, "1.1.0", skillsync.Compatibility{}), size: 1 << 20, limit: 8, want: skillsync.ErrInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/repos/") {
					size := tc.size
					if size == 0 {
						size = int64(len(tc.body))
					}
					_ = json.NewEncoder(w).Encode([]release{{TagName: "v1.1.0", Assets: []asset{{Name: "skillsync-bundle.tar", URL: serverURL(r, "/asset"), Size: size}}}})
					return
				}
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			_, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 1, MaxAssetBytes: tc.limit}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestNewerCompatibleHonorsCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 1}).NewerCompatible(ctx, matched.Source, "1.2.3")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestSourceRejectsInvalidReleaseMetadataAndUntrustedAssetURLs(t *testing.T) {
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	if _, _, err := (Source{}).NewerCompatible(context.Background(), skillsync.Source{Repository: "not-github"}, "1.2.3"); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("invalid repository error = %v", err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		raw, _ := json.Marshal([]release{
			{TagName: "not-a-version"},
			{TagName: "v1.1.0"},
			{TagName: "v1.3.0", Assets: []asset{{Name: "different-asset"}}},
			{TagName: "v1.2.0", Assets: []asset{{Name: snapshot.DefaultAssetName, URL: "http://untrusted.invalid/archive"}}},
		})
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: request}, nil
	})}
	_, _, err := (Source{BaseURL: "https://api.github.example", Client: client, MaxPages: 1}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
	if !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
	if _, err := (Source{BaseURL: "https://api.github.example", Client: client, MaxAssetBytes: 1}).download(context.Background(), "http://untrusted.invalid/asset"); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("insecure asset error = %v", err)
	}
}

func TestHTTPAndMetadataErrors(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/repos/bad-json/") {
			_, _ = w.Write([]byte("{"))
			return
		}
		switch r.URL.Path {
		case "/status":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/large":
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("0123456789"))
		case "/stream-large":
			_, _ = w.Write([]byte("0123456789"))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()
	source := Source{BaseURL: server.URL, Client: server.Client(), MaxMetadataBytes: 32, MaxAssetBytes: 4}
	if _, err := (Source{BaseURL: "://bad", Client: server.Client()}).list(context.Background(), "strongo", "plugin", 1); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("bad base error = %v", err)
	}
	if _, err := source.get(context.Background(), server.URL+"/status", 4); err == nil {
		t.Fatal("expected status failure")
	}
	if _, err := source.get(context.Background(), server.URL+"/large", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("content length error = %v", err)
	}
	if _, err := source.get(context.Background(), server.URL+"/stream-large", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("stream size error = %v", err)
	}
	if _, err := source.get(context.Background(), "://bad", 4); err == nil {
		t.Fatal("expected request construction failure")
	}
	if _, err := source.get(context.Background(), "http://127.0.0.1:1", 4); err == nil {
		t.Fatal("expected transport failure")
	}
	readFailure := Source{Client: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: errReadCloser{}, Request: request}, nil
	})}}
	if _, err := readFailure.get(context.Background(), "https://api.github.example/asset", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("read failure error = %v", err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }))
	defer httpServer.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpServer.URL, http.StatusFound)
	}))
	defer tlsServer.Close()
	if _, err := (Source{Client: tlsServer.Client()}).get(context.Background(), tlsServer.URL, 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("redirect error = %v", err)
	}
	if _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxMetadataBytes: 64}).list(context.Background(), "bad-json", "x", 1); err == nil {
		t.Fatal("expected malformed metadata rejection")
	}
}

func TestSmallHelpers(t *testing.T) {
	defaults := (Source{}).defaults()
	if defaults.Client == nil || defaults.BaseURL != "https://api.github.com" || defaults.AssetName != snapshot.DefaultAssetName || defaults.MaxPages != 10 || defaults.MaxMetadataBytes != 1<<20 || defaults.MaxAssetBytes != 16<<20 {
		t.Fatalf("defaults = %#v", defaults)
	}
	for _, repository := range []string{"github.com/owner", "https://git.example/owner/repo", "github.com/owner/repo?x", "github.com/owner/a\\b"} {
		if _, _, err := githubRepository(repository); !errors.Is(err, skillsync.ErrInvalidConfig) {
			t.Fatalf("repository %q error = %v", repository, err)
		}
	}
	owner, repository, err := githubRepository("https://github.com/strongo/plugin")
	if err != nil || owner != "strongo" || repository != "plugin" {
		t.Fatalf("github repository = %q/%q, %v", owner, repository, err)
	}
	if _, ok := releaseAsset(nil, "missing"); ok {
		t.Fatal("expected absent asset")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func serverURL(r *http.Request, path string) string {
	return "https://" + r.Host + path + "?" + strconv.Itoa(len(path))
}
