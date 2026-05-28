package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

	c.TrackDownload(context.Background(), "https://api.unsplash.com/track")
	if !bytes.Contains(buf.Bytes(), []byte("invalid unsplash download location host")) {
		t.Error("expected invalid host warning in logs due to bad BaseURL")
	}
}
