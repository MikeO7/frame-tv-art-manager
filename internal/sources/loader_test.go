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
	"sync/atomic"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func newTestLoader(cfg *config.Config, logger *slog.Logger) *Loader {
	store, err := collection.New(collection.Config{
		Root: cfg.ArtworkDir, MaxItems: cfg.MaxArtworkImages,
		MaxImportBytes: int64(cfg.MaxDownloadSizeMB) * bytesPerMB,
	})
	if err != nil {
		panic(err)
	}
	return NewLoader(cfg, logger, store)
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
	downloaded, err := l.Sync(context.Background(), collection.Snapshot{})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if downloaded != 1 {
		t.Errorf("expected 1 download, got %d", downloaded)
	}

	// Verify file exists
	entries, readErr := os.ReadDir(artworkDir)
	var artworkFiles int
	for _, entry := range entries {
		if !entry.IsDir() {
			artworkFiles++
		}
	}
	if readErr != nil || artworkFiles != 1 {
		t.Errorf("expected 1 artwork file, got %d (%v)", artworkFiles, readErr)
	}
}

type staticSourceProvider struct {
	images []SourceImage
}

func (p staticSourceProvider) Name() string          { return "static" }
func (p staticSourceProvider) CanHandle(string) bool { return true }
func (p staticSourceProvider) Resolve(context.Context, string) ([]SourceImage, error) {
	return append([]SourceImage(nil), p.images...), nil
}

type countingImporter struct {
	collection.Store
	batches    int
	batchSizes []int
}

type scriptedImporter struct {
	snapshot collection.Snapshot
	err      error
}

func (i scriptedImporter) Import(context.Context, collection.ImportRequest) (collection.Snapshot, error) {
	return i.snapshot, i.err
}

func (i scriptedImporter) ImportBatch(context.Context, []collection.ImportRequest) (collection.Snapshot, error) {
	return i.snapshot, i.err
}

func (i *countingImporter) ImportBatch(ctx context.Context, requests []collection.ImportRequest) (collection.Snapshot, error) {
	i.batches++
	i.batchSizes = append(i.batchSizes, len(requests))
	return i.Store.ImportBatch(ctx, requests)
}

func TestLoaderSyncImportsResolvedImagesAsBatch(t *testing.T) {
	artworkDir := t.TempDir()
	first := testJPEGBytes(t)
	var secondBuffer bytes.Buffer
	if err := jpeg.Encode(&secondBuffer, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		if r.URL.Path == "/first" {
			_, _ = w.Write(first)
			return
		}
		_, _ = w.Write(secondBuffer.Bytes())
	}))
	defer server.Close()
	store, err := collection.New(collection.Config{Root: artworkDir})
	if err != nil {
		t.Fatal(err)
	}
	importer := &countingImporter{Store: store}
	loader := NewLoader(&config.Config{ArtworkDir: artworkDir}, slog.Default(), importer)
	loader.providers = []SourceProvider{staticSourceProvider{images: []SourceImage{
		{URL: server.URL + "/first", Identity: "first"},
		{URL: server.URL + "/second", Identity: "second"},
	}}}

	count, err := loader.syncLine(context.Background(), "static:any")
	if err != nil {
		t.Fatalf("syncLine() error = %v", err)
	}
	if count != 2 || importer.batches != 1 || !slices.Equal(importer.batchSizes, []int{2}) {
		t.Fatalf("count = %d, batches = %d, sizes = %v", count, importer.batches, importer.batchSizes)
	}
}

func TestLoaderSyncStopsDownloadingImmediatelyWhenCanceled(t *testing.T) {
	artworkDir := t.TempDir()
	requestStarted := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(requestStarted)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	loader := newTestLoader(&config.Config{ArtworkDir: artworkDir}, slog.Default())
	loader.providers = []SourceProvider{staticSourceProvider{images: []SourceImage{
		{URL: server.URL + "/1", Identity: "one"},
		{URL: server.URL + "/2", Identity: "two"},
		{URL: server.URL + "/3", Identity: "three"},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := loader.syncLine(ctx, "static:any")
		result <- err
	}()
	<-requestStarted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("syncLine() error = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("download requests = %d, want 1", got)
	}
}

func TestLoaderSyncFlushesAtBoundedBatchSize(t *testing.T) {
	artworkDir := t.TempDir()
	imageData := testJPEGBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imageData)
	}))
	defer server.Close()
	store, err := collection.New(collection.Config{Root: artworkDir})
	if err != nil {
		t.Fatal(err)
	}
	importer := &countingImporter{Store: store}
	loader := NewLoader(&config.Config{ArtworkDir: artworkDir}, slog.Default(), importer)
	images := make([]SourceImage, sourceImportBatchSize+1)
	for index := range images {
		images[index] = SourceImage{URL: server.URL, Identity: fmt.Sprintf("image-%02d", index)}
	}
	loader.providers = []SourceProvider{staticSourceProvider{images: images}}
	count, err := loader.syncLine(context.Background(), "static:any")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !slices.Equal(importer.batchSizes, []int{sourceImportBatchSize, 1}) {
		t.Fatalf("count = %d, batch sizes = %v", count, importer.batchSizes)
	}
}

