package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// scriptedProvider counts resolutions and fails while err is set.
type scriptedProvider struct {
	name     string
	images   []sourceImage
	err      error
	calls    atomic.Int32
	tracked  []string
	trackErr error
}

func (p *scriptedProvider) Name() string          { return p.name }
func (p *scriptedProvider) CanHandle(string) bool { return true }
func (p *scriptedProvider) Resolve(context.Context, string) ([]sourceImage, error) {
	p.calls.Add(1)
	if p.err != nil {
		return nil, p.err
	}
	return append([]sourceImage(nil), p.images...), nil
}

func (p *scriptedProvider) TrackDownload(_ context.Context, trackURL string) error {
	p.tracked = append(p.tracked, trackURL)
	return p.trackErr
}

type stateFixture struct {
	artworkDir  string
	sourcesFile string
	provider    *scriptedProvider
	server      *httptest.Server
	now         time.Time
}

func newStateFixture(t *testing.T, lines string) *stateFixture {
	t.Helper()
	jpegData := testJPEGBytes(t)
	var other bytes.Buffer
	if err := jpeg.Encode(&other, image.NewRGBA(image.Rect(0, 0, 3, 3)), nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/other.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(other.Bytes())
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegData)
		}
	}))
	t.Cleanup(server.Close)
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")
	if err := os.WriteFile(sourcesFile, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &stateFixture{
		artworkDir: t.TempDir(), sourcesFile: sourcesFile, server: server, now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		provider: &scriptedProvider{name: providerPexels, images: []sourceImage{{
			URL: server.URL + "/missing", Fallbacks: []string{server.URL + "/art.jpg"}, Identity: "pexels--art--1",
			TrackURL:    "https://api.example/track/1",
			Attribution: attribution{Provider: providerPexels, Title: "Art", Artist: "Ann", License: pexelsLicense},
		}}},
	}
	return fixture
}

func (f *stateFixture) loader(t *testing.T) *loader {
	t.Helper()
	loader := newTestLoader(&config.Config{
		SourcesFile: f.sourcesFile, ArtworkDir: f.artworkDir, MaxDownloadSizeMB: 20,
	}, discardLogger())
	loader.providers = []sourceProvider{f.provider}
	loader.now = func() time.Time { return f.now }
	return loader
}

