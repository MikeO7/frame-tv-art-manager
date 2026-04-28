package optimize

import (
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func writeFakeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	f, err := os.Create(filepath.Clean(path)) //nolint:gosec // Test file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
}

func TestOptimizeFile_Disabled(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/photo.jpg"
	writeFakeJPEG(t, path, 100, 100)

	cfg := Config{Enabled: false}
	_, _, ok, err := OptimizeFile(path, cfg, testLogger())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if ok {
		t.Error("should not optimize when disabled")
	}
}

func TestOptimizeFile_AlreadySmall(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/photo.jpg"
	// Use 16:9 aspect (384x216) which is already small and correct aspect.
	writeFakeJPEG(t, path, 384, 216)

	cfg := DefaultConfig()
	_, _, ok, err := OptimizeFile(path, cfg, testLogger())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if ok {
		t.Error("image already 16:9 and smaller than max dimensions should not be modified")
	}
}

func TestOptimizeFile_LargeImageResized(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/large.jpg"
	// 8K image — should be resized to 4K.
	writeFakeJPEG(t, path, 7680, 4320)

	cfg := DefaultConfig()
	newW, newH, ok, err := OptimizeFile(path, cfg, testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected image to be resized")
	}
	if newW > cfg.MaxWidth || newH > cfg.MaxHeight {
		t.Errorf("returned dimensions %dx%d exceed max %dx%d", newW, newH, cfg.MaxWidth, cfg.MaxHeight)
	}

	// Verify output dimensions.
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // Test file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	b := img.Bounds()
	if b.Dx() > cfg.MaxWidth || b.Dy() > cfg.MaxHeight {
		t.Errorf("resized image %dx%d exceeds max %dx%d", b.Dx(), b.Dy(), cfg.MaxWidth, cfg.MaxHeight)
	}
}

func TestOptimizeFile_SkipsPNG(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/photo.png"
	// Write minimal file (content doesn't matter for skip test).
	if err := os.WriteFile(path, []byte("fake"), 0644); err != nil { //nolint:gosec // Test file
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	_, _, ok, err := OptimizeFile(path, cfg, testLogger())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if ok {
		t.Error("PNG files should be skipped")
	}
}

func TestFitDimensions(t *testing.T) {
	tests := []struct {
		origW, origH, maxW, maxH int
		wantW, wantH             int
	}{
		{7680, 4320, 3840, 2160, 3840, 2160}, // exact 2x scale
		{1920, 1080, 3840, 2160, 1920, 1080}, // already smaller — unreachable (caller checks first)
		{3840, 2160, 3840, 2160, 3840, 2160}, // exact match
		{4000, 1000, 3840, 2160, 3840, 960},  // width-constrained
		{1000, 4000, 3840, 2160, 540, 2160},  // height-constrained
	}

	for _, tc := range tests {
		w, h := fitDimensions(tc.origW, tc.origH, tc.maxW, tc.maxH)
		if w != tc.wantW || h != tc.wantH {
			t.Errorf("fitDimensions(%d,%d,%d,%d) = %dx%d, want %dx%d",
				tc.origW, tc.origH, tc.maxW, tc.maxH, w, h, tc.wantW, tc.wantH)
		}
	}
}
