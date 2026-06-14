package sync

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const testIP = "192.168.1.10"

func TestEngine_RunOnce_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	artworkDir := filepath.Join(tmpDir, "artwork")
	tokenDir := filepath.Join(tmpDir, "tokens")
	_ = os.MkdirAll(artworkDir, 0o700)
	_ = os.MkdirAll(tokenDir, 0o700)

	cfg := &config.Config{
		TVIPs:           []string{testIP},
		ArtworkDir:      artworkDir,
		TokenDir:        tokenDir,
		SyncIntervalMin: 1,
		OptimizeEnabled: false,
		DryRun:          true, // Use dry run to avoid real network calls
	}

	// We need a way to mock the source loader and samsung client if we wanted a full test,
	// but for now let's just test the initialization and basic flow.

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	_ = e.RunOnce(context.Background())
	// We expect a connection failure since there's no TV at 127.0.0.1,
	// but this still covers the engine's initialization and pre-sync flow.
}

func TestParseDimensions(t *testing.T) {
	tests := []struct {
		filename   string
		expW, expH int
		expOk      bool
	}{
		{"art_3840x2160_opt.h_abc.jpg", 3840, 2160, true},
		{"simple.jpg", 0, 0, false},
		{"invalid_100x_opt.jpg", 0, 0, false},
		{"prefix_1920x1080.jpg", 1920, 1080, true},
	}

	for _, tt := range tests {
		w, h, ok := artwork.ParseDimensions(tt.filename)
		if ok != tt.expOk || w != tt.expW || h != tt.expH {
			t.Errorf("parseDimensions(%q) = %d,%d,%v; want %d,%d,%v", tt.filename, w, h, ok, tt.expW, tt.expH, tt.expOk)
		}
	}
}

type mockTVTransport struct {
	artMode     bool
	skip        bool
	failConnect error
	uploaded    []samsung.ArtContent
}

func (m *mockTVTransport) ShouldSkip() bool { return m.skip }

func (m *mockTVTransport) Connect(context.Context) error { return m.failConnect }

func (m *mockTVTransport) Close() error { return nil }

func (m *mockTVTransport) Model() string { return "Mock TV" }

func (m *mockTVTransport) IsInArtMode(context.Context) bool { return m.artMode }

func (m *mockTVTransport) SaveMetadata(context.Context) error { return nil }

func (m *mockTVTransport) ListUploaded(context.Context) ([]samsung.ArtContent, error) {
	return m.uploaded, nil
}

func (m *mockTVTransport) Upload(context.Context, string, string, string) (string, error) {
	return "new-id", nil
}

func (m *mockTVTransport) DeleteImages(context.Context, []string) error { return nil }

func (m *mockTVTransport) SelectImage(context.Context, string) error { return nil }

func (m *mockTVTransport) SlideshowStatus(context.Context) (*samsung.SlideshowStatus, error) {
	return &samsung.SlideshowStatus{}, nil
}

func (m *mockTVTransport) SetSlideshow(context.Context, samsung.SlideshowStatus) error {
	return nil
}

func (m *mockTVTransport) SetBrightness(context.Context, int) error { return nil }

func (m *mockTVTransport) TurnOff(context.Context) error { return nil }

func (m *mockTVTransport) RecordFailure(_ time.Duration) { m.skip = true }

func (m *mockTVTransport) RecordSuccess() { m.skip = false }

func createSmallJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func TestEngine_RunOnce_Full(t *testing.T) {
	tmpDir := t.TempDir()
	artworkDir := filepath.Join(tmpDir, "artwork")
	tokenDir := filepath.Join(tmpDir, "tokens")
	_ = os.MkdirAll(artworkDir, 0o700)
	_ = os.MkdirAll(tokenDir, 0o700)

	// Create a dummy image
	_ = os.WriteFile(filepath.Join(artworkDir, "test.jpg"), createSmallJPEG(), 0o600)

	cfg := &config.Config{
		TVIPs:           []string{testIP},
		ArtworkDir:      artworkDir,
		TokenDir:        tokenDir,
		SyncIntervalMin: 1,
		OptimizeEnabled: false,
		DryRun:          false,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	err := e.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
}

func TestEngine_RunOnce_DryRun(t *testing.T) {
	artworkDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(artworkDir, "test.jpg"), []byte("data"), 0o600)

	cfg := &config.Config{
		TVIPs:           []string{testIP},
		ArtworkDir:      artworkDir,
		TokenDir:        t.TempDir(),
		DryRun:          true,
		SyncIntervalMin: 1,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_RunOnce_NotArtMode(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{testIP},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 1,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: false}
	}

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_RunOnce_UnknownRemoval(t *testing.T) {
	cfg := &config.Config{
		TVIPs:               []string{testIP},
		ArtworkDir:          t.TempDir(),
		TokenDir:            t.TempDir(),
		RemoveUnknownImages: true,
		SyncIntervalMin:     1,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_RunLoop(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{testIP},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 1,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = e.RunLoop(ctx)
}

func TestEngine_DetermineBrightness(t *testing.T) {
	cfg := &config.Config{
		SolarEnabled:     false,
		ManualBrightness: func() *int { i := 5; return &i }(),
	}

	b := determineBrightness(cfg, slog.Default())
	if b == nil || *b != 5 {
		t.Errorf("expected 5, got %v", b)
	}

	cfg.SolarEnabled = true
	lat := 40.0
	lon := -105.0
	cfg.Latitude = &lat
	cfg.Longitude = &lon
	cfg.Timezone = "America/Denver"
	cfg.BrightnessMin = 0
	cfg.BrightnessMax = 10

	// This calls brightness.Calculate which might fail if no internet for suncalc,
	// but it usually works since it's local calculation.
	_ = determineBrightness(cfg, slog.Default())
}

func TestEngine_OptimizationFlow(t *testing.T) {
	tmpDir := t.TempDir()
	artworkDir := filepath.Join(tmpDir, "artwork")
	_ = os.MkdirAll(artworkDir, 0o700)

	cfg := &config.Config{
		ArtworkDir:      artworkDir,
		OptimizeEnabled: true,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	_, _ = optimize.OptimizeCatalog(context.Background(), cfg.ArtworkDir, e.catalog, cfg.OptimizeOptions(), nil, slog.Default())
}

func TestEngine_UpdateMappingsAfterRename(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		TVIPs:      []string{"1.2.3.4"},
		ArtworkDir: tmpDir,
		TokenDir:   tmpDir,
	}
	e := NewEngine(cfg, slog.Default(), nil)

	// Create mapping
	m, _ := LoadMapping(tmpDir, "1.2.3.4")
	m.Set("old.jpg", "id1")
	_ = m.Save()

	for _, ip := range e.cfg.TVIPs {
		mapping, err := LoadMapping(e.cfg.TokenDir, ip)
		if err != nil {
			continue
		}
		if mapping.Rename("old.jpg", "new.jpg") {
			_ = mapping.Save()
		}
	}

	// Load it back
	m2, _ := LoadMapping(tmpDir, "1.2.3.4")
	if cid, ok := m2.GetContentID("new.jpg"); !ok || cid != "id1" {
		t.Errorf("expected new.jpg to have id1, got %s", cid)
	}
}

func TestEngine_RunOnce_ScanError(t *testing.T) {
	// Use a path that is a file, so SupportedFiles fails (it expects a dir)
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(tmpFile, []byte("data"), 0o600)

	cfg := &config.Config{
		TVIPs:      []string{testIP},
		ArtworkDir: tmpFile,
		TokenDir:   t.TempDir(),
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}
	err := e.RunOnce(context.Background())
	if err == nil {
		t.Error("expected error when scanning a file as a directory")
	}
}

func TestEngine_DownloadSources_Error(t *testing.T) {
	artworkDir := t.TempDir()
	sourcesFile := filepath.Join(t.TempDir(), "sources.txt")
	_ = os.WriteFile(sourcesFile, []byte("invalid:prefix:123"), 0o600)

	cfg := &config.Config{
		ArtworkDir:  artworkDir,
		SourcesFile: sourcesFile,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	// Sync will fail because of invalid prefix
	_, _ = e.downloadSources(slog.Default())
}

func TestEngine_Backoff(t *testing.T) {
	cfg := &config.Config{
		TVIPs:      []string{"1.1.1.1"},
		ArtworkDir: t.TempDir(),
		TokenDir:   t.TempDir(),
	}
	e := NewEngine(cfg, slog.Default(), nil)

	// Force a failure to trigger backoff
	if c, ok := e.getClient("1.1.1.1").(*samsung.Client); ok {
		c.RecordFailure(1 * time.Minute)
	}

	err := e.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
}

func TestEngine_SyncTV_Success(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		TVIPs:      []string{"1.1.1.1"},
		ArtworkDir: tmpDir,
		TokenDir:   tmpDir,
	}
	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	// Create a dummy image
	_ = os.WriteFile(filepath.Join(tmpDir, "test.jpg"), createSmallJPEG(), 0o600)

	err := e.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
}

func TestEngine_RunOnce_SyncTVError(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		TVIPs:      []string{"1.2.3.4"},
		ArtworkDir: tmpDir,
		TokenDir:   tmpDir,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{
			artMode:     true,
			failConnect: fmt.Errorf("some connection error"),
		}
	}

	err := e.RunOnce(context.Background())
	if err == nil {
		t.Errorf("expected error from connection failure")
	}
}

func TestEngine_OptimizeCatalog_WithRename(t *testing.T) {
	tmpDir := t.TempDir()
	artworkDir := filepath.Join(tmpDir, "artwork")
	tokenDir := filepath.Join(tmpDir, "tokens")
	_ = os.MkdirAll(artworkDir, 0o700)
	_ = os.MkdirAll(tokenDir, 0o700)

	// Create mapping with the expected hashed filename that the catalog builder produces
	m, _ := LoadMapping(tokenDir, "1.2.3.4")
	m.Set("unoptimized.h_334182b100c5.jpg", "id1")
	_ = m.Save()

	// Create a small unoptimized image
	_ = os.WriteFile(filepath.Join(artworkDir, "unoptimized.jpg"), createSmallJPEG(), 0o600)

	cfg := &config.Config{
		TVIPs:           []string{"1.2.3.4"},
		ArtworkDir:      artworkDir,
		TokenDir:        tokenDir,
		OptimizeEnabled: true,
	}

	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{
			artMode: true,
			uploaded: []samsung.ArtContent{
				{ContentID: "id1"},
			},
		}
	}

	err := e.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Verify that the mapping has been updated with the renamed file
	m2, _ := LoadMapping(tokenDir, "1.2.3.4")
	hasUnoptimized := false
	hasOptimized := false
	var optimizedVal string
	for k, v := range m2.AllContentIDs() {
		if k == "unoptimized.h_334182b100c5.jpg" {
			hasUnoptimized = true
		}
		if strings.Contains(k, "unoptimized_0x0_opt.h_334182b100c5.jpg") {
			hasOptimized = true
			optimizedVal = v
		}
	}
	if hasUnoptimized {
		t.Errorf("expected unoptimized.h_334182b100c5.jpg to be removed from mapping")
	}
	if !hasOptimized {
		t.Errorf("expected optimized filename in mapping, got mapping content: %v", m2.AllContentIDs())
	}
	if optimizedVal != "id1" {
		t.Errorf("expected mapping to preserve content ID 'id1', got '%s'", optimizedVal)
	}
}
