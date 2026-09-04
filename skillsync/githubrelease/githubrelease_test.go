package githubrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
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

func companion(t *testing.T, version string, compatibility skillsync.Compatibility) []byte {
	t.Helper()
	descriptor, content := descriptor(t, version, compatibility)
	raw, err := snapshot.DescriptorJSON(descriptor, content)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNewerCompatiblePaginatesSkipsIneligibleAndValidatesAsset(t *testing.T) {
	compatible := artifact(t, "1.2.0", skillsync.Compatibility{MinCLI: "1.0.0", MaxCLI: "2.0.0"})
	incompatible := artifact(t, "2.0.0", skillsync.Compatibility{MinCLI: "9.0.0"})
	compatibleDescriptor := companion(t, "1.2.0", skillsync.Compatibility{MinCLI: "1.0.0", MaxCLI: "2.0.0"})
	incompatibleDescriptor := companion(t, "2.0.0", skillsync.Compatibility{MinCLI: "9.0.0"})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/strongo/plugin/releases":
			page := r.URL.Query().Get("page")
			if page == "1" {
				w.Header().Set("Link", "<next>; rel=\"next\"")
				_ = json.NewEncoder(w).Encode([]release{{TagName: "v3.0.0", Prerelease: true}, {TagName: "v2.0.0", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptors/incompatible"), Size: int64(len(incompatibleDescriptor))}, {Name: "skillsync-bundle.tar", URL: serverURL(r, "/assets/incompatible"), Size: int64(len(incompatible))}}}})
				return
			}
			if page == "2" {
				_ = json.NewEncoder(w).Encode([]release{{TagName: "v0.92.2", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptors/compatible"), Size: int64(len(compatibleDescriptor))}, {Name: "skillsync-bundle.tar", URL: serverURL(r, "/assets/compatible"), Size: int64(len(compatible))}}}})
				return
			}
			_ = json.NewEncoder(w).Encode([]release{})
		case "/assets/incompatible":
			_, _ = w.Write(incompatible)
		case "/assets/compatible":
			_, _ = w.Write(compatible)
		case "/descriptors/incompatible":
			_, _ = w.Write(incompatibleDescriptor)
		case "/descriptors/compatible":
			_, _ = w.Write(compatibleDescriptor)
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
			descriptorBody := companion(t, "1.1.0", skillsync.Compatibility{})
			if tc.name == "no-match" {
				descriptorBody = companion(t, "1.1.0", skillsync.Compatibility{MinCLI: "9.0.0"})
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/repos/") {
					size := tc.size
					if size == 0 {
						size = int64(len(tc.body))
					}
					_ = json.NewEncoder(w).Encode([]release{{TagName: "v1.1.0", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptor"), Size: int64(len(descriptorBody))}, {Name: "skillsync-bundle.tar", URL: serverURL(r, "/asset"), Size: size}}}})
					return
				}
				if r.URL.Path == "/descriptor" {
					_, _ = w.Write(descriptorBody)
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

func TestNewerCompatibleReleaseSelectionUsesDescriptors(t *testing.T) {
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	versions := []string{"1.1.0", "1.4.0", "1.3.0"}
	archives := make(map[string][]byte, len(versions))
	descriptors := make(map[string][]byte, len(versions))
	for _, version := range versions {
		archives[version] = artifact(t, version, skillsync.Compatibility{})
		descriptors[version] = companion(t, version, skillsync.Compatibility{})
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/strongo/plugin/releases":
			_ = json.NewEncoder(w).Encode([]release{
				{TagName: "unrelated-host-tag", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptor/1.1.0"), Size: int64(len(descriptors["1.1.0"]))}, {Name: snapshot.DefaultAssetName, URL: serverURL(r, "/archive/1.1.0"), Size: int64(len(archives["1.1.0"]))}}},
				{TagName: "v0.1.0", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptor/1.4.0"), Size: int64(len(descriptors["1.4.0"]))}, {Name: snapshot.DefaultAssetName, URL: serverURL(r, "/archive/1.4.0"), Size: int64(len(archives["1.4.0"]))}}},
				{TagName: "release-name-is-not-version", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptor/1.3.0"), Size: int64(len(descriptors["1.3.0"]))}, {Name: snapshot.DefaultAssetName, URL: serverURL(r, "/archive/1.3.0"), Size: int64(len(archives["1.3.0"]))}}},
			})
		default:
			parts := strings.Split(r.URL.Path, "/")
			version := parts[len(parts)-1]
			if strings.HasPrefix(r.URL.Path, "/descriptor/") {
				_, _ = w.Write(descriptors[version])
				return
			}
			_, _ = w.Write(archives[version])
		}
	}))
	defer server.Close()

	resolved, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 1}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source.Version != "1.4.0" {
		t.Fatalf("selected descriptor version = %q", resolved.Source.Version)
	}
}

func TestNewerCompatibleRejectsDescriptorAndArchiveBoundaryFailures(t *testing.T) {
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	validDescriptor := companion(t, "1.2.0", skillsync.Compatibility{})
	wrongSource, wrongContent := descriptor(t, "1.3.0", skillsync.Compatibility{})
	wrongSource.Source.Repository = "github.com/strongo/other-plugin"
	wrongSourceDescriptor, err := snapshot.DescriptorJSON(wrongSource, wrongContent)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		descriptor  []byte
		descriptorN int64
		archive     []byte
		archiveN    int64
		current     string
		want        error
		wantAny     bool
		wantArchive bool
	}{
		{name: "declared-descriptor-limit", descriptor: validDescriptor, descriptorN: 1<<20 + 1, want: skillsync.ErrInvalidConfig},
		{name: "oversized-companion-body", descriptor: append(validDescriptor, bytes.Repeat([]byte("x"), 1<<20)...), descriptorN: 1, want: skillsync.ErrInvalidConfig},
		{name: "invalid-companion", descriptor: []byte("{"), descriptorN: 1, want: skillsync.ErrInvalidConfig},
		{name: "old-version", descriptor: companion(t, "1.0.0", skillsync.Compatibility{}), descriptorN: 1, want: skillsync.ErrNoNewerCompatible},
		{name: "wrong-source", descriptor: wrongSourceDescriptor, descriptorN: 1, want: skillsync.ErrNoNewerCompatible},
		{name: "incompatible", descriptor: companion(t, "1.3.0", skillsync.Compatibility{MinCLI: "9.0.0"}), descriptorN: 1, current: "1.2.3", want: skillsync.ErrNoNewerCompatible},
		{name: "missing-archive", descriptor: validDescriptor, descriptorN: 1, want: skillsync.ErrNoNewerCompatible},
		{name: "declared-archive-limit", descriptor: validDescriptor, descriptorN: 1, archive: artifact(t, "1.2.0", skillsync.Compatibility{}), archiveN: 16<<20 + 1, want: skillsync.ErrInvalidConfig},
		{name: "archive-download-failure", descriptor: validDescriptor, descriptorN: 1, archiveN: 1, wantAny: true, wantArchive: true},
		{name: "archive-identity-mismatch", descriptor: validDescriptor, descriptorN: 1, archive: artifact(t, "1.3.0", skillsync.Compatibility{}), archiveN: 1, want: skillsync.ErrInvalidConfig, wantArchive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archiveRequests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/strongo/plugin/releases":
					assets := []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptor"), Size: tc.descriptorN}}
					if tc.name != "missing-archive" {
						assets = append(assets, asset{Name: snapshot.DefaultAssetName, URL: serverURL(r, "/archive"), Size: tc.archiveN})
					}
					_ = json.NewEncoder(w).Encode([]release{{TagName: "ignored", Assets: assets}})
				case "/descriptor":
					_, _ = w.Write(tc.descriptor)
				case "/archive":
					archiveRequests.Add(1)
					if tc.name == "archive-download-failure" {
						http.Error(w, "gone", http.StatusGone)
						return
					}
					_, _ = w.Write(tc.archive)
				}
			}))
			defer server.Close()
			_, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 1}).NewerCompatible(context.Background(), matched.Source, tc.current)
			if tc.wantAny && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantAny && !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if got := archiveRequests.Load() > 0; got != tc.wantArchive {
				t.Fatalf("archive requests = %d", archiveRequests.Load())
			}
		})
	}
}

func TestNewerCompatibleSkipsDraftAndRejectsIncompletePagination(t *testing.T) {
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	descriptorBody := companion(t, "1.2.0", skillsync.Compatibility{})
	for _, tc := range []struct {
		name    string
		link    bool
		release release
		want    error
	}{
		{name: "draft", release: release{Draft: true, Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, Size: int64(len(descriptorBody))}}}, want: skillsync.ErrNoNewerCompatible},
		{name: "prerelease", release: release{Prerelease: true, Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, Size: int64(len(descriptorBody))}}}, want: skillsync.ErrNoNewerCompatible},
		{name: "pagination-limit", link: true, want: skillsync.ErrSearchIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var descriptorRequests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/strongo/plugin/releases" {
					if tc.link {
						w.Header().Set("Link", "<next>; rel=\"next\"")
					}
					_ = json.NewEncoder(w).Encode([]release{tc.release})
					return
				}
				descriptorRequests.Add(1)
				_, _ = w.Write(descriptorBody)
			}))
			defer server.Close()
			_, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 1}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if descriptorRequests.Load() != 0 {
				t.Fatalf("unexpected descriptor request count %d", descriptorRequests.Load())
			}
		})
	}
}

