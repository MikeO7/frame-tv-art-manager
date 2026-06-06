package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnsplashClient_FetchPhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		if r.URL.Path != "/photos/123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		photo := unsplashPhoto{ID: "123", Width: 100, Height: 100}
		_ = json.NewEncoder(w).Encode(photo)
	}))
	defer server.Close()

	c := newUnsplashClient("app", "key", "secret", slog.Default())
	c.BaseURL = server.URL

	photo, err := c.FetchPhoto(context.Background(), "123")
	if err != nil {
		t.Fatalf("FetchPhoto failed: %v", err)
	}

	if photo.ID != "123" {
		t.Errorf("expected ID 123, got %s", photo.ID)
	}
}

func TestUnsplashClient_FetchCollectionPhotos(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		photos := []unsplashPhoto{{ID: "1"}, {ID: "2"}}
		_ = json.NewEncoder(w).Encode(photos)
	}))
	defer server.Close()

	c := newUnsplashClient("app", "key", "secret", slog.Default())
	c.BaseURL = server.URL

	photos, err := c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err != nil {
		t.Fatalf("FetchCollectionPhotos failed: %v", err)
	}

	if len(photos) != 2 {
		t.Errorf("expected 2 photos, got %d", len(photos))
	}
}

func TestUnsplashClient_TrackDownload(t *testing.T) {
	tracked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		if r.URL.Path == "/track" {
			tracked = true
		}
	}))
	defer server.Close()

	c := newUnsplashClient("app", "key", "secret", slog.Default())
	// Set BaseURL to the test server to pass validation
	c.BaseURL = server.URL
	c.TrackDownload(context.Background(), server.URL+"/track")

	if !tracked {
		t.Error("expected track request to be sent")
	}
}

func TestUnsplashClient_FetchCollectionPhotos_Pagination(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		pages++
		if pages == 1 {
			// Return a full page
			photos := make([]unsplashPhoto, 30)
			for i := 0; i < 30; i++ {
				photos[i] = unsplashPhoto{ID: "p1"}
			}
			_ = json.NewEncoder(w).Encode(photos)
		} else {
			// Return a partial page to end
			photos := []unsplashPhoto{{ID: "p2"}}
			_ = json.NewEncoder(w).Encode(photos)
		}
	}))
	defer server.Close()

	c := newUnsplashClient("app", "key", "secret", slog.Default())
	c.BaseURL = server.URL

	photos, err := c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err != nil {
		t.Fatalf("FetchCollectionPhotos failed: %v", err)
	}

	if len(photos) != 31 {
		t.Errorf("expected 31 photos, got %d", len(photos))
	}
	if pages != 2 {
		t.Errorf("expected 2 pages fetched, got %d", pages)
	}
}

func TestUnsplashClient_TrackDownload_Errors(t *testing.T) {
	logger, buf := newTestLogger()
	c := newUnsplashClient("app", "key", "secret", logger)
	c.BaseURL = "https://api.unsplash.com"

	// 1. Test invalid URL format
	c.TrackDownload(context.Background(), "://invalid-url")
	if !bytes.Contains(buf.Bytes(), []byte("invalid unsplash download location URL format")) {
		t.Error("expected invalid URL format warning in logs")
	}
	buf.Reset()

	// 2. Test invalid host
	c.TrackDownload(context.Background(), "https://example.com/track")
	if !bytes.Contains(buf.Bytes(), []byte("invalid unsplash download location host")) {
		t.Error("expected invalid host warning in logs")
	}
	buf.Reset()

	// 3. Test request creation error
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.TrackDownload(ctx, "https://api.unsplash.com/track")
	if bytes.Contains(buf.Bytes(), []byte("unsplash download tracked")) {
		t.Error("expected track request not to succeed on bad context")
	}
	buf.Reset()

	// 4. Test client execution error
	c.BaseURL = "invalid-scheme://api.unsplash.com"
	c.TrackDownload(context.Background(), "invalid-scheme://api.unsplash.com/track")
	if bytes.Contains(buf.Bytes(), []byte("unsplash download tracked")) {
		t.Error("expected track request not to succeed on bad client request")
	}
}

func TestUnsplashClient_TrackDownload_InvalidBaseURL(t *testing.T) {
	logger, buf := newTestLogger()
	c := newUnsplashClient("app", "key", "secret", logger)
	c.BaseURL = "://invalid-base-url"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.TrackDownload(ctx, "https://api.unsplash.com/track")
	if !bytes.Contains(buf.Bytes(), []byte("invalid unsplash download location host")) {
		t.Error("expected invalid host warning in logs due to bad BaseURL")
	}
}

