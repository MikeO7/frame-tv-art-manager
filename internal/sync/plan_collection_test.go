package sync

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

func TestBuildReconcileInput_Slideshow(t *testing.T) {
	cfg := &config.Config{
		SlideshowOverride: true,
		SlideshowEnabled:  true,
		SlideshowInterval: 15,
		SlideshowType:     "sequential",
		MatteStyle:        "none",
		Timezone:          "UTC",
	}
	input := BuildReconcileInput(cfg, map[string]struct{}{"a.jpg": {}}, map[string]string{}, &MatteConfig{}, slog.Default())
	if input.Slideshow == nil {
		t.Fatal("expected slideshow settings")
	}
	if input.Slideshow.Type != "slideshow" {
		t.Errorf("type = %q", input.Slideshow.Type)
	}
	if input.Slideshow.Value != "15" {
		t.Errorf("interval = %q", input.Slideshow.Value)
	}
}

func TestBuildReconcileInput_InvalidSlideshowInterval(t *testing.T) {
	cfg := &config.Config{
		SlideshowOverride: true,
		SlideshowEnabled:  true,
		SlideshowInterval: 99,
		SlideshowType:     "shuffle",
		MatteStyle:        "none",
	}
	input := BuildReconcileInput(cfg, nil, nil, &MatteConfig{}, slog.Default())
	if input.Slideshow == nil {
		t.Fatal("expected defaulted slideshow settings")
	}
	if input.Slideshow.Value != "3" || input.Slideshow.Type != "shuffleslideshow" {
		t.Errorf("slideshow = %+v", input.Slideshow)
	}
}

func TestBuildReconcileInput_NoSlideshowOverride(t *testing.T) {
	cfg := &config.Config{
		SlideshowOverride: false,
		SlideshowEnabled:  true,
	}
	input := BuildReconcileInput(cfg, nil, nil, &MatteConfig{}, slog.Default())
	if input.Slideshow != nil {
		t.Errorf("expected nil slideshow, got %+v", input.Slideshow)
	}
}

func TestCollection_ScanAndOptimize(t *testing.T) {
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

	cfg := &config.Config{
		ArtworkDir:      dir,
		TVIPs:           []string{"1.2.3.4"},
		TokenDir:        t.TempDir(),
		OptimizeEnabled: false,
	}
	index := sources.NewArtworkIndex(dir, slog.Default())
	collection := NewCollection(cfg, slog.Default(), index)

	files, optimized, err := collection.ScanAndOptimize(slog.Default())
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

func TestCollection_HandleSingleOptimization_RemovesCorrupt(t *testing.T) {
	dir := t.TempDir()
	badFile := "bad.jpg"
	if err := os.WriteFile(filepath.Join(dir, badFile), []byte("not-an-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ArtworkDir:      dir,
		TVIPs:           []string{"1.2.3.4"},
		TokenDir:        t.TempDir(),
		OptimizeEnabled: false,
	}
	index := sources.NewArtworkIndex(dir, slog.Default())
	collection := NewCollection(cfg, slog.Default(), index)

	localFiles := map[string]struct{}{badFile: {}}
	var mu sync.Mutex
	modified, ok := collection.HandleSingleOptimization(badFile, localFiles, cfg.OptimizeOptions(), &mu, slog.Default())
	if modified || ok {
		t.Errorf("expected corrupt file to be skipped: modified=%v ok=%v", modified, ok)
	}
	if _, exists := localFiles[badFile]; exists {
		t.Error("corrupt file should be removed from local set")
	}
}

func TestMappingStore_GetAndRenameAll(t *testing.T) {
	tokenDir := t.TempDir()
	cfg := &config.Config{
		TokenDir: tokenDir,
		TVIPs:    []string{"1.2.3.4", "5.6.7.8"},
	}
	store := NewMappingStore(cfg, slog.Default())

	m1, err := store.Get("1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	m1.Set("old.jpg", "id-old")
	if err := m1.Save(); err != nil {
		t.Fatal(err)
	}

	store.RenameAll("old.jpg", "new.jpg")

	m1, _ = store.Get("1.2.3.4")
	if cid, ok := m1.GetContentID("new.jpg"); !ok || cid != "id-old" {
		t.Errorf("expected renamed mapping on TV1, got %q ok=%v", cid, ok)
	}
}

func TestCycleReporter_PrintSummary(t *testing.T) {
	cfg := &config.Config{SyncIntervalMin: 5}
	reporter := NewCycleReporter(cfg, slog.Default())
	reporter.SetCycleNum(2)

	reporter.PrintSummary(
		time.Now().Add(-time.Second),
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

func TestReconciler_UploadWithRetry(t *testing.T) {
	retryFake := &retryUploadTransport{
		failuresBeforeSuccess: 1,
		contentID:             "uploaded-id",
	}

	r := NewReconciler(slog.Default())
	policy := config.SyncPolicy{
		ArtworkDir:     t.TempDir(),
		UploadAttempts: 3,
		UploadDelay:    time.Millisecond,
		MatteStyle:     "none",
		DryRun:         false,
	}

	req := ReconcileInput{
		LocalFiles:     map[string]struct{}{"photo.jpg": {}},
		Mapping:        map[string]string{},
		MatteOverrides: map[string]string{"photo.jpg": "none"},
	}
	result, err := r.Run(context.Background(), retryFake, "1.2.3.4", req, policy)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
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