func TestNewerCompatibleSkipsInvalidMatchedVersion(t *testing.T) {
	matched, _ := descriptor(t, "1.0.0", skillsync.Compatibility{})
	matched.Source.Version = "not-a-version"
	descriptorBody := companion(t, "1.2.0", skillsync.Compatibility{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/strongo/plugin/releases" {
			_ = json.NewEncoder(w).Encode([]release{{Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: serverURL(r, "/descriptor"), Size: int64(len(descriptorBody))}, {Name: snapshot.DefaultAssetName, URL: serverURL(r, "/archive")}}}})
			return
		}
		_, _ = w.Write(descriptorBody)
	}))
	defer server.Close()
	_, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxPages: 1}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
	if !errors.Is(err, skillsync.ErrNoNewerCompatible) {
		t.Fatalf("err=%v", err)
	}
}

func TestDescriptorDecodeRequiresOneValidDocument(t *testing.T) {
	want, _ := descriptor(t, "1.2.0", skillsync.Compatibility{})
	valid, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeDescriptor(valid)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded=%#v err=%v", got, err)
	}
	for _, raw := range [][]byte{
		[]byte("[]"),
		append(append([]byte(nil), valid...), []byte("\n{}")...),
		[]byte(`{"plugin":{},"source":{}}`),
	} {
		if _, err := decodeDescriptor(raw); !errors.Is(err, skillsync.ErrInvalidConfig) {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
}

func TestGetEnforcesHTTPSRedirectsAndResponseLimits(t *testing.T) {
	var insecureRequests atomic.Int32
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		insecureRequests.Add(1)
		_, _ = w.Write([]byte("insecure"))
	}))
	defer insecure.Close()

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/downgrade":
			http.Redirect(w, r, insecure.URL, http.StatusFound)
		case "/redirect":
			http.Redirect(w, r, server.URL+"/ok", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, server.URL+"/loop", http.StatusFound)
		case "/chunked":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("012"))
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte("456"))
		case "/ok":
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer server.Close()

	source := Source{Client: server.Client()}
	if _, _, err := source.get(context.Background(), server.URL+"/downgrade", 8); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("downgrade err=%v", err)
	}
	if insecureRequests.Load() != 0 {
		t.Fatalf("HTTP redirect target received %d requests", insecureRequests.Load())
	}
	if _, _, err := source.get(context.Background(), server.URL+"/chunked", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("chunked limit err=%v", err)
	}

	stop := errors.New("custom redirect policy")
	client := *server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return stop }
	if _, _, err := (Source{Client: &client}).get(context.Background(), server.URL+"/redirect", 8); !errors.Is(err, stop) {
		t.Fatalf("custom redirect policy err=%v", err)
	}
	if _, _, err := source.get(context.Background(), server.URL+"/loop", 8); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("default redirect limit err=%v", err)
	}
}

