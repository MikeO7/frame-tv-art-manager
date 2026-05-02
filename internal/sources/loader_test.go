package sources

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoader_Sync_Direct(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")

	// Mock server for direct downloads
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-image-data"))
	}))
	defer server.Close()

	content := fmt.Sprintf("# comment\n%s\n", server.URL)
	_ = os.WriteFile(sourcesFile, []byte(content), 0600)

	l := NewLoader(sourcesFile, artworkDir, "", "", "", "", "", "", 0, 0, slog.Default())
	downloaded, err := l.Sync()
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if downloaded != 1 {
		t.Errorf("expected 1 download, got %d", downloaded)
	}

	// Verify file exists
	files, _ := os.ReadDir(artworkDir)
	if len(files) != 1 {
		t.Errorf("expected 1 file in artwork dir, got %d", len(files))
	}
}

func TestLoader_UrlToSlug(t *testing.T) {
	l := &Loader{}
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/photo.jpg", "example.com_photo.jpg"},
		{"https://www.unsplash.com/123", "unsplash.com_123"},
		{"invalid-url", "direct-source"},
	}

	for _, tt := range tests {
		got := l.urlToSlug(tt.url)
		if got != tt.want {
			t.Errorf("urlToSlug(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

const testURL = "http://x.com/a"

func TestExtensionFromResponse(t *testing.T) {
	tests := []struct {
		ct   string
		url  string
		want string
	}{
		{"image/jpeg", testURL, ".jpg"},
		{"image/png", testURL, extPNG},
		{"text/plain", "http://x.com/a.png", extPNG},
		{"text/plain", testURL, ".jpg"}, // default
	}

	for _, tt := range tests {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Content-Type", tt.ct)
		got := extensionFromResponse(resp, tt.url)
		if got != tt.want {
			t.Errorf("extensionFromResponse(%q, %q) = %q, want %q", tt.ct, tt.url, got, tt.want)
		}
	}
}

func TestLoadSources_Yaml(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")

	yamlContent := `
providers:
  unsplash:
    - photo:123
    - collection:abc
  nasa:
    - apod
`
	_ = os.WriteFile(path, []byte(yamlContent), 0600)

	l := &Loader{sourcesFile: path}
	urls, err := l.loadSources()
	if err != nil {
		t.Fatalf("loadSources YAML failed: %v", err)
	}

	if len(urls) != 3 {
		t.Errorf("expected 3 URLs, got %d", len(urls))
	}
}

func TestLoadSources_Txt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.txt")

	txtContent := "direct:http://a.com/1.jpg\n# comment\nhttp://b.com/2.jpg\n"
	_ = os.WriteFile(path, []byte(txtContent), 0600)

	l := &Loader{sourcesFile: path}
	urls, err := l.loadSources()
	if err != nil {
		t.Fatalf("loadSources TXT failed: %v", err)
	}

	if len(urls) != 2 {
		t.Errorf("expected 2 URLs, got %d", len(urls))
	}
}

func TestLoader_InternalMethods(t *testing.T) {
	artworkDir := t.TempDir()

	// Create a file with specific content to test hashing
	path := filepath.Join(artworkDir, "test__1234567890ab.jpg")
	_ = os.WriteFile(path, []byte("some-data"), 0600)

	l := &Loader{
		artworkDir: artworkDir,
		logger:     slog.Default(),
		index:      make(map[string]string),
		prefixMap:  make(map[string]string),
		visited:    make(map[string]bool),
	}

	l.buildContentIndex()

	if len(l.prefixMap) != 1 {
		t.Errorf("expected 1 item in prefixMap, got %d", len(l.prefixMap))
	}

	// Test cleanup
	l.visited["test__1234567890ab.jpg"] = true
	l.cleanupUnusedSources()
	// Should not delete the visited file
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("visited file was accidentally deleted")
	}

	// Should delete unvisited file
	unvisitedPath := filepath.Join(artworkDir, "002__unvisited__hash.jpg")
	_ = os.WriteFile(unvisitedPath, []byte("x"), 0600)
	l.cleanupUnusedSources()
	if _, err := os.Stat(unvisitedPath); err == nil {
		t.Error("unvisited file was not deleted")
	}
}

func TestLoader_Sync_Failures(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")

	// Mock server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	content := server.URL + "\n"
	_ = os.WriteFile(sourcesFile, []byte(content), 0600)

	l := NewLoader(sourcesFile, artworkDir, "", "", "", "", "", "", 0, 0, slog.Default())
	downloaded, err := l.Sync()
	if err != nil {
		t.Fatalf("Sync should not fail on download error: %v", err)
	}

	if downloaded != 0 {
		t.Errorf("expected 0 downloads, got %d", downloaded)
	}
}

func TestLoader_UrlToSlug_Long(t *testing.T) {
	l := &Loader{}
	longURL := "https://example.com/" + strings.Repeat("a", 200)
	slug := l.urlToSlug(longURL)
	if len(slug) > 100 {
		t.Errorf("slug too long: %d", len(slug))
	}
}

func mockProviderHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case strings.Contains(path, "/photos") || strings.Contains(path, "/collections"): // Unsplash
		_, _ = w.Write([]byte(`[{"id": "u1", "links": {"download_location": "http://example.com/download"}}]`))
	case strings.Contains(path, "/search") && strings.Contains(r.URL.RawQuery, "nasa"): // NASA Search
		_, _ = w.Write([]byte(`{"collection": {"items": [{"href": "http://example.com/nasa1", "data": [{"nasa_id": "n1", "media_type": "image"}]}]}}`))
	case strings.Contains(path, "/artic"): // Artic
		_, _ = w.Write([]byte(`{"data": {"id": 456, "image_id": "a1"}}`))
	case strings.Contains(path, "/curated") || strings.Contains(path, "/collections/"): // Pexels
		_, _ = w.Write([]byte(`{"photos": [{"id": 789, "src": {"original": "http://example.com/p1.jpg"}}]}`))
	case (strings.Contains(path, "/api") || strings.Contains(path, "/?key=")) && (strings.Contains(r.URL.RawQuery, "editors_choice") || strings.Contains(r.URL.RawQuery, "q=") || strings.Contains(r.URL.RawQuery, "user=")): // Pixabay
		_, _ = w.Write([]byte(`{"hits": [{"id": 101, "largeImageURL": "http://example.com/pix1.jpg"}]}`))
	case strings.Contains(path, "/nasa1"): // NASA asset manifest
		_, _ = w.Write([]byte(`["http://example.com/nasa1.jpg"]`))
	case strings.Contains(path, "/apod"): // NASA APOD
		_, _ = w.Write([]byte(`{"url": "http://example.com/apod.jpg", "media_type": "image"}`))
	default:
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-image-data"))
	}
}

