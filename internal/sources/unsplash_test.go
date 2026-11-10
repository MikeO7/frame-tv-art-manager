package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUnsplashResolveSendsVersionAndRequestsBoundedRendition(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Version") != "v1" || r.Header.Get(headerAuthorization) != "Client-ID key" {
			t.Errorf("headers = %v", r.Header)
		}
		switch r.URL.Path {
		case "/photos/abc":
			_, _ = w.Write([]byte(`{"id":"abc","width":4000,"height":6000,"alt_description":"A lake",
				"user":{"name":"Jo"},"links":{"html":"https://unsplash.com/photos/abc","download_location":"https://api.unsplash.example/photos/abc/download"},
				"urls":{"raw":"https://images.unsplash.com/photo-abc?ixid=xyz"}}`))
		case "/photos/empty":
			_, _ = w.Write([]byte(`{"id":"empty","urls":{"raw":""}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := newUnsplashProvider("key", discardLogger())
	provider.BaseURL = server.URL

	images, err := provider.Resolve(context.Background(), "unsplash:photo:abc")
	if err != nil || len(images) != 1 {
		t.Fatalf("Resolve() = (%+v, %v)", images, err)
	}
	rendition, _ := url.Parse(images[0].URL)
	query := rendition.Query()
	if query.Get("fit") != "max" || query.Get("w") != "3840" || query.Get("fm") != "jpg" || query.Get("ixid") != "xyz" {
		t.Errorf("rendition URL = %s, want fit=max, w=3840, and the original raw parameters", images[0].URL)
	}
	if images[0].Identity != sourceIdentity("unsplash", "abc", "abc-abc") {
		t.Errorf("identity = %q, want the stable photo-ID identity", images[0].Identity)
	}
	if images[0].TrackURL != "https://api.unsplash.example/photos/abc/download" {
		t.Errorf("track URL = %q", images[0].TrackURL)
	}
	want := attribution{
		Provider: "unsplash", Title: "A lake", Artist: "Jo",
		SourceURL: "https://unsplash.com/photos/abc?" + unsplashReferral, License: unsplashLicense,
	}
	if images[0].Attribution != want {
		t.Errorf("attribution = %+v, want %+v", images[0].Attribution, want)
	}
	if _, err := provider.Resolve(context.Background(), "unsplash:photo:empty"); err == nil {
		t.Error("Resolve() accepted a photo without a raw URL")
	}
}

func TestUnsplashResolveRejectsMissingKeyAndMalformedLines(t *testing.T) {
	t.Parallel()
	if _, err := newUnsplashProvider("", discardLogger()).Resolve(context.Background(), "unsplash:photo:x"); err == nil ||
		!strings.Contains(err.Error(), "UNSPLASH_ACCESS_KEY") {
		t.Fatalf("Resolve() without key error = %v", err)
	}
	provider := newUnsplashProvider("key", discardLogger())
	for line, want := range map[string]string{"unsplash:photo": "invalid unsplash format", "unsplash:other:x": "unknown unsplash type"} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}

func TestUnsplashCollectionPaginatesAndSkipsUnusablePhotos(t *testing.T) {
	t.Parallel()
	var pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page := pages.Add(1)
		count := unsplashPageSize
		if page == 2 {
			count = 1
		}
		photos := make([]map[string]any, count)
		for index := range photos {
			photos[index] = map[string]any{
				"id": fmt.Sprintf("p%d-%d", page, index), "urls": map[string]string{"raw": "https://images.unsplash.com/x"},
			}
		}
		if page == 2 {
			photos = append(photos, map[string]any{"id": ""})
		}
		_ = json.NewEncoder(w).Encode(photos)
	}))
	defer server.Close()
	provider := newUnsplashProvider("key", discardLogger())
	provider.BaseURL = server.URL

	images, err := provider.Resolve(context.Background(), "unsplash:collection:col")
	if err != nil || len(images) != unsplashPageSize+1 || pages.Load() != 2 {
		t.Fatalf("Resolve() = %d images over %d pages, %v; want %d over 2", len(images), pages.Load(), err, unsplashPageSize+1)
	}
}

func TestUnsplashTrackDownloadReportsFailures(t *testing.T) {
	t.Parallel()
	var tracked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Version") != "v1" {
			t.Errorf("tracking request omitted Accept-Version")
		}
		tracked.Add(1)
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"url":"https://images.unsplash.com/x"}`))
	}))
	defer server.Close()
	provider := newUnsplashProvider("key", discardLogger())
	provider.BaseURL = server.URL

	if err := provider.TrackDownload(context.Background(), server.URL+"/track"); err != nil {
		t.Fatalf("TrackDownload() error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		location string
		want     string
	}{
		{name: "non-success status", ctx: context.Background(), location: server.URL + "/fail", want: "tracking error: 403"},
		{name: "malformed URL", ctx: context.Background(), location: "://bad", want: "invalid unsplash download location"},
		{name: "foreign host", ctx: context.Background(), location: "https://example.com/track", want: "not on the API host"},
		{name: "canceled", ctx: canceled, location: server.URL + "/track", want: "context canceled"},
	}
	for _, test := range tests {
		if err := provider.TrackDownload(test.ctx, test.location); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: TrackDownload() error = %v, want %q", test.name, err, test.want)
		}
	}
	if tracked.Load() != 2 {
		t.Fatalf("tracking requests = %d, want only the two on-host requests", tracked.Load())
	}
}
