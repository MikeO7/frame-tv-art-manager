package sources

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnsplashClient_FetchPhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/photos/123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		photo := UnsplashPhoto{ID: "123", Width: 100, Height: 100}
		_ = json.NewEncoder(w).Encode(photo)
	}))
	defer server.Close()

	c := NewUnsplashClient("app", "key", "secret", slog.Default())
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
		photos := []UnsplashPhoto{{ID: "1"}, {ID: "2"}}
		_ = json.NewEncoder(w).Encode(photos)
	}))
	defer server.Close()

	c := NewUnsplashClient("app", "key", "secret", slog.Default())
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
		if r.URL.Path == "/track" {
			tracked = true
		}
	}))
	defer server.Close()

	c := NewUnsplashClient("app", "key", "secret", slog.Default())
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
		pages++
		if pages == 1 {
			// Return a full page
			photos := make([]UnsplashPhoto, 30)
			for i := 0; i < 30; i++ {
				photos[i] = UnsplashPhoto{ID: "p1"}
			}
			_ = json.NewEncoder(w).Encode(photos)
		} else {
			// Return a partial page to end
			photos := []UnsplashPhoto{{ID: "p2"}}
			_ = json.NewEncoder(w).Encode(photos)
		}
	}))
	defer server.Close()

	c := NewUnsplashClient("app", "key", "secret", slog.Default())
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
