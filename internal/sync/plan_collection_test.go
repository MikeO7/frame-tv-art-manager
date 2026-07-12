package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestPhoneUpload_CollageIntegration(t *testing.T) {
	// Create a temporary directory for artwork
	tmpDir := t.TempDir()

	// 1. Create a health.Server to handle uploads
	status := health.NewStatus()
	cfg := &config.Config{
		HealthPort:        0, // disable real listener
		UploadEnabled:     true,
		ArtworkDir:        tmpDir,
		MaxDownloadSizeMB: 20,
		OptimizeMaxWidth:  3840,
		OptimizeMaxHeight: 2160,
	}
	srv := health.NewServer(cfg, status, slog.Default())

	// Create two distinct vertical portrait JPEG files (height > width)
	jpeg1 := createPortraitJPEG(t, 10)
	jpeg2 := createPortraitJPEG(t, 20)

	// Helper to send upload request
	uploadForm := func(filename string, content []byte) string {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		_, err = part.Write(content)
		if err != nil {
			t.Fatalf("write content: %v", err)
		}
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		w := httptest.NewRecorder()
		srv.HandleUpload(w, req)
		if w.Code != 200 {
			t.Fatalf("upload failed with code %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp["filename"].(string)
	}

	fn1 := uploadForm("myphone1.jpg", jpeg1)
	fn2 := uploadForm("myphone2.jpg", jpeg2)

	if !strings.HasPrefix(fn1, "upload") || !strings.HasPrefix(fn2, "upload") {
		t.Fatalf("expected filenames to start with upload, got %q and %q", fn1, fn2)
	}

	// 2. Perform catalog optimization
	catalog := sources.NewArtworkCatalog(tmpDir, slog.Default())
	optCfg := cfg.OptimizeOptions()
	optCfg.PortraitMode = "crop" // Ensure even if global mode is crop, phone uploads pair up!

	optimizedCount, err := optimize.OptimizeCatalog(context.Background(), tmpDir, catalog, optCfg, nil, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeCatalog failed: %v", err)
	}

	if optimizedCount != 1 {
		t.Errorf("expected 1 optimized count (collaging the pair), got %d", optimizedCount)
	}

	// 3. Verify that the two original upload files are removed
	if _, err := os.Stat(filepath.Join(tmpDir, fn1)); !os.IsNotExist(err) {
		t.Errorf("expected original file %s to be deleted", fn1)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, fn2)); !os.IsNotExist(err) {
		t.Errorf("expected original file %s to be deleted", fn2)
	}

	// 4. Verify that a collage file is created
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var collageFile string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "collage_") {
			collageFile = f.Name()
			break
		}
	}

	if collageFile == "" {
		t.Fatalf("expected a collage file to be created, files in dir: %v", files)
	}

	// Verify collage file contains dimensions 3840x2160 in its name
	if !strings.Contains(collageFile, "3840x2160") {
		t.Errorf("expected collage filename to contain 3840x2160, got %q", collageFile)
	}
}

// TestPhoneUpload_OddPortraitCount verifies the documented behavior that an odd
// number of portrait uploads pairs as many as possible and leaves the final
// unpaired portrait alone (it waits for a future partner on a later cycle).
func TestPhoneUpload_OddPortraitCount(t *testing.T) {
	tmpDir := t.TempDir()

	status := health.NewStatus()
	cfg := &config.Config{
		HealthPort:        0,
		UploadEnabled:     true,
		ArtworkDir:        tmpDir,
		MaxDownloadSizeMB: 20,
		OptimizeMaxWidth:  3840,
		OptimizeMaxHeight: 2160,
	}
	srv := health.NewServer(cfg, status, slog.Default())

	uploadForm := func(filename string, content []byte) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err = part.Write(content); err != nil {
			t.Fatalf("write content: %v", err)
		}
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		w := httptest.NewRecorder()
		srv.HandleUpload(w, req)
		if w.Code != 200 {
			t.Fatalf("upload failed with code %d: %s", w.Code, w.Body.String())
		}
	}

	// Three distinct portrait uploads.
	uploadForm("p1.jpg", createPortraitJPEG(t, 10))
	uploadForm("p2.jpg", createPortraitJPEG(t, 20))
	uploadForm("p3.jpg", createPortraitJPEG(t, 30))

	catalog := sources.NewArtworkCatalog(tmpDir, slog.Default())
	optCfg := cfg.OptimizeOptions()
	optCfg.PortraitMode = "crop"

	if _, err := optimize.OptimizeCatalog(context.Background(), tmpDir, catalog, optCfg, nil, slog.Default()); err != nil {
		t.Fatalf("OptimizeCatalog failed: %v", err)
	}

	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var collageCount, imageCount int
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".jpg") && !strings.HasSuffix(name, ".png") {
			continue
		}
		imageCount++
		if strings.HasPrefix(name, "collage_") {
			collageCount++
		}
	}

	if collageCount != 1 {
		t.Errorf("expected exactly 1 collage from 3 portraits, got %d (files: %v)", collageCount, files)
	}
	// One collage (from 2 portraits) + one leftover portrait = 2 image files.
	if imageCount != 2 {
		t.Errorf("expected 2 image files (1 collage + 1 unpaired portrait), got %d (files: %v)", imageCount, files)
	}
}

