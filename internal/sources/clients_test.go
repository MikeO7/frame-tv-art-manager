package sources

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testImageURL = "http://x.com/orig.jpg"

func TestNASAClient_FetchAPOD(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APODResponse{
			Title: "Test APOD",
			URL:   "http://x.com/a.jpg",
			HDURL: "http://x.com/hd.jpg",
			Type:  "image",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewNASAClient("key", slog.Default())
	c.BaseURL = server.URL

	apod, err := c.FetchAPOD(context.Background())
	if err != nil {
		t.Fatalf("FetchAPOD failed: %v", err)
	}

	if apod.Title != "Test APOD" {
		t.Errorf("expected Title Test APOD, got %s", apod.Title)
	}
}

func TestNASAClient_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search") {
			result := struct {
				Collection struct {
					Items []struct {
						Href string `json:"href"`
					} `json:"items"`
				} `json:"collection"`
			}{}
			result.Collection.Items = append(result.Collection.Items, struct {
				Href string `json:"href"`
			}{Href: "http://" + r.Host + "/manifest.json"})
			_ = json.NewEncoder(w).Encode(result)
		} else {
			manifest := []string{"http://x.com/image~orig.jpg"}
			_ = json.NewEncoder(w).Encode(manifest)
		}
	}))
	defer server.Close()

	c := NewNASAClient("key", slog.Default())
	c.SearchURL = server.URL

	urls, err := c.SearchNASAImageLibrary(context.Background(), "mars")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(urls) != 1 || urls[0] != "http://x.com/image~orig.jpg" {
		t.Errorf("expected 1 URL, got %v", urls)
	}
}

func TestArticClient_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Data []ArticArtwork `json:"data"`
		}{
			Data: []ArticArtwork{{ID: 456, ImageID: "a1"}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewArticClient(slog.Default())
	c.BaseURL = server.URL
	urls, err := c.Search(context.Background(), "monet")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestPixabayClient_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Hits []PixabayPhoto `json:"hits"`
		}{
			Hits: []PixabayPhoto{{ID: 101, LargeImageURL: testImageURL}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPixabayClient("key", slog.Default())
	c.BaseURL = server.URL
	urls, err := c.Search(context.Background(), "nature")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestArticClient_FetchPhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Data ArticArtwork `json:"data"`
		}{
			Data: ArticArtwork{ID: 1, Title: "Art", ImageID: "img123"},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewArticClient(slog.Default())
	c.BaseURL = server.URL

	url, err := c.FetchPhoto(context.Background(), "1")
	if err != nil {
		t.Fatalf("FetchPhoto failed: %v", err)
	}

	if url == "" || !contains(url, "img123") {
		t.Errorf("expected URL containing img123, got %s", url)
	}
}

func TestPexelsClient_FetchPhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		photo := PexelsPhoto{ID: 123}
		photo.Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(photo)
	}))
	defer server.Close()

	c := NewPexelsClient("key", slog.Default())
	c.BaseURL = server.URL

	url, err := c.FetchPhoto(context.Background(), "123")
	if err != nil {
		t.Fatalf("FetchPhoto failed: %v", err)
	}
	if url != testImageURL {
		t.Errorf("expected http://x.com/orig.jpg, got %s", url)
	}
}

func TestPixabayClient_FetchPhoto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Hits []PixabayPhoto `json:"hits"`
		}{
			Hits: []PixabayPhoto{{ID: 1, ImageURL: testImageURL}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPixabayClient("key", slog.Default())
	c.BaseURL = server.URL

	url, err := c.FetchPhoto(context.Background(), "1")
	if err != nil {
		t.Fatalf("FetchPhoto failed: %v", err)
	}
	if url != testImageURL {
		t.Errorf("expected http://x.com/orig.jpg, got %s", url)
	}
}

func TestPexelsClient_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Photos []PexelsPhoto `json:"photos"`
		}{
			Photos: []PexelsPhoto{{ID: 1}},
		}
		result.Photos[0].Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPexelsClient("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.Search(context.Background(), "nature")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(urls) != 1 || urls[0] != testImageURL {
		t.Errorf("expected 1 URL, got %v", urls)
	}
}

func TestPexelsClient_Curated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Photos []PexelsPhoto `json:"photos"`
		}{
			Photos: []PexelsPhoto{{ID: 1}},
		}
		result.Photos[0].Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPexelsClient("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.Curated(context.Background())
	if err != nil {
		t.Fatalf("Curated failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestPexelsClient_FetchCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Media []PexelsPhoto `json:"media"`
		}{
			Media: []PexelsPhoto{{ID: 1}},
		}
		result.Media[0].Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPexelsClient("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.FetchCollection(context.Background(), "abc")
	if err != nil {
		t.Fatalf("FetchCollection failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestPixabayClient_EditorsChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Hits []PixabayPhoto `json:"hits"`
		}{
			Hits: []PixabayPhoto{{ID: 1, LargeImageURL: testImageURL}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPixabayClient("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.EditorsChoice(context.Background())
	if err != nil {
		t.Fatalf("EditorsChoice failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestPixabayClient_User(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Hits []PixabayPhoto `json:"hits"`
		}{
			Hits: []PixabayPhoto{{ID: 1, LargeImageURL: testImageURL}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := NewPixabayClient("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.User(context.Background(), "user123")
	if err != nil {
		t.Fatalf("User failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func contains(s, substr string) bool {
	// Simple contains for tests
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
