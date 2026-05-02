package sources

import (
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

func TestExtensionFromResponse(t *testing.T) {
	tests := []struct {
		ct   string
		url  string
		want string
	}{
		{"image/jpeg", "http://x.com/a", ".jpg"},
		{"image/png", "http://x.com/a", ".png"},
		{"text/plain", "http://x.com/a.png", ".png"},
		{"text/plain", "http://x.com/a", ".jpg"}, // default
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
