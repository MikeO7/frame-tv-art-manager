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

func TestPixabayProvider_FetchPhoto_Errors(t *testing.T) {
	// Test error from fetchPhotoList (simulate 500 server error)
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()

	c := newPixabayProvider("key", slog.Default())
	c.BaseURL = server500.URL
	_, err := c.FetchPhoto(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "pixabay api error") {
		t.Errorf("expected 500 error, got %v", err)
	}

	// Test photo not found (0 hits)
	serverEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Hits []pixabayPhoto `json:"hits"`
		}{
			Hits: []pixabayPhoto{},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer serverEmpty.Close()

	c.BaseURL = serverEmpty.URL
	_, err = c.FetchPhoto(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "pixabay photo not found") {
		t.Errorf("expected not found error, got %v", err)
	}
}

func TestPixabayProvider_fetchAllPages_ErrorsAndLimits(t *testing.T) {
	// 1. Error fetching list
	serverErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverErr.Close()

	c := newPixabayProvider("key", slog.Default())
	_, err := c.fetchAllPages(context.Background(), serverErr.URL+"?k=v")
	if err == nil {
		t.Errorf("expected error fetching list, got nil")
	}

	// 2. Max pages limit (> 5 pages)
	pagesRequested := 0
	serverLimit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pagesRequested++
		// Always return 200 items to keep going to next page
		hits := make([]pixabayPhoto, 200)
		for i := 0; i < 200; i++ {
			hits[i] = pixabayPhoto{ID: i, ImageURL: "http://example.com/img.jpg"}
		}
		result := struct {
			Hits []pixabayPhoto `json:"hits"`
		}{Hits: hits}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer serverLimit.Close()

	urls, err := c.fetchAllPages(context.Background(), serverLimit.URL+"?k=v")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(urls) != 200*5 {
		t.Errorf("expected 1000 items (5 pages of 200), got %d", len(urls))
	}
	if pagesRequested != 5 {
		t.Errorf("expected 5 pages requested, got %d", pagesRequested)
	}
}

func TestPixabayProvider_fetchPhotoList_ErrorsAndParsing(t *testing.T) {
	c := newPixabayProvider("key", slog.Default())

	// 1. Context cancel (NewRequest error / Do error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.fetchPhotoList(ctx, "http://example.com")
	if err == nil {
		t.Error("expected error from canceled context, got nil")
	}

	// 2. Bad URL format (Do error)
	_, err = c.fetchPhotoList(context.Background(), "invalid-url://example")
	if err == nil {
		t.Error("expected error from bad URL, got nil")
	}

    // 3. Bad request format (NewRequest error)
    _, err = c.fetchPhotoList(context.Background(), "://example")
    if err == nil {
		t.Error("expected error from bad URL, got nil")
	}

	// 4. Bad JSON
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{invalid-json"))
	}))
	defer serverBadJSON.Close()

	_, err = c.fetchPhotoList(context.Background(), serverBadJSON.URL)
	if err == nil || !strings.Contains(err.Error(), "decode pixabay response") {
		t.Errorf("expected JSON decode error, got %v", err)
	}

	// 5. Image priority parsing
	serverPriority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		hits := []pixabayPhoto{
			{ID: 1, ImageURL: "image1", FullHDURL: "full1", LargeImageURL: "large1"}, // should pick ImageURL
			{ID: 2, ImageURL: "", FullHDURL: "full2", LargeImageURL: "large2"},       // should pick FullHDURL
			{ID: 3, ImageURL: "", FullHDURL: "", LargeImageURL: "large3"},            // should pick LargeImageURL
			{ID: 4, ImageURL: "", FullHDURL: "", LargeImageURL: ""},                  // should pick nothing
		}
		result := struct {
			Hits []pixabayPhoto `json:"hits"`
		}{Hits: hits}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer serverPriority.Close()

	urls, err := c.fetchPhotoList(context.Background(), serverPriority.URL)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(urls) != 3 {
		t.Fatalf("expected 3 urls, got %d", len(urls))
	}
	if urls[0] != "image1" || urls[1] != "full2" || urls[2] != "large3" {
		t.Errorf("unexpected priority choices: %v", urls)
	}
}

func TestPixabayProvider_Resolve_ErrorsAndPaths(t *testing.T) {
	c := newPixabayProvider("key", slog.Default())

	var globalIndex int32 = 0

	// 1. Invalid format (no colon)
	_, err := c.Resolve(context.Background(), "pixabay", &globalIndex)
	if err == nil || !strings.Contains(err.Error(), "invalid pixabay format") {
		t.Errorf("expected invalid format error, got %v", err)
	}

	// 2. Search without query
	_, err = c.Resolve(context.Background(), "pixabay:search", &globalIndex)
	if err == nil || !strings.Contains(err.Error(), "search requires a query") {
		t.Errorf("expected search query error, got %v", err)
	}

	// 3. Photo without ID
	_, err = c.Resolve(context.Background(), "pixabay:photo", &globalIndex)
	if err == nil || !strings.Contains(err.Error(), "photo requires an ID") {
		t.Errorf("expected photo ID error, got %v", err)
	}

	// 4. User without ID
	_, err = c.Resolve(context.Background(), "pixabay:user", &globalIndex)
	if err == nil || !strings.Contains(err.Error(), "user requires an ID") {
		t.Errorf("expected user ID error, got %v", err)
	}

	// 5. Unknown command
	_, err = c.Resolve(context.Background(), "pixabay:unknown_cmd:123", &globalIndex)
	if err == nil || !strings.Contains(err.Error(), "unknown pixabay type") {
		t.Errorf("expected unknown type error, got %v", err)
	}

	// 6. Underlying fetch error
	// Use an invalid server to trigger fetch error for search
	c.BaseURL = "invalid-url://foo"
	_, err = c.Resolve(context.Background(), "pixabay:search:mars", &globalIndex)
	if err == nil {
		t.Error("expected fetch error to propagate up, got nil")
	}

	// 7. Successful resolve path check
	serverSuccess := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits := []pixabayPhoto{{ID: 1, ImageURL: "http://example.com/img.jpg"}}
		result := struct {
			Hits []pixabayPhoto `json:"hits"`
		}{Hits: hits}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer serverSuccess.Close()

	c.BaseURL = serverSuccess.URL
	images, err := c.Resolve(context.Background(), "pixabay:photo:123", &globalIndex)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if !strings.Contains(images[0].Identity, "__pixabay__") {
		t.Errorf("expected identity to contain __pixabay__, got %s", images[0].Identity)
	}
}

func TestPixabayProvider_fetchAllPages_BreakOnEmpty(t *testing.T) {
	serverEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Hits []pixabayPhoto `json:"hits"`
		}{
			Hits: []pixabayPhoto{},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer serverEmpty.Close()

	c := newPixabayProvider("key", slog.Default())
	urls, err := c.fetchAllPages(context.Background(), serverEmpty.URL+"?k=v")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(urls) != 0 {
		t.Errorf("expected 0 urls, got %d", len(urls))
	}
}
