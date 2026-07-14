package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func newTestLoader(cfg *config.Config, logger *slog.Logger) *Loader {
	idx := NewArtworkCatalog(cfg.ArtworkDir, logger)
	store, err := collection.New(collection.Config{
		Root: cfg.ArtworkDir, MaxItems: cfg.MaxArtworkImages,
		MaxImportBytes: int64(cfg.MaxDownloadSizeMB) * bytesPerMB,
	})
	if err != nil {
		panic(err)
	}
	return NewLoader(cfg, logger, idx, store)
}

func testJPEGBytes(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatalf("encode JPEG fixture: %v", err)
	}
	return buffer.Bytes()
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return buffer.Bytes()
}

func setMockProviderURLs(l *Loader, baseURL string) {
	for _, provider := range l.providers {
		switch value := provider.(type) {
		case *unsplashProvider:
			value.BaseURL = baseURL
		case *nasaProvider:
			value.BaseURL, value.SearchURL = baseURL, baseURL
		case *articProvider:
			value.BaseURL, value.IIIFBaseURL = baseURL, baseURL
		case *pexelsProvider:
			value.BaseURL = baseURL
		case *pixabayProvider:
			value.BaseURL = baseURL
		}
	}
}

func TestLoader_Sync_Direct(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")

	// Mock server for direct downloads
	imageData := testJPEGBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imageData)
	}))
	defer server.Close()

	content := fmt.Sprintf("# comment\n%s\n", server.URL)
	_ = os.WriteFile(sourcesFile, []byte(content), 0o600)

	l := newTestLoader(&config.Config{
		SourcesFile: sourcesFile,
		ArtworkDir:  artworkDir,
	}, slog.Default())
	downloaded, err := l.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if downloaded != 1 {
		t.Errorf("expected 1 download, got %d", downloaded)
	}

	// Verify file exists
	files, catalogErr := l.index.SupportedFiles()
	if catalogErr != nil || len(files) != 1 {
		t.Errorf("expected 1 artwork file, got %v (%v)", files, catalogErr)
	}
}

func TestLoaderSyncDoesNotExposeRawSourceCredentials(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")
	const sensitiveLine = "unsplash:unknown?api_key=source-secret"
	if err := os.WriteFile(sourcesFile, []byte(sensitiveLine+"\n"), 0o600); err != nil {
		t.Fatalf("write sources: %v", err)
	}
	logger, logs := newTestLogger()
	loader := newTestLoader(&config.Config{
		SourcesFile: sourcesFile, ArtworkDir: artworkDir, UnsplashAccessKey: "configured-key",
	}, logger)
	_, err := loader.Sync(context.Background())
	if err == nil {
		t.Fatal("expected invalid source error")
	}
	combined := logs.String() + err.Error()
	for _, secret := range []string{sensitiveLine, "source-secret", "api_key"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("source diagnostics exposed %q: %s", secret, combined)
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
	urls, err := l.loadSources(context.Background())
	if err != nil {
		t.Fatalf("loadSources YAML failed: %v", err)
	}

	want := []string{"nasa:apod", "unsplash:photo:123", "unsplash:collection:abc"}
	if !slices.Equal(urls, want) {
		t.Errorf("YAML sources = %v, want deterministic %v", urls, want)
	}
}

func TestLoadSources_Txt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.txt")

	txtContent := "direct:http://a.com/1.jpg\n# comment\nhttp://b.com/2.jpg\n"
	_ = os.WriteFile(path, []byte(txtContent), 0o600)

	l := &Loader{sourcesFile: path}
	urls, err := l.loadSources(context.Background())
	if err != nil {
		t.Fatalf("loadSources TXT failed: %v", err)
	}

	if len(urls) != 2 {
		t.Errorf("expected 2 URLs, got %d", len(urls))
	}
}

func TestLoadSourcesDoesNotCacheFailedRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - https://example.com/art.jpg\n"), 0o600); err != nil {
		t.Fatalf("write valid sources: %v", err)
	}
	loader := &Loader{sourcesFile: path}
	if _, err := loader.loadSources(context.Background()); err != nil {
		t.Fatalf("load valid sources: %v", err)
	}

	if err := os.WriteFile(path, []byte("sources: [unterminated"), 0o600); err != nil {
		t.Fatalf("write invalid sources: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if got, err := loader.loadSources(context.Background()); err == nil {
			t.Fatalf("attempt %d returned stale sources %v instead of parse error", attempt, got)
		}
	}
}

func TestLoader_InternalMethods(t *testing.T) {
	artworkDir := t.TempDir()

	path := filepath.Join(artworkDir, "test__1234567890ab.jpg")
	_ = os.WriteFile(path, []byte("some-data"), 0o600)

	idx := NewArtworkCatalog(artworkDir, slog.Default())
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("rebuild catalog: %v", err)
	}

	if _, ok := idx.LookupPrefix("test"); !ok {
		t.Error("expected prefix entry for test identity")
	}

	idx.MarkVisited("test__1234567890ab.jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("visited file was accidentally deleted")
	}

	unvisitedPath := filepath.Join(artworkDir, "002__unvisited__hash.jpg")
	_ = os.WriteFile(unvisitedPath, []byte("x"), 0o600)
	if _, err := os.Stat(unvisitedPath); err != nil {
		t.Errorf("unvisited operator file was not preserved: %v", err)
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
	downloaded, err := l.Sync(context.Background())
	if err == nil {
		t.Fatal("Sync should report a degraded cycle on download error")
	}

	if downloaded != 0 {
		t.Errorf("expected 0 downloads, got %d", downloaded)
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

	_, err := l.Sync(context.Background())
	if err == nil {
		t.Fatal("Sync should report provider failures")
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

	_, _ = l.Sync(context.Background())
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

	imageData := testJPEGBytes(t)
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
			_, _ = w.Write(imageData)
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

	imageData := testPNGBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	defer server.Close()

	downloaded, err := l.executeDownload(context.Background(), server.URL, "000__direct__test.jpg", "000__direct__test")
	if err != nil {
		t.Fatalf("executeDownload failed: %v", err)
	}
	if !downloaded {
		t.Error("expected downloaded=true")
	}

	entries, err := l.index.SupportedFiles()
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 artwork file, got %v (%v)", entries, err)
	}
	for name := range entries {
		if !strings.HasSuffix(name, ".png") {
			t.Errorf("expected png extension, got %q", name)
		}
	}
}

func TestLoader_downloadWithIdentity_MaxReached(t *testing.T) {
	artworkDir := t.TempDir()
	l := newTestLoader(&config.Config{
		ArtworkDir:       artworkDir,
		MaxArtworkImages: 1,
	}, slog.Default())
	if err := os.WriteFile(filepath.Join(artworkDir, "operator.jpg"), []byte("operator bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.index.Rebuild(); err != nil {
		t.Fatal(err)
	}

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
	_, err := l.loadSources(context.Background())
	if err == nil {
		t.Error("expected error for invalid yaml format")
	}
}

func TestLoaderLoadYAMLSourcesRejectsOversizedManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte("sources: []"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxSourcesManifestBytes+1); err != nil {
		t.Fatal(err)
	}
	loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())
	loader.sourcesFile = path
	if _, err := loader.loadSources(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("loadYamlSources() error = %v, want bounded manifest rejection", err)
	}
}

func TestLoaderLoadTXTSourcesRejectsUnsafeManifestReads(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.txt")
	if err := os.WriteFile(valid, []byte("https://example.com/art.jpg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(directory, "oversized.txt")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, maxSourcesManifestBytes+1); err != nil {
			t.Fatal(err)
		}
		loader.sourcesFile = path
		if _, err := loader.loadSources(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("loadTxtSources() error = %v, want bounded manifest rejection", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(directory, "sources-link.txt")
		if err := os.Symlink(valid, path); err != nil {
			t.Fatal(err)
		}
		loader.sourcesFile = path
		if _, err := loader.loadSources(context.Background()); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("loadTxtSources() error = %v, want symlink rejection", err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		loader.sourcesFile = valid
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := loader.loadSources(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("loadTxtSources() error = %v, want canceled", err)
		}
	})
}
