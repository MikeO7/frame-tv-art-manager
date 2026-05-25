package sync

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

func TestDetermineSlideshowSettings(t *testing.T) {
	cfg := &config.Config{
		SlideshowOverride: true,
		SlideshowEnabled:  true,
		SlideshowInterval: 15,
		SlideshowType:     "sequential",
	}
	s := determineSlideshowSettings(cfg, slog.Default())
	if s == nil {
		t.Fatal("expected slideshow settings")
	}
	if s.Type != "slideshow" {
		t.Errorf("type = %q", s.Type)
	}
	if s.Value != "15" {
		t.Errorf("interval = %q", s.Value)
	}
}

func TestDetermineSlideshowSettings_InvalidInterval(t *testing.T) {
	cfg := &config.Config{
		SlideshowOverride: true,
		SlideshowEnabled:  true,
		SlideshowInterval: 99,
		SlideshowType:     "shuffle",
	}
	s := determineSlideshowSettings(cfg, slog.Default())
	if s == nil {
		t.Fatal("expected defaulted slideshow settings")
	}
	if s.Value != "3" || s.Type != "shuffleslideshow" {
		t.Errorf("slideshow = %+v", s)
	}
}

func TestDetermineSlideshowSettings_NoOverride(t *testing.T) {
	cfg := &config.Config{
		SlideshowOverride: false,
		SlideshowEnabled:  true,
	}
	s := determineSlideshowSettings(cfg, slog.Default())
	if s != nil {
		t.Errorf("expected nil slideshow, got %+v", s)
	}
}

func TestCatalog_ScanAndOptimize(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	filename := "plainphoto.jpg"
	if err := os.WriteFile(filepath.Join(dir, filename), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog := sources.NewArtworkCatalog(dir, slog.Default())

	optCfg := (&config.Config{OptimizeEnabled: false}).OptimizeOptions()
	optimized, err := optimize.OptimizeCatalog(context.Background(), dir, catalog, optCfg, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if optimized > 0 {
		catalog.InvalidateCache()
	}
	files, err := catalog.SupportedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if optimized != 0 {
		t.Errorf("expected 0 optimized with optimization disabled, got %d", optimized)
	}
}

func TestCatalog_OptimizeCatalog_RemovesCorrupt(t *testing.T) {
	dir := t.TempDir()
	badFile := "bad.jpg"
	if err := os.WriteFile(filepath.Join(dir, badFile), []byte("not-an-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog := sources.NewArtworkCatalog(dir, slog.Default())

	optCfg := (&config.Config{OptimizeEnabled: false}).OptimizeOptions()
	files, err := catalog.SupportedFiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for f := range files {
		if strings.HasPrefix(f, "bad") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected bad.jpg (or its hashed rename) to be in supported files, catalog has: %v", files)
	}

	optimized, err := optimize.OptimizeCatalog(context.Background(), dir, catalog, optCfg, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if optimized != 0 {
		t.Errorf("expected 0 optimized, got %d", optimized)
	}
}

func TestLogCycleSummary(t *testing.T) {
	LogCycleSummary(
		slog.Default(),
		2,
		time.Now().Add(-time.Second),
		5,
		3, 1, 2,
		[]TVSyncResult{
			{
				IP: "1.2.3.4", Model: "Frame", Status: "ok",
				Uploaded: 1, Deleted: 0, TotalImages: 5,
				Brightness: "7", Slideshow: "15m shuffle",
			},
			{IP: "5.6.7.8", Status: statusBackoff},
			{IP: "9.9.9.9", Status: "failed", ErrorMessage: "connection refused"},
			{IP: "10.0.0.1", Status: "skipped (not art mode)"},
		},
		[]string{"Source download issue: timeout"},
	)
}

func TestTVReconciler_Reconcile_UploadWithRetry(t *testing.T) {
	retryFake := &retryUploadTransport{
		failuresBeforeSuccess: 1,
		contentID:             "uploaded-id",
	}

	tmpDir := t.TempDir()
	cfg := &config.Config{
		TokenDir:       tmpDir,
		ArtworkDir:     tmpDir,
		UploadAttempts: 3,
		UploadDelay:    time.Millisecond,
		MatteStyle:     "none",
		DryRun:         false,
	}

	m, _ := LoadMapping(tmpDir, "1.2.3.4")
	_ = m.Save()

	mc := &config.MatteConfig{Overrides: map[string]string{"photo.jpg": "none"}}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	localFiles := map[string]struct{}{"photo.jpg": {}}
	result, err := s.Reconcile(context.Background(), retryFake, localFiles)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Uploaded != 1 {
		t.Errorf("uploaded = %d", result.Uploaded)
	}
	if retryFake.attempts != 2 {
		t.Errorf("expected 2 upload attempts, got %d", retryFake.attempts)
	}
}

func TestEngine_DownloadSources_WithHealth(t *testing.T) {
	cfg := &config.Config{
		TVIPs:      []string{testIP},
		ArtworkDir: t.TempDir(),
		TokenDir:   t.TempDir(),
	}
	status := health.NewStatus()
	e := NewEngine(cfg, slog.Default(), status)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	status.SetStage("idle")
}

func TestEngine_RunLoop_Cancelled(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{testIP},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 60,
	}
	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &mockTVTransport{artMode: true}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.RunLoop(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop did not exit after cancel")
	}
}

type retryUploadTransport struct {
	failuresBeforeSuccess int
	attempts              int
	contentID             string
}

func (r *retryUploadTransport) ShouldSkip() bool {
	return false
}

func (r *retryUploadTransport) Connect(_ context.Context) error {
	return nil
}

func (r *retryUploadTransport) Close() error {
	return nil
}

func (r *retryUploadTransport) Model() string {
	return "Retry TV"
}

func (r *retryUploadTransport) IsInArtMode(_ context.Context) bool {
	return true
}

func (r *retryUploadTransport) SaveMetadata(_ context.Context) error {
	return nil
}

func (r *retryUploadTransport) ListUploaded(_ context.Context) ([]samsung.ArtContent, error) {
	return nil, nil
}

func (r *retryUploadTransport) Upload(_ context.Context, _, _, _ string) (string, error) {
	r.attempts++
	if r.attempts <= r.failuresBeforeSuccess {
		return "", context.DeadlineExceeded
	}
	return r.contentID, nil
}

func (r *retryUploadTransport) DeleteImages(_ context.Context, _ []string) error {
	return nil
}

func (r *retryUploadTransport) SelectImage(_ context.Context, _ string) error {
	return nil
}

func (r *retryUploadTransport) SlideshowStatus(_ context.Context) (*samsung.SlideshowStatus, error) {
	return nil, nil
}

func (r *retryUploadTransport) SetSlideshow(_ context.Context, _ samsung.SlideshowStatus) error {
	return nil
}

func (r *retryUploadTransport) SetBrightness(_ context.Context, _ int) error {
	return nil
}

func (r *retryUploadTransport) TurnOff(_ context.Context) error {
	return nil
}

func (r *retryUploadTransport) RecordFailure(_ time.Duration) {}

func (r *retryUploadTransport) RecordSuccess() {}