func TestUnsplashClient_FetchCollectionPhotos_Errors(t *testing.T) {
	c := newUnsplashClient("app", "key", "secret", slog.Default())

	// 1. NewRequestWithContext error (bad URL)
	c.BaseURL = "://invalid"
	_, err := c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err == nil {
		t.Error("expected error for bad URL")
	}

	// 2. client.Do error (bad scheme)
	c.BaseURL = "invalid-scheme://example.com"
	_, err = c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err == nil {
		t.Error("expected error for bad scheme")
	}

	// 3. non-200 status code
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()
	c.BaseURL = server500.URL
	_, err = c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unsplash api error")) {
		t.Errorf("expected unsplash api error, got: %v", err)
	}

	// 4. bad JSON response
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer serverBadJSON.Close()
	c.BaseURL = serverBadJSON.URL
	_, err = c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("decode unsplash response")) {
		t.Errorf("expected decode error, got: %v", err)
	}
}

func TestUnsplashClient_FetchCollectionPhotos_MaxPages(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		// Always return exactly 30 photos so it keeps going
		photos := make([]unsplashPhoto, 30)
		for i := 0; i < 30; i++ {
			photos[i] = unsplashPhoto{ID: "p1"}
		}
		_ = json.NewEncoder(w).Encode(photos)
	}))
	defer server.Close()

	c := newUnsplashClient("app", "key", "secret", slog.Default())
	c.BaseURL = server.URL

	photos, err := c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err != nil {
		t.Fatalf("FetchCollectionPhotos failed: %v", err)
	}

	if len(photos) != 33*30 { // Should stop after 33 pages
		t.Errorf("expected 990 photos, got %d", len(photos))
	}
	if pages != 33 {
		t.Errorf("expected 33 pages fetched, got %d", pages)
	}
}

func TestUnsplashClient_FetchPhoto_Errors(t *testing.T) {
	c := newUnsplashClient("app", "key", "secret", slog.Default())

	// 1. NewRequestWithContext error (bad URL)
	c.BaseURL = "://invalid"
	_, err := c.FetchPhoto(context.Background(), "123")
	if err == nil {
		t.Error("expected error for bad URL")
	}

	// 2. client.Do error (bad scheme)
	c.BaseURL = "invalid-scheme://example.com"
	_, err = c.FetchPhoto(context.Background(), "123")
	if err == nil {
		t.Error("expected error for bad scheme")
	}

	// 3. non-200 status code
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()
	c.BaseURL = server500.URL
	_, err = c.FetchPhoto(context.Background(), "123")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unsplash api error")) {
		t.Errorf("expected unsplash api error, got: %v", err)
	}

	// 4. bad JSON response
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer serverBadJSON.Close()
	c.BaseURL = serverBadJSON.URL
	_, err = c.FetchPhoto(context.Background(), "123")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("decode unsplash response")) {
		t.Errorf("expected decode error, got: %v", err)
	}
}

func TestUnsplashProvider_Resolve(t *testing.T) {
	var globalIndex int32 = 0
	c := newUnsplashClient("app", "key", "secret", slog.Default())
	p := newUnsplashProvider("app", "key", "secret", slog.Default())
	p.client = c.client
	p.BaseURL = c.BaseURL
	p.accessKey = c.accessKey

	// Test missing accessKey
	p.accessKey = ""
	_, err := p.Resolve(context.Background(), "unsplash:photo:123", &globalIndex)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("UNSPLASH_ACCESS_KEY not configured")) {
		t.Errorf("expected config error, got: %v", err)
	}
	p.accessKey = "key"

	// Test invalid format
	_, err = p.Resolve(context.Background(), "unsplash:photo", &globalIndex)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("invalid unsplash format")) {
		t.Errorf("expected format error, got: %v", err)
	}

	// Test unknown type
	_, err = p.Resolve(context.Background(), "unsplash:unknown:123", &globalIndex)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unknown unsplash type")) {
		t.Errorf("expected unknown type error, got: %v", err)
	}

	// Test collection resolving error
	p.BaseURL = "invalid-scheme://example.com"
	_, err = p.Resolve(context.Background(), "unsplash:collection:col-abc", &globalIndex)
	if err == nil {
		t.Error("expected fetch collection error")
	}

	// Test photo resolving error
	_, err = p.Resolve(context.Background(), "unsplash:photo:123", &globalIndex)
	if err == nil {
		t.Error("expected fetch photo error")
	}

	// Test successful resolve with long slug and download tracking
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/photos/123" {
			photo := unsplashPhoto{ID: "long-id-" + string(make([]byte, 100)), Width: 100, Height: 100}
			photo.URLs.Raw = "https://example.com/raw"
			photo.Links.DownloadLocation = "https://example.com/download"
			_ = json.NewEncoder(w).Encode(photo)
		}
	}))
	defer server.Close()
	p.BaseURL = server.URL

	images, err := p.Resolve(context.Background(), "unsplash:photo:123", &globalIndex)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if len(images[0].Identity) > 120 { // 000__unsplash__ + 100 chars
		t.Errorf("expected truncated slug, got identity: %s", images[0].Identity)
	}

	// Trigger OnDownload
	err = images[0].OnDownload(context.Background())
	if err != nil {
		t.Errorf("OnDownload failed: %v", err)
	}
}

