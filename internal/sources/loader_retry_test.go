package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestOnlyMarkedConfigurationErrorsSkipTheBackoff(t *testing.T) {
	t.Parallel()
	failing := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/objects/") {
			_, _ = w.Write([]byte(`{"objectID":7,"isPublicDomain":false}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	met := newTestMetProvider(failing.URL)
	pexels := newPexelsProvider("key", discardLogger())
	pexels.BaseURL = failing.URL
	logger := discardLogger()
	museum := newMuseumProvider(newArtSearcher(logger, met), logger)

	tests := []struct {
		name          string
		provider      sourceProvider
		line          string
		configuration bool
	}{
		{name: "missing api key", provider: newUnsplashProvider("", logger), line: "unsplash:photo:abc", configuration: true},
		{name: "malformed unsplash line", provider: newUnsplashProvider("key", logger), line: "unsplash:photo", configuration: true},
		{name: "unknown unsplash type", provider: newUnsplashProvider("key", logger), line: "unsplash:album:1", configuration: true},
		{name: "malformed pexels line", provider: pexels, line: "pexels:", configuration: true},
		{name: "pexels search without query", provider: pexels, line: "pexels:search:", configuration: true},
		{name: "unknown pixabay type", provider: newPixabayProvider("key", logger), line: "pixabay:album:1", configuration: true},
		{name: "unsupported pixabay user", provider: newPixabayProvider("key", logger), line: "pixabay:user:ann", configuration: true},
		{name: "malformed nasa line", provider: newNasaProvider("key", logger), line: "nasa", configuration: true},
		{name: "unknown met type", provider: met, line: "met:gallery:1", configuration: true},
		{name: "non-numeric met object", provider: met, line: "met:object:abc", configuration: true},
		{name: "unsupported met filter", provider: met, line: "met:search:cats?colour=red", configuration: true},
		{name: "unknown commons type", provider: newCommonsProvider(logger), line: "commons:gallery", configuration: true},
		{name: "unknown cleveland type", provider: newClevelandProvider(logger), line: "cleveland:gallery:1", configuration: true},
		{name: "unknown smk type", provider: newSMKProvider(logger), line: "smk:gallery:1", configuration: true},
		{name: "unknown artic type", provider: newArticProvider(logger), line: "art_institute_of_chicago:gallery:1", configuration: true},
		{name: "museum search without query", provider: museum, line: "museum:search:", configuration: true},
		{name: "http status", provider: pexels, line: "pexels:curated"},
		{name: "content answer", provider: met, line: "met:object:7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.provider.Resolve(context.Background(), tc.line)
			if err == nil {
				t.Fatal("expected a resolution failure to classify")
			}
			if got := isSourceConfigError(err); got != tc.configuration {
				t.Fatalf("isSourceConfigError(%v) = %t, want %t", err, got, tc.configuration)
			}
		})
	}
	if !isSourceConfigError(fmt.Errorf("met search: %w", errors.Join(configError("search requires a query")))) {
		t.Fatal("a wrapped configuration error lost its mark")
	}
}

func TestLoaderRetriesConfigurationErrorsWithoutBackoff(t *testing.T) {
	fixture := newStateFixture(t, "pexels:curated\n")
	fixture.provider.err = configError("PEXELS_API_KEY not configured")
	loader := fixture.loader(t)

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err == nil ||
			!strings.Contains(err.Error(), "not configured") {
			t.Fatalf("Sync() %d error = %v, want the configuration error every time", attempt, err)
		}
	}
	if calls := fixture.provider.calls.Load(); calls != 2 {
		t.Fatalf("provider resolutions = %d, want one per cycle", calls)
	}
	if entry, recorded := loader.state.Resolutions[sourceLineKey("pexels:curated")]; recorded {
		t.Fatalf("configuration error was recorded as a provider failure: %+v", entry)
	}

	// The operator fixes the configuration and restarts: no backoff remains.
	fixture.provider.err = nil
	if downloaded, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err != nil || downloaded != 1 {
		t.Fatalf("Sync() after the fix = (%d, %v), want an immediate download", downloaded, err)
	}
}

func TestLoaderBacksOffUnmarkedErrorsAndKeepsTheCachedResolution(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "unforeseen error", err: errors.New("unexpected end of JSON input")},
		{name: "content answer", err: errors.New("met object is not public domain or has no open-access image")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newStateFixture(t, "pexels:curated\n")
			loader := fixture.loader(t)
			first, err := loader.Sync(context.Background(), collection.Snapshot{})
			if err != nil || first != 1 {
				t.Fatalf("first Sync() = (%d, %v)", first, err)
			}
			fixture.now = fixture.now.Add(sourceResolutionTTL + time.Minute)
			failedAt := fixture.now
			fixture.provider.err = tc.err

			// The failure is absorbed: the cached resolution is used.
			if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err != nil {
				t.Fatalf("Sync() with a failing provider = %v, want the last-known-good resolution", err)
			}
			entry := readStateFile(t, fixture.artworkDir).Resolutions[sourceLineKey("pexels:curated")]
			if !entry.Resolved || entry.Failures != 1 || len(entry.Images) != 1 ||
				!entry.NextAttempt.Equal(failedAt.Add(minFailureBackoff)) {
				t.Fatalf("cached resolution after failure = %+v, want it kept with a backoff", entry)
			}
			fixture.now = failedAt.Add(minFailureBackoff / 2)
			if _, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err != nil {
				t.Fatalf("Sync() during backoff = %v", err)
			}
			if calls := fixture.provider.calls.Load(); calls != 2 {
				t.Fatalf("provider resolutions = %d, want no request during backoff", calls)
			}
		})
	}
}

func TestLoaderBacksOffFailedDownloads(t *testing.T) {
	jpegData := testJPEGBytes(t)
	var requests atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		if healthy.Load() {
			_, _ = w.Write(jpegData)
			return
		}
		_, _ = w.Write([]byte("not an image"))
	}))
	defer server.Close()
	fixture := newStateFixture(t, "pexels:curated\n")
	fixture.provider.images = []sourceImage{{URL: server.URL + "/art.jpg", Identity: "pexels--art--9"}}
	const key = "source:pexels--art--9"
	start := fixture.now

	if _, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err == nil {
		t.Fatal("Sync() accepted a download that is not an image")
	}
	failure := readStateFile(t, fixture.artworkDir).Downloads[key]
	if failure.Failures != 1 || !failure.NextAttempt.Equal(start.Add(minFailureBackoff)) {
		t.Fatalf("recorded download failure = %+v", failure)
	}

	// A restarted process does not fetch the image again until it is due.
	fixture.now = start.Add(minFailureBackoff / 2)
	if downloaded, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err != nil || downloaded != 0 {
		t.Fatalf("Sync() during backoff = (%d, %v), want the image skipped", downloaded, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("download requests = %d, want none during backoff", got)
	}

	fixture.now = start.Add(minFailureBackoff)
	if _, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err == nil {
		t.Fatal("Sync() accepted the second failed download")
	}
	failure = readStateFile(t, fixture.artworkDir).Downloads[key]
	if requests.Load() != 2 || failure.Failures != 2 || !failure.NextAttempt.Equal(fixture.now.Add(2*minFailureBackoff)) {
		t.Fatalf("after the retry: requests = %d, failure = %+v; want a doubled backoff", requests.Load(), failure)
	}

	healthy.Store(true)
	fixture.now = failure.NextAttempt
	if downloaded, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err != nil || downloaded != 1 {
		t.Fatalf("Sync() after recovery = (%d, %v)", downloaded, err)
	}
	if state := readStateFile(t, fixture.artworkDir); len(state.Downloads) != 0 {
		t.Fatalf("download failures after success = %+v, want them cleared", state.Downloads)
	}
	data, err := os.ReadFile(collection.ControlPath(fixture.artworkDir, sourceStateName))
	if err != nil || bytes.Contains(data, []byte("download_failures")) {
		t.Fatalf("state without failures still writes the field: %v", err)
	}
}

func TestSourceStateDownloadFailuresAreBackwardCompatibleAndPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), sourceStateName)
	old := `{"version":1,"resolutions":{},"attributions":{"source:a":{"provider":"pexels"}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := readSourceState(context.Background(), path)
	if err != nil || len(state.Attributions) != 1 || len(state.Downloads) != 0 {
		t.Fatalf("readSourceState(old file) = (%+v, %v)", state, err)
	}

	fixture := newStateFixture(t, "pexels:curated\n")
	fixture.provider.images = nil
	loader := fixture.loader(t)
	if err := loader.ensureState(context.Background()); err != nil {
		t.Fatal(err)
	}
	loader.state.Downloads = map[string]downloadFailure{
		"source:due-soon":     {Failures: 1, NextAttempt: fixture.now.Add(time.Minute)},
		"source:long-overdue": {Failures: 3, NextAttempt: fixture.now.Add(-sourceResolutionTTL - time.Minute)},
		"source:collected":    {Failures: 1, NextAttempt: fixture.now.Add(time.Minute)},
	}
	snapshot := collection.Snapshot{Items: []collection.Item{{
		Name: "collected.jpg", Origin: collection.Origin{Key: "source:collected", Class: collection.OriginSource},
	}}}
	if _, err := loader.Sync(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	downloads := readStateFile(t, fixture.artworkDir).Downloads
	if _, kept := downloads["source:due-soon"]; !kept || len(downloads) != 1 {
		t.Fatalf("download failures after pruning = %+v, want only the one still pending", downloads)
	}
}

// truncatedJPEG returns a JPEG whose header decodes but whose pixel data is
// cut short, so only a full decode rejects it.
func truncatedJPEG(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			picture.Set(x, y, color.RGBA{R: uint8(x * 37), G: uint8(y * 91), B: uint8(x * y), A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, picture, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	data := buffer.Bytes()[:buffer.Len()/2]
	if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("truncated fixture lost its header: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err == nil {
		t.Fatal("truncated fixture still decodes")
	}
	return data
}

func sizedJPEG(t *testing.T, width int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, width, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestLoaderKeepsValidDownloadsWhenTheCollectionRefusesABatch(t *testing.T) {
	bodies := map[string][]byte{
		"/one": sizedJPEG(t, 2), "/two": sizedJPEG(t, 3), "/three": sizedJPEG(t, 4), "/corrupt": truncatedJPEG(t),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(bodies[r.URL.Path])
	}))
	defer server.Close()

	tests := []struct {
		name       string
		maxItems   int
		paths      []string
		wantCount  int
		wantSizes  []int
		wantErr    error
		wantFailed string
	}{
		{
			name: "one invalid download", paths: []string{"/one", "/corrupt", "/two"},
			wantCount: 2, wantSizes: []int{3, 1, 1, 1}, wantErr: collection.ErrInvalidImport, wantFailed: "source:corrupt",
		},
		{
			name: "collection fills up", maxItems: 2, paths: []string{"/one", "/two", "/three"},
			wantCount: 2, wantSizes: []int{3, 1, 1, 1}, wantErr: collection.ErrCollectionFull,
		},
		{
			name: "only download is invalid", paths: []string{"/corrupt"},
			wantCount: 0, wantSizes: []int{1}, wantErr: collection.ErrInvalidImport, wantFailed: "source:corrupt",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			artworkDir := t.TempDir()
			store, err := collection.New(collection.Config{Root: artworkDir, MaxItems: tc.maxItems})
			if err != nil {
				t.Fatal(err)
			}
			importer := &countingImporter{Store: store}
			loader := NewLoader(&config.Config{ArtworkDir: artworkDir, MaxDownloadSizeMB: 20}, discardLogger(), importer)
			images := make([]sourceImage, 0, len(tc.paths))
			for _, path := range tc.paths {
				images = append(images, sourceImage{URL: server.URL + path, Identity: strings.TrimPrefix(path, "/")})
			}
			loader.providers = []sourceProvider{staticSourceProvider{images: images}}
			if err := loader.ensureState(context.Background()); err != nil {
				t.Fatal(err)
			}

			count, err := loader.syncLine(context.Background(), "static:any")
			if count != tc.wantCount || !errors.Is(err, tc.wantErr) {
				t.Fatalf("syncLine() = (%d, %v), want %d valid downloads kept and %v", count, err, tc.wantCount, tc.wantErr)
			}
			if !slices.Equal(importer.batchSizes, tc.wantSizes) {
				t.Fatalf("import batch sizes = %v, want %v", importer.batchSizes, tc.wantSizes)
			}
			snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
			if err != nil || len(snapshot.Items) != tc.wantCount {
				t.Fatalf("committed items = %d, %v; want %d", len(snapshot.Items), err, tc.wantCount)
			}
			if _, failed := loader.state.Downloads[tc.wantFailed]; failed != (tc.wantFailed != "") || len(loader.state.Downloads) > 1 {
				t.Fatalf("recorded download failures = %+v, want %q", loader.state.Downloads, tc.wantFailed)
			}
		})
	}
}

func TestMetSearchInspectsNoMoreThanItsScanBound(t *testing.T) {
	t.Parallel()
	ids := make([]string, 100)
	for index := range ids {
		ids[index] = fmt.Sprint(index + 1)
	}
	tests := []struct {
		name         string
		object       func(http.ResponseWriter, *http.Request)
		wantRequests int32
		wantErr      bool
	}{
		{
			name: "objects without open-access images", wantRequests: 2 * metScanFactor,
			object: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"objectID":1,"isPublicDomain":false}`))
			},
		},
		{
			// Every inspected object failed, although the provider listed more.
			name: "every inspected object fails", wantRequests: 2 * metScanFactor, wantErr: true,
			object: http.NotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var objectRequests atomic.Int32
			server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1.1/search" {
					// The provider ignores the requested page size.
					_, _ = w.Write([]byte(`{"total":100,"objectIDs":[` + strings.Join(ids, ",") + `]}`))
					return
				}
				objectRequests.Add(1)
				tc.object(w, r)
			}))
			candidates, err := newTestMetProvider(server.URL).search(context.Background(), "flowers", url.Values{}, 2)
			if (err != nil) != tc.wantErr || len(candidates) != 0 {
				t.Fatalf("search() = (%v, %v), want error %t", candidates, err, tc.wantErr)
			}
			if got := objectRequests.Load(); got != tc.wantRequests {
				t.Fatalf("object requests = %d, want the scan bound %d", got, tc.wantRequests)
			}
		})
	}
}