func readStateFile(t *testing.T, artworkDir string) sourceState {
	t.Helper()
	path := collection.ControlPath(artworkDir, sourceStateName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat source state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("source state mode = %04o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state sourceState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestLoaderCachesResolutionsDurablyAndRecordsAttribution(t *testing.T) {
	fixture := newStateFixture(t, "pexels:curated\n")
	loader := fixture.loader(t)

	downloaded, err := loader.Sync(context.Background(), collection.Snapshot{})
	if err != nil || downloaded != 1 {
		t.Fatalf("Sync() = (%d, %v), want the fallback rendition downloaded", downloaded, err)
	}
	if got := fixture.provider.tracked; len(got) != 1 || got[0] != "https://api.example/track/1" {
		t.Fatalf("tracked downloads = %v", got)
	}
	state := readStateFile(t, fixture.artworkDir)
	entry := state.Resolutions[sourceLineKey("pexels:curated")]
	if !entry.Resolved || !entry.ExpiresAt.Equal(fixture.now.Add(sourceResolutionTTL)) || len(entry.Images) != 1 {
		t.Fatalf("cached resolution = %+v", entry)
	}
	attributions, err := ReadAttributions(context.Background(), fixture.artworkDir)
	if err != nil || len(attributions) != 1 || attributions[0].Artist != "Ann" {
		t.Fatalf("ReadAttributions() = (%+v, %v)", attributions, err)
	}

	// A restarted process reuses the durable resolution until it expires.
	fixture.now = fixture.now.Add(sourceResolutionTTL - time.Minute)
	restarted := fixture.loader(t)
	if _, err := restarted.Sync(context.Background(), collection.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.provider.calls.Load(); calls != 1 {
		t.Fatalf("provider resolutions = %d, want 1 while cached", calls)
	}
	fixture.now = fixture.now.Add(2 * time.Minute)
	if _, err := restarted.Sync(context.Background(), collection.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.provider.calls.Load(); calls != 2 {
		t.Fatalf("provider resolutions = %d, want a refresh after expiry", calls)
	}
}

func TestLoaderKeepsLastKnownGoodResolutionAndBacksOff(t *testing.T) {
	fixture := newStateFixture(t, "pexels:curated\n")
	loader := fixture.loader(t)
	if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(sourceResolutionTTL + time.Minute)
	retryAt := fixture.now.Add(3 * time.Hour)
	fixture.provider.err = &rateLimitError{Provider: providerPexels, RetryAt: retryAt}

	downloaded, err := loader.Sync(context.Background(), collection.Snapshot{})
	if err != nil || downloaded != 0 {
		t.Fatalf("Sync() with failing provider = (%d, %v), want the last-known-good resolution", downloaded, err)
	}
	entry := readStateFile(t, fixture.artworkDir).Resolutions[sourceLineKey("pexels:curated")]
	if !entry.Resolved || entry.Failures != 1 || !entry.NextAttempt.Equal(retryAt) || len(entry.Images) != 1 {
		t.Fatalf("cached resolution after failure = %+v", entry)
	}
	fixture.now = fixture.now.Add(time.Hour)
	if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if calls := fixture.provider.calls.Load(); calls != 2 {
		t.Fatalf("provider resolutions = %d, want no request before the rate-limit reset", calls)
	}
}

func TestLoaderBacksOffUnresolvedSourcesWithoutRequests(t *testing.T) {
	fixture := newStateFixture(t, "pexels:curated\n")
	fixture.provider.err = errors.New("provider offline")
	loader := fixture.loader(t)

	if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err == nil || !strings.Contains(err.Error(), "provider offline") {
		t.Fatalf("first Sync() error = %v", err)
	}
	fixture.now = fixture.now.Add(minFailureBackoff / 2)
	if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err == nil || !strings.Contains(err.Error(), "backing off") {
		t.Fatalf("second Sync() error = %v, want a backoff error", err)
	}
	if calls := fixture.provider.calls.Load(); calls != 1 {
		t.Fatalf("provider resolutions = %d, want 1 during backoff", calls)
	}
	fixture.now = fixture.now.Add(minFailureBackoff)
	fixture.provider.err = nil
	if downloaded, err := loader.Sync(context.Background(), collection.Snapshot{}); err != nil || downloaded != 1 {
		t.Fatalf("Sync() after backoff = (%d, %v)", downloaded, err)
	}
}

func TestLoaderPrunesStateForRemovedSourcesAndArtwork(t *testing.T) {
	fixture := newStateFixture(t, "pexels:curated\n")
	loader := fixture.loader(t)
	if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.sourcesFile, []byte("pexels:search:other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.provider.images = nil
	// The collection no longer contains the downloaded artwork.
	if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	state := readStateFile(t, fixture.artworkDir)
	if _, ok := state.Resolutions[sourceLineKey("pexels:curated")]; ok || len(state.Resolutions) != 1 {
		t.Fatalf("resolutions = %v, want only the configured line", state.Resolutions)
	}
	if len(state.Attributions) != 0 {
		t.Fatalf("attributions = %v, want credits pruned with the artwork", state.Attributions)
	}
}

func TestLoaderStateFileSafety(t *testing.T) {
	t.Run("unreadable state is discarded", func(t *testing.T) {
		fixture := newStateFixture(t, "pexels:curated\n")
		path := collection.ControlPath(fixture.artworkDir, sourceStateName)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{truncated"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err != nil {
			t.Fatalf("Sync() error = %v, want the corrupt cache replaced", err)
		}
		if state := readStateFile(t, fixture.artworkDir); state.Version != sourceStateVersion {
			t.Fatalf("rewritten state = %+v", state)
		}
	})
	t.Run("unsupported version is discarded", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), sourceStateName)
		if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSourceState(context.Background(), path); !errors.Is(err, errSourceStateUnreadable) {
			t.Fatalf("readSourceState() error = %v", err)
		}
	})
	t.Run("insecure mode is rejected", func(t *testing.T) {
		fixture := newStateFixture(t, "pexels:curated\n")
		path := collection.ControlPath(fixture.artworkDir, sourceStateName)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.loader(t).Sync(context.Background(), collection.Snapshot{}); err == nil || !strings.Contains(err.Error(), "mode") {
			t.Fatalf("Sync() error = %v, want mode rejection", err)
		}
		if _, err := ReadAttributions(context.Background(), fixture.artworkDir); err == nil {
			t.Fatal("ReadAttributions() accepted an insecure state file")
		}
	})
	t.Run("symlinked control directory is rejected", func(t *testing.T) {
		fixture := newStateFixture(t, "pexels:curated\n")
		if err := os.Symlink(t.TempDir(), filepath.Dir(collection.ControlPath(fixture.artworkDir, sourceStateName))); err != nil {
			t.Fatal(err)
		}
		loader := fixture.loader(t)
		loader.importer = scriptedImporter{err: errors.New("import refused")}
		if _, err := loader.Sync(context.Background(), collection.Snapshot{}); err == nil || !strings.Contains(err.Error(), "persist source state") {
			t.Fatalf("Sync() error = %v, want the persistence failure reported", err)
		}
	})
}

func TestResolutionExpiryPolicy(t *testing.T) {
	t.Parallel()
	morning := time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		provider, line string
		now, want      time.Time
	}{
		{directProviderName, "https://example.com/a.jpg", morning, time.Time{}},
		{providerNASA, "nasa:apod", morning, time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)},
		{providerNASA, "nasa:apod", evening, time.Date(2026, 3, 2, 6, 0, 0, 0, time.UTC)},
		{providerNASA, "nasa:search:moon", morning, morning.Add(sourceResolutionTTL)},
		{providerPixabay, "pixabay:search:moon", evening, evening.Add(sourceResolutionTTL)},
		{providerCommons, "commons:potd", evening, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)},
		{providerCommons, "commons:featured", evening, evening.Add(sourceResolutionTTL)},
		{museumProviderName, "museum:search:winter", morning, morning.Add(sourceResolutionTTL)},
	}
	for _, test := range tests {
		if got := resolutionExpiry(test.provider, test.line, test.now); !got.Equal(test.want) {
			t.Errorf("resolutionExpiry(%s, %s) = %s, want %s", test.provider, test.line, got, test.want)
		}
	}
	now := morning
	if got := failureRetryAt(errors.New("x"), now, 1); !got.Equal(now.Add(minFailureBackoff)) {
		t.Errorf("first failure retry = %s", got)
	}
	if got := failureRetryAt(errors.New("x"), now, 40); !got.Equal(now.Add(sourceResolutionTTL)) {
		t.Errorf("repeated failure retry = %s, want the TTL cap", got)
	}
}