func TestImportSourceBatchErrorContracts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.jpg")
	if err := os.WriteFile(path, testJPEGBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	pending := []pendingSourceDownload{{prepared: preparedDownload{
		path: path, filename: "download.jpg", originKey: "source:test", written: 10,
	}}}

	t.Run("missing importer", func(t *testing.T) {
		loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())
		loader.importer = nil
		if _, err := loader.importSourceBatch(context.Background(), pending); err == nil {
			t.Fatal("importSourceBatch() accepted a missing importer")
		}
	})
	t.Run("missing prepared file", func(t *testing.T) {
		loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())
		missing := []pendingSourceDownload{{prepared: preparedDownload{path: path + ".missing"}}}
		if _, err := loader.importSourceBatch(context.Background(), missing); err == nil {
			t.Fatal("importSourceBatch() accepted a missing prepared file")
		}
	})
	t.Run("import failure", func(t *testing.T) {
		loader := NewLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default(), scriptedImporter{err: errors.New("commit failed")})
		if _, err := loader.importSourceBatch(context.Background(), pending); err == nil || !strings.Contains(err.Error(), "commit failed") {
			t.Fatalf("importSourceBatch() error = %v", err)
		}
	})
	t.Run("wrong change count", func(t *testing.T) {
		loader := NewLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default(), scriptedImporter{})
		if _, err := loader.importSourceBatch(context.Background(), pending); err == nil || !strings.Contains(err.Error(), "1 downloads") {
			t.Fatalf("importSourceBatch() error = %v", err)
		}
	})
	t.Run("empty change name", func(t *testing.T) {
		loader := NewLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default(), scriptedImporter{snapshot: collection.Snapshot{
			Changes: []collection.Change{{Kind: collection.ChangeAdded}},
		}})
		if _, err := loader.importSourceBatch(context.Background(), pending); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("importSourceBatch() error = %v", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		loader := NewLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default(), scriptedImporter{snapshot: collection.Snapshot{
			Changes: []collection.Change{{Kind: collection.ChangeDuplicate, Name: "existing.jpg"}},
		}})
		added, err := loader.importSourceBatch(context.Background(), pending)
		if err != nil || added != 0 || !loader.checkExisting("test") {
			t.Fatalf("importSourceBatch() = %d, %v", added, err)
		}
	})
}

func TestLoaderSyncLineLimitAndImportErrors(t *testing.T) {
	t.Run("no provider", func(t *testing.T) {
		loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())
		loader.providers = nil
		if _, err := loader.syncLine(context.Background(), "unknown:any"); err == nil {
			t.Fatal("syncLine() accepted an unknown provider")
		}
	})
	t.Run("collection limit", func(t *testing.T) {
		loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir(), MaxArtworkImages: 1}, slog.Default())
		loader.collectionSize = 1
		loader.providers = []SourceProvider{staticSourceProvider{images: []SourceImage{{
			URL: "http://example.com/never-requested.jpg", Identity: "limited",
		}}}}
		count, err := loader.syncLine(context.Background(), "static:any")
		if err != nil || count != 0 {
			t.Fatalf("syncLine() = %d, %v", count, err)
		}
	})
	t.Run("final batch import failure", func(t *testing.T) {
		imageData := testJPEGBytes(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(imageData)
		}))
		defer server.Close()
		loader := NewLoader(
			&config.Config{ArtworkDir: t.TempDir()},
			slog.Default(),
			scriptedImporter{err: errors.New("batch commit failed")},
		)
		loader.providers = []SourceProvider{staticSourceProvider{images: []SourceImage{{URL: server.URL, Identity: "one"}}}}
		if _, err := loader.syncLine(context.Background(), "static:any"); err == nil || !strings.Contains(err.Error(), "batch commit failed") {
			t.Fatalf("syncLine() error = %v", err)
		}
	})
}

func TestLoaderSyncUsesDurableSourceOriginInsteadOfFilename(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testJPEGBytes(t))
	}))
	defer server.Close()
	if err := os.WriteFile(sourcesFile, []byte(server.URL+"\n"), 0o600); err != nil {
		t.Fatalf("write sources: %v", err)
	}

	loader := newTestLoader(&config.Config{SourcesFile: sourcesFile, ArtworkDir: artworkDir}, slog.Default())
	originKey := "source:" + sourceURLIdentity("direct", server.URL)
	snapshot := collection.Snapshot{Items: []collection.Item{{
		Name:   "arbitrary-operator-looking-name.jpg",
		Origin: collection.Origin{Key: originKey, Class: collection.OriginSource},
	}}}
	downloaded, err := loader.Sync(context.Background(), snapshot)
	if err != nil || downloaded != 0 || requests != 0 {
		t.Fatalf("Sync() = (%d, %v), requests=%d; want durable-origin skip", downloaded, err, requests)
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
	_, err := loader.Sync(context.Background(), collection.Snapshot{})
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
	downloaded, err := l.Sync(context.Background(), collection.Snapshot{})
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

	_, err := l.Sync(context.Background(), collection.Snapshot{})
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

	_, _ = l.Sync(context.Background(), collection.Snapshot{})
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

	count, err := l.syncLine(context.Background(), "artic:search:monet")
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

	entries, err := os.ReadDir(artworkDir)
	if err != nil {
		t.Fatal(err)
	}
	var foundPNG bool
	for _, entry := range entries {
		foundPNG = foundPNG || strings.HasSuffix(entry.Name(), ".png")
	}
	if !foundPNG {
		t.Fatalf("expected committed PNG artwork, got %v", entries)
	}
}

func TestLoader_downloadWithIdentity_MaxReached(t *testing.T) {
	artworkDir := t.TempDir()
	l := newTestLoader(&config.Config{
		ArtworkDir:       artworkDir,
		MaxArtworkImages: 1,
	}, slog.Default())
	l.collectionSize = 1

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