// recordingTVTransport captures the file paths passed to Upload so end-to-end
// tests can assert which artwork was pushed to the TV.
type recordingTVTransport struct {
	mu       sync.Mutex
	uploaded []string
}

func (r *recordingTVTransport) ShouldSkip() bool                   { return false }
func (r *recordingTVTransport) Connect(context.Context) error      { return nil }
func (r *recordingTVTransport) Close() error                       { return nil }
func (r *recordingTVTransport) Model() string                      { return "Recording TV" }
func (r *recordingTVTransport) IsInArtMode(context.Context) bool   { return true }
func (r *recordingTVTransport) SaveMetadata(context.Context) error { return nil }

func (r *recordingTVTransport) ListUploaded(context.Context) ([]samsung.ArtContent, error) {
	return nil, nil
}

func (r *recordingTVTransport) Upload(_ context.Context, filePath, _, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uploaded = append(r.uploaded, filepath.Base(filePath))
	return "content-" + filepath.Base(filePath), nil
}

func (r *recordingTVTransport) DeleteImages(context.Context, []string) error { return nil }
func (r *recordingTVTransport) SelectImage(context.Context, string) error    { return nil }

func (r *recordingTVTransport) SlideshowStatus(context.Context) (*samsung.SlideshowStatus, error) {
	return &samsung.SlideshowStatus{}, nil
}

func (r *recordingTVTransport) SetSlideshow(context.Context, samsung.SlideshowStatus) error {
	return nil
}
func (r *recordingTVTransport) SetBrightness(context.Context, int) error { return nil }
func (r *recordingTVTransport) TurnOff(context.Context) error            { return nil }
func (r *recordingTVTransport) RecordFailure(_ time.Duration)            {}
func (r *recordingTVTransport) RecordSuccess()                           {}

func (r *recordingTVTransport) uploads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.uploaded))
	copy(out, r.uploaded)
	return out
}

// TestPhoneUpload_EndToEnd exercises the full iOS path: a photo POSTed to the
// /upload endpoint lands in the artwork dir and is pushed to the TV on the next
// sync cycle.
func TestPhoneUpload_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	artworkDir := filepath.Join(tmpDir, "artwork")
	tokenDir := filepath.Join(tmpDir, "tokens")
	if err := os.MkdirAll(artworkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		TVIPs:             []string{"192.168.1.10"},
		ArtworkDir:        artworkDir,
		TokenDir:          tokenDir,
		SyncIntervalMin:   1,
		UploadEnabled:     true,
		MaxDownloadSizeMB: 20,
		OptimizeEnabled:   false,
		DryRun:            false,
	}

	// Simulate the iOS Shortcut POSTing a landscape JPEG to /upload.
	srv := health.NewServer(cfg, health.NewStatus(), slog.Default())
	landscape := createLandscapeJPEG(t, 42)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "vacation.jpg")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err = part.Write(landscape); err != nil {
		t.Fatalf("write content: %v", err)
	}
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)
	if w.Code != 200 {
		t.Fatalf("upload failed with code %d: %s", w.Code, w.Body.String())
	}
	var uploadResp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&uploadResp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	savedName, _ := uploadResp["filename"].(string)
	if !strings.HasPrefix(savedName, "upload") {
		t.Fatalf("expected saved filename to start with upload, got %q", savedName)
	}

	// Run a sync cycle and assert the uploaded photo is pushed to the TV.
	transport := &recordingTVTransport{}
	engine := NewEngine(cfg, slog.Default(), nil)
	engine.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return transport
	}

	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	uploads := transport.uploads()
	found := false
	for _, name := range uploads {
		if name == savedName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected uploaded photo %q to be pushed to TV, got uploads: %v", savedName, uploads)
	}
}

