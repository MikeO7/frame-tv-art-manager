package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func newTestLoader(cfg *config.Config, logger *slog.Logger) *Loader {
	idx := NewArtworkIndex(cfg.ArtworkDir, logger)
	return NewLoader(cfg, logger, idx)
}

func setMockProviderURLs(l *Loader, baseURL string) {
	if p, ok := l.Provider("unsplash").(*unsplashProvider); ok {
		p.BaseURL = baseURL
	}
	if p, ok := l.Provider("nasa").(*nasaProvider); ok {
		p.BaseURL = baseURL
		p.SearchURL = baseURL
	}
	if p, ok := l.Provider("artic").(*articProvider); ok {
		p.BaseURL = baseURL
		p.IIIFBaseURL = baseURL
	}
	if p, ok := l.Provider("pexels").(*pexelsProvider); ok {
		p.BaseURL = baseURL
	}
	if p, ok := l.Provider("pixabay").(*pixabayProvider); ok {
		p.BaseURL = baseURL
	}
}

func TestLoader_Sync_Direct(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")

	// Mock server for direct downloads
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-image-data"))
	}))
	defer server.Close()

	content := fmt.Sprintf("# comment\n%s\n", server.URL)
	_ = os.WriteFile(sourcesFile, []byte(content), 0o600)

	l := newTestLoader(&config.Config{
		SourcesFile: sourcesFile,
		ArtworkDir:  artworkDir,
	}, slog.Default())
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
		{"image/jpeg", testURL, extJPG},
		{"image/png", testURL, extPNG},
		{"text/plain", "http://x.com/a.png", extPNG},
		{"text/plain", testURL, extJPG}, // default
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
	_ = os.WriteFile(path, []byte(yamlContent), 0o600)

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
	_ = os.WriteFile(path, []byte(txtContent), 0o600)

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

	path := filepath.Join(artworkDir, "test__1234567890ab.jpg")
	_ = os.WriteFile(path, []byte("some-data"), 0o600)

	idx := NewArtworkIndex(artworkDir, slog.Default())
	idx.Rebuild()

	if _, ok := idx.LookupPrefix("test"); !ok {
		t.Error("expected prefix entry for test identity")
	}

	idx.MarkVisited("test__1234567890ab.jpg")
	for _, filename := range idx.UnusedManagedFiles() {
		_ = os.Remove(filepath.Join(artworkDir, filename))
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("visited file was accidentally deleted")
	}

	unvisitedPath := filepath.Join(artworkDir, "002__unvisited__hash.jpg")
	_ = os.WriteFile(unvisitedPath, []byte("x"), 0o600)
	for _, filename := range idx.UnusedManagedFiles() {
		_ = os.Remove(filepath.Join(artworkDir, filename))
	}
	if _, err := os.Stat(unvisitedPath); err == nil {
		t.Error("unvisited file was not deleted")
	}
}

func TestLoader_Sync_Failures(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")

	// Mock server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	content := server.URL + "\n"
	_ = os.WriteFile(sourcesFile, []byte(content), 0o600)

	l := newTestLoader(&config.Config{
		SourcesFile: sourcesFile,
		ArtworkDir:  artworkDir,
	}, slog.Default())
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
	_ = os.WriteFile(sourcesFile, []byte(content), 0o600)

	l := newTestLoader(&config.Config{
		SourcesFile:       sourcesFile,
		ArtworkDir:        artworkDir,
		UnsplashAppID:     "app",
		UnsplashAccessKey: "key",
		UnsplashSecretKey: "secret",
		NasaAPIKey:        providerNASA,
		PexelsAPIKey:      providerPexels,
		PixabayAPIKey:     providerPixabay,
	}, slog.Default())
	setMockProviderURLs(l, server.URL)

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
	_ = os.WriteFile(sourcesFile, []byte(content), 0o600)

	l := newTestLoader(&config.Config{
		SourcesFile:       sourcesFile,
		ArtworkDir:        artworkDir,
		UnsplashAppID:     "app",
		UnsplashAccessKey: "key",
		UnsplashSecretKey: "secret",
		NasaAPIKey:        providerNASA,
		PexelsAPIKey:      providerPexels,
		PixabayAPIKey:     providerPixabay,
	}, slog.Default())

	// Mock server for downloads
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/photos") {
			_, _ = w.Write([]byte(`{"id": "u1", "links": {"download_location": "http://example.com/download"}}`))
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-image-data"))
		}
	}))
	defer server.Close()
	setMockProviderURLs(l, server.URL)

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
	if ext != extPNG {
		t.Errorf("expected %s, got %s", extPNG, ext)
	}

	resp.Header.Set("Content-Type", "application/octet-stream")
	ext = extensionFromResponse(resp, "file"+extPNG)
	if ext != extPNG {
		t.Errorf("expected %s from filename, got %s", extPNG, ext)
	}
}

func TestLoader_syncLine_ArticSearch(t *testing.T) {
	artworkDir := t.TempDir()
	l := newTestLoader(&config.Config{
		ArtworkDir: artworkDir,
	}, slog.Default())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
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
	setMockProviderURLs(l, server.URL)

	var globalIndex int32
	count, err := l.syncLine(context.Background(), "artic:search:monet", &globalIndex)
	if err != nil {
		t.Fatalf("syncLine failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 URL, got %d", count)
	}
}

func TestLoader_executeDownload(t *testing.T) {
	artworkDir := t.TempDir()
	l := newTestLoader(&config.Config{
		ArtworkDir:        artworkDir,
		MaxDownloadSizeMB: 1,
	}, slog.Default())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	}))
	defer server.Close()

	downloaded, err := l.executeDownload(context.Background(), server.URL, "000__direct__test.jpg")
	if err != nil {
		t.Fatalf("executeDownload failed: %v", err)
	}
	if !downloaded {
		t.Error("expected downloaded=true")
	}

	entries, err := os.ReadDir(artworkDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Errorf("expected png extension, got %q", entries[0].Name())
	}
}

func TestLoader_downloadWithIdentity_MaxReached(t *testing.T) {
	artworkDir := t.TempDir()
	l := newTestLoader(&config.Config{
		ArtworkDir:       artworkDir,
		MaxArtworkImages: 1,
	}, slog.Default())
	l.index.MarkVisited("existing.jpg")

	downloaded, err := l.downloadWithIdentity(context.Background(), "http://example.com/x.jpg", "001__direct__x")
	if err != nil {
		t.Fatal(err)
	}
	if downloaded {
		t.Error("expected skip when max reached")
	}
}

func TestLoader_loadYamlSources_Invalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte("not: valid: yaml: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())
	l.sourcesFile = path
	_, err := l.loadYamlSources()
	if err == nil {
		t.Error("expected error for invalid yaml format")
	}
}