func TestGetRejectsResponsesWithoutHTTPSFinalRequest(t *testing.T) {
	for _, responseRequest := range []*http.Request{nil, &http.Request{URL: &url.URL{Scheme: "http", Host: "example.invalid"}}} {
		client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: responseRequest}, nil
		})}
		if _, _, err := (Source{Client: client}).get(context.Background(), "https://api.github.example/asset", 8); !errors.Is(err, skillsync.ErrInvalidConfig) {
			t.Fatalf("request=%v err=%v", responseRequest, err)
		}
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
		if strings.Contains(request.URL.Path, "/descriptor") {
			raw := companion(t, "1.2.0", skillsync.Compatibility{})
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: request}, nil
		}
		raw, _ := json.Marshal([]release{
			{TagName: "not-a-version"},
			{TagName: "v1.1.0"},
			{TagName: "v1.3.0", Assets: []asset{{Name: "different-asset"}}},
			{TagName: "v1.2.0", Assets: []asset{{Name: snapshot.DefaultDescriptorAssetName, URL: "https://api.github.example/descriptor", Size: 100}, {Name: snapshot.DefaultAssetName, URL: "http://untrusted.invalid/archive"}}},
		})
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: request}, nil
	})}
	_, _, err := (Source{BaseURL: "https://api.github.example", Client: client, MaxPages: 1}).NewerCompatible(context.Background(), matched.Source, "1.2.3")
	if !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
	if _, err := (Source{BaseURL: "https://api.github.example", Client: client, MaxAssetBytes: 1}).download(context.Background(), "http://untrusted.invalid/asset", 1); !errors.Is(err, skillsync.ErrInvalidConfig) {
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
	if _, _, err := (Source{BaseURL: "://bad", Client: server.Client()}).list(context.Background(), "strongo", "plugin", 1); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("bad base error = %v", err)
	}
	if _, _, err := source.get(context.Background(), server.URL+"/status", 4); err == nil {
		t.Fatal("expected status failure")
	}
	if _, _, err := source.get(context.Background(), server.URL+"/large", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("content length error = %v", err)
	}
	if _, _, err := source.get(context.Background(), server.URL+"/stream-large", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("stream size error = %v", err)
	}
	if _, _, err := source.get(context.Background(), "://bad", 4); err == nil {
		t.Fatal("expected request construction failure")
	}
	if _, _, err := source.get(context.Background(), "http://127.0.0.1:1", 4); err == nil {
		t.Fatal("expected transport failure")
	}
	readFailure := Source{Client: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: errReadCloser{}, Request: request}, nil
	})}}
	if _, _, err := readFailure.get(context.Background(), "https://api.github.example/asset", 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("read failure error = %v", err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }))
	defer httpServer.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpServer.URL, http.StatusFound)
	}))
	defer tlsServer.Close()
	if _, _, err := (Source{Client: tlsServer.Client()}).get(context.Background(), tlsServer.URL, 4); !errors.Is(err, skillsync.ErrInvalidConfig) {
		t.Fatalf("redirect error = %v", err)
	}
	if _, _, err := (Source{BaseURL: server.URL, Client: server.Client(), MaxMetadataBytes: 64}).list(context.Background(), "bad-json", "x", 1); err == nil {
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