func createLandscapeJPEG(t *testing.T, val uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := range img.Pix {
		img.Pix[i] = val
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// TestPhoneUpload_LeftoverPairsOnLaterCycle is an adversarial regression test
// for the documented "waits for a future partner" behavior of unpaired
// portraits. A lone portrait uploaded on cycle 1 must remain raw so that a
// portrait uploaded on cycle 2 can pair with it into a collage.
//
// Before the fix the lone portrait was optimized in cycle 1, gaining the
// "_opt.h_" marker that permanently excludes it from the raw-portrait scan, so
// no collage could ever form for one-at-a-time uploads.
func TestPhoneUpload_LeftoverPairsOnLaterCycle(t *testing.T) {
	tmpDir := t.TempDir()

	status := health.NewStatus()
	cfg := &config.Config{
		HealthPort:          0,
		UploadEnabled:       true,
		ArtworkDir:          tmpDir,
		MaxDownloadSizeMB:   20,
		OptimizeEnabled:     true,
		OptimizeMaxWidth:    3840,
		OptimizeMaxHeight:   2160,
		OptimizeJPEGQuality: 90,
	}
	srv := health.NewServer(cfg, status, slog.Default())

	uploadForm := func(filename string, content []byte) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err = part.Write(content); err != nil {
			t.Fatalf("write content: %v", err)
		}
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		w := httptest.NewRecorder()
		srv.HandleUpload(w, req)
		if w.Code != 200 {
			t.Fatalf("upload failed with code %d: %s", w.Code, w.Body.String())
		}
	}

	runOptimize := func() {
		catalog := sources.NewArtworkCatalog(tmpDir, slog.Default())
		optCfg := cfg.OptimizeOptions()
		optCfg.PortraitMode = "crop"
		if _, err := optimize.OptimizeCatalog(context.Background(), tmpDir, catalog, optCfg, nil, slog.Default()); err != nil {
			t.Fatalf("OptimizeCatalog failed: %v", err)
		}
	}

	countCollages := func() int {
		files, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "collage_") {
				n++
			}
		}
		return n
	}

	// Cycle 1: a single portrait upload. No partner yet -> no collage, and the
	// lone portrait must stay raw (not optimized) so it can pair later.
	uploadForm("first.jpg", createPortraitJPEG(t, 10))
	runOptimize()
	if got := countCollages(); got != 0 {
		t.Fatalf("cycle 1: expected 0 collages, got %d", got)
	}

	// Cycle 2: a second portrait arrives. It must pair with the cycle-1 leftover.
	uploadForm("second.jpg", createPortraitJPEG(t, 20))
	runOptimize()
	if got := countCollages(); got != 1 {
		t.Fatalf("cycle 2: expected leftover to pair into 1 collage, got %d", got)
	}
}

// createPortraitJPEG builds a distinct vertical (portrait) JPEG. val seeds the
// pixel data so each call produces content with a unique hash.
func createPortraitJPEG(t *testing.T, val uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for i := range img.Pix {
		switch i % 4 {
		case 0:
			img.Pix[i] = val
		case 3:
			img.Pix[i] = 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLogCycleSummary(t *testing.T) {
	LogCycleSummary(slog.Default(), CycleSummary{
		CycleNum:        2,
		StartTime:       time.Now().Add(-time.Second),
		SyncIntervalMin: 5,
		TotalLocal:      3,
		FromSources:     1,
		Optimized:       2,
		TVs: []TVSyncResult{
			{
				IP: "1.2.3.4", Model: "Frame", Status: "ok",
				Uploaded: 1, Deleted: 0, TotalImages: 5,
				Brightness: "7", Slideshow: "15m shuffle",
			},
			{IP: "5.6.7.8", Status: statusBackoff},
			{IP: "9.9.9.9", Status: statusFailed, ErrorMessage: "connection refused"},
			{IP: "10.0.0.1", Status: "skipped (not art mode)"},
		},
		Warnings: []string{"Source download issue: timeout"},
	})
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