func TestLoaderRemovesOnlyStaleTemporaryDownloads(t *testing.T) {
	stale, err := os.CreateTemp("", sourceDownloadPattern)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := os.CreateTemp("", sourceDownloadPattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []*os.File{stale, fresh} {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(file.Name()) })
	}
	old := time.Now().Add(-2 * staleDownloadAge)
	if err := os.Chtimes(stale.Name(), old, old); err != nil {
		t.Fatal(err)
	}
	loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, discardLogger())
	loader.removeStaleDownloads()
	if _, err := os.Stat(stale.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale download still present: %v", err)
	}
	if _, err := os.Stat(fresh.Name()); err != nil {
		t.Fatalf("fresh download was removed: %v", err)
	}
}

func TestPrepareImageFallsBackAndReportsEveryCandidate(t *testing.T) {
	t.Parallel()
	fixture := newStateFixture(t, "")
	loader := fixture.loader(t)
	prepared, err := loader.prepareImage(context.Background(), sourceImage{
		URL: fixture.server.URL + "/missing", Fallbacks: []string{fixture.server.URL + "/other.jpg"}, Identity: "x",
	})
	if err != nil {
		t.Fatalf("prepareImage() error = %v", err)
	}
	_ = os.Remove(prepared.path)
	_, err = loader.prepareImage(context.Background(), sourceImage{
		URL: fixture.server.URL + "/missing", Fallbacks: []string{"ftp://example.com/a.jpg"}, Identity: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Fatalf("prepareImage() error = %v, want both candidate failures", err)
	}
}
