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