func TestLoader_Sync_Providers(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources_providers.txt")

	// Mock server for all providers
	server := httptest.NewServer(http.HandlerFunc(mockProviderHandler))
	defer server.Close()

	content := "unsplash:photo:123\nunsplash:collection:456\nnasa:apod\nnasa:search:mars\nartic:photo:456\npexels:curated\npexels:collection:789\npixabay:editors_choice\npixabay:search:nature\npixabay:user:mike\n"
	_ = os.WriteFile(sourcesFile, []byte(content), 0600)

	l := NewLoader(sourcesFile, artworkDir, "app", "key", "secret", "nasa", "pexels", "pixabay", 0, 0, slog.Default())
	// Override BaseURLs to point to our mock server
	l.unsplash.BaseURL = server.URL
	l.nasa.BaseURL = server.URL
	l.nasa.SearchURL = server.URL
	l.artic.BaseURL = server.URL
	l.pexels.BaseURL = server.URL
	l.pixabay.BaseURL = server.URL

	_, err := l.Sync()
	if err != nil {
		t.Fatalf("Sync with providers failed: %v", err)
	}
}

func TestLoader_Sync_Yaml(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.yaml")

	content := `
sources:
  - unsplash:photo:123
  - nasa:apod
`
	_ = os.WriteFile(sourcesFile, []byte(content), 0600)

	l := NewLoader(sourcesFile, artworkDir, "app", "key", "secret", "nasa", "pexels", "pixabay", 0, 0, slog.Default())

	// Mock server for downloads
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/photos") {
			_, _ = w.Write([]byte(`{"id": "u1", "links": {"download_location": "http://example.com/download"}}`))
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-image-data"))
		}
	}))
	defer server.Close()
	l.unsplash.BaseURL = server.URL

	_, _ = l.Sync()
}

func TestLoader_UtilityMethods(t *testing.T) {
	// truncateURL
	u := "https://example.com/very/long/path/to/image.jpg?query=param"
	trunc := truncateURL(u)
	if len(trunc) > 80 {
		t.Errorf("expected truncated URL, got %s", trunc)
	}

	// extensionFromResponse
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("Content-Type", "image/png")
	ext := extensionFromResponse(resp, "file.jpg")
	if ext != ".png" {
		t.Errorf("expected .png, got %s", ext)
	}

	resp.Header.Set("Content-Type", "application/octet-stream")
	ext = extensionFromResponse(resp, "file.png")
	if ext != ".png" {
		t.Errorf("expected .png from filename, got %s", ext)
	}
}

func TestLoader_handleArticLine_Search(t *testing.T) {
	artworkDir := t.TempDir()
	l := NewLoader("", artworkDir, "", "", "", "", "", "", 0, 0, slog.Default())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []any{
					map[string]any{"id": 1, "image_id": "img1"},
				},
			})
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-image-data"))
		}
	}))
	defer server.Close()
	l.artic.BaseURL = server.URL
	l.artic.IIIFBaseURL = server.URL

	var globalIndex int32
	count, err := l.handleArticLine("artic:search:monet", &globalIndex)
	if err != nil {
		t.Fatalf("handleArticLine failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 URL, got %d", count)
	}
}