func TestUnsplashClient_TrackDownload_ContextError(t *testing.T) {
	logger, buf := newTestLogger()
	c := newUnsplashClient("app", "key", "secret", logger)
	c.BaseURL = "https://api.unsplash.com"

	// http.NewRequestWithContext error
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.TrackDownload(ctx, "https://api.unsplash.com/track")

	if bytes.Contains(buf.Bytes(), []byte("unsplash download tracked")) {
		t.Error("expected error to skip success log")
	}
}

func TestUnsplashClient_FetchCollectionPhotos_EmptyPageBreak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]unsplashPhoto{})
	}))
	defer server.Close()

	c := newUnsplashClient("app", "key", "secret", slog.Default())
	c.BaseURL = server.URL

	photos, err := c.FetchCollectionPhotos(context.Background(), "col-abc")
	if err != nil {
		t.Fatalf("FetchCollectionPhotos failed: %v", err)
	}

	if len(photos) != 0 {
		t.Errorf("expected 0 photos, got %d", len(photos))
	}
}

func TestUnsplashProvider_Resolve_PhotoError(t *testing.T) {
	var globalIndex int32 = 0
	c := newUnsplashClient("app", "key", "secret", slog.Default())
	p := newUnsplashProvider("app", "key", "secret", slog.Default())
	p.client = c.client
	p.accessKey = c.accessKey

	// Server returns 500 for photo
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	p.BaseURL = server.URL

	_, err := p.Resolve(context.Background(), "unsplash:photo:123", &globalIndex)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unsplash api error")) {
		t.Errorf("expected unsplash api error for photo resolve, got: %v", err)
	}
}

func TestUnsplashProvider_Resolve_SlugLength(t *testing.T) {
	var globalIndex int32 = 0
	c := newUnsplashClient("app", "key", "secret", slog.Default())
	p := newUnsplashProvider("app", "key", "secret", slog.Default())
	p.client = c.client
	p.accessKey = c.accessKey

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return normal photo
		photo := unsplashPhoto{ID: "short-id", Width: 100, Height: 100}
		photo.URLs.Raw = "https://example.com/raw"
		_ = json.NewEncoder(w).Encode(photo)
	}))
	defer server.Close()
	p.BaseURL = server.URL

	images, err := p.Resolve(context.Background(), "unsplash:photo:123", &globalIndex)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// length check for identity which should not be truncated
	expectedSlug := "123-short-id"
	expectedIdentity := fmt.Sprintf("%03d__unsplash__%s", 0, expectedSlug)
	if images[0].Identity != expectedIdentity {
		t.Errorf("expected identity %v, got %v", expectedIdentity, images[0].Identity)
	}
}

func TestUnsplashProvider_Resolve_CollectionError(t *testing.T) {
	var globalIndex int32 = 0
	c := newUnsplashClient("app", "key", "secret", slog.Default())
	p := newUnsplashProvider("app", "key", "secret", slog.Default())
	p.client = c.client
	p.accessKey = c.accessKey

	// Server returns 500 for collection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	p.BaseURL = server.URL

	_, err := p.Resolve(context.Background(), "unsplash:collection:col-abc", &globalIndex)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unsplash api error")) {
		t.Errorf("expected unsplash api error for collection resolve, got: %v", err)
	}
}

func TestUnsplashProvider_Resolve_SlugLengthTruncation(t *testing.T) {
	var globalIndex int32 = 0
	c := newUnsplashClient("app", "key", "secret", slog.Default())
	p := newUnsplashProvider("app", "key", "secret", slog.Default())
	p.client = c.client
	p.accessKey = c.accessKey

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return normal photo
		photo := unsplashPhoto{ID: string(bytes.Repeat([]byte("a"), 105)), Width: 100, Height: 100} // make ID longer than 100
		photo.URLs.Raw = "https://example.com/raw"
		_ = json.NewEncoder(w).Encode(photo)
	}))
	defer server.Close()
	p.BaseURL = server.URL

	images, err := p.Resolve(context.Background(), "unsplash:photo:123", &globalIndex)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Check truncation
	// Format is idx__unsplash__slug
	// Identity is "%03d__unsplash__%s", idx, slug (slug is truncated to 100)
	expectedSlug := "123-" + string(bytes.Repeat([]byte("a"), 105))
	expectedSlug = strings.ReplaceAll(expectedSlug, " ", "-")
	if len(expectedSlug) > 100 {
		expectedSlug = expectedSlug[:100]
	}
	expectedIdentity := fmt.Sprintf("%03d__unsplash__%s", 0, expectedSlug)
	if images[0].Identity != expectedIdentity {
		t.Errorf("expected identity %v, got %v", expectedIdentity, images[0].Identity)
	}
}
