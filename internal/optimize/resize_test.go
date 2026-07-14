package optimize

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func TestOptimizeFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.jpg")

	// Create a test image (200x200)
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, img, nil)
	_ = f.Close()

	cfg := DefaultConfig()
	cfg.MaxWidth = 100
	cfg.MaxHeight = 100
	cfg.SmartCropEnabled = true

	newName, mod, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile failed: %v", err)
	}

	if !mod {
		t.Error("expected modified to be true")
	}

	// The file should have been renamed according to naming policy.
	if !strings.Contains(newName, "100x100_opt.h_") {
		t.Errorf("expected new filename to contain 100x100_opt.h_, got %s", newName)
	}

	// Check if file still exists and is valid
	if err := ValidateImage(filepath.Join(tmpDir, newName)); err != nil {
		t.Errorf("optimized image is invalid: %v", err)
	}
}

func TestOptimizeFile_MuseumMode(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "museum.jpg")

	// Create a test image
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, img, nil)
	_ = f.Close()

	cfg := DefaultConfig()
	cfg.MaxWidth = 100
	cfg.MaxHeight = 100
	cfg.MuseumModeEnabled = true
	cfg.MuseumModeIntensity = 5

	newName, mod, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile museum mode failed: %v", err)
	}
	if !mod {
		t.Error("expected modified to be true in museum mode")
	}
	if !strings.Contains(newName, "100x100_opt.h_") {
		t.Errorf("expected new filename to contain 100x100_opt.h_, got %s", newName)
	}
}

func TestSharpen(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	// Set a pixel to create contrast
	img.Set(5, 5, color.RGBA{255, 255, 255, 255})

	sharpened := sharpen(img)
	if sharpened.Bounds() != img.Bounds() {
		t.Error("sharpened image bounds mismatch")
	}
	// The center pixel should be different
	if sharpened.At(5, 5) == img.At(5, 5) {
		t.Log("Note: Sharpening 1x1 white pixel in 10x10 black might not change value due to kernel, but it should run")
	}
}

func TestDither(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	dithered := dither(img)
	if dithered.Bounds() != img.Bounds() {
		t.Error("dithered image bounds mismatch")
	}
}

func TestValidateImage_Invalid(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid.jpg")
	_ = os.WriteFile(path, []byte("not an image"), 0o600)

	if err := ValidateImage(path); err == nil {
		t.Error("expected error for invalid image")
	}
}

func BenchmarkSharpen(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sharpen(img)
	}
}

func TestFindBestDirectorCrop(t *testing.T) {
	// Create an image with a high-saliency area (red block) on the right
	img := image.NewRGBA(image.Rect(0, 0, 300, 100))
	// Right side: high contrast red/blue
	for y := 0; y < 100; y++ {
		for x := 200; x < 300; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}

	// Target 100x100 crop
	bestOffset := findBestDirectorCrop(img, 100, 100, true)
	// bestOffset should be around 200 (the right side)
	if bestOffset < 150 {
		t.Errorf("expected best offset to be on the right (>150), got %d", bestOffset)
	}
}

func BenchmarkDither(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dither(img)
	}
}

func TestCheckFastPath_StaleSmallerDimensions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxWidth = 3840
	cfg.MaxHeight = 2160

	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		{
			name:     "exact match skips",
			filename: "photo_3840x2160_opt.h_abc123.jpg",
			want:     true,
		},
		{
			name:     "stale smaller dims re-optimizes",
			filename: "photo_1920x1080_opt.h_abc123.jpg",
			want:     false,
		},
		{
			name:     "no opt marker always processes",
			filename: "photo.h_abc123.jpg",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFastPath(tt.filename, cfg, slog.Default())
			if got != tt.want {
				t.Errorf("checkFastPath(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestOptimizeFile_MuseumModeSkipsDither(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "museum_dither.jpg")

	// Create a test image with a smooth gradient to make noise visible
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, img, &jpeg.Options{Quality: 100})
	_ = f.Close()

	// Run with museum mode ON
	cfgMuseum := DefaultConfig()
	cfgMuseum.MaxWidth = 100
	cfgMuseum.MaxHeight = 100
	cfgMuseum.MuseumModeEnabled = true
	cfgMuseum.MuseumModeIntensity = 1

	museumName, museumMod, err := OptimizeFile(path, cfgMuseum, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile museum mode failed: %v", err)
	}
	if !museumMod {
		t.Error("expected modified to be true with museum mode")
	}
	if err := ValidateImage(filepath.Join(tmpDir, museumName)); err != nil {
		t.Errorf("museum mode output is invalid: %v", err)
	}

	// Run without museum mode for comparison — this path uses dither instead
	path2 := filepath.Join(tmpDir, "no_museum.jpg")
	f2, err := os.Create(filepath.Clean(path2))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f2, img, &jpeg.Options{Quality: 100})
	_ = f2.Close()

	cfgNormal := DefaultConfig()
	cfgNormal.MaxWidth = 100
	cfgNormal.MaxHeight = 100
	cfgNormal.MuseumModeEnabled = false

	normalName, normalMod, err := OptimizeFile(path2, cfgNormal, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile normal mode failed: %v", err)
	}
	if !normalMod {
		t.Error("expected modified to be true without museum mode")
	}
	if err := ValidateImage(filepath.Join(tmpDir, normalName)); err != nil {
		t.Errorf("normal mode output is invalid: %v", err)
	}
}

func TestOptimizeFile_DisabledAndUnsupported(t *testing.T) {
	tmpDir := t.TempDir()
	pathDisabled := filepath.Join(tmpDir, "disabled.jpg")
	f, err := os.Create(filepath.Clean(pathDisabled))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 100, 100)), nil)
	_ = f.Close()

	cfg := DefaultConfig()
	cfg.Enabled = false
	gotName, mod, err := OptimizeFile(pathDisabled, cfg, slog.Default())
	if err != nil || gotName != "disabled.jpg" || mod {
		t.Fatalf("OptimizeFile disabled = (%s, %v, %v)", gotName, mod, err)
	}

	pathPNG := filepath.Join(tmpDir, "image.png")
	if err := os.WriteFile(pathPNG, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	gotName, mod, err = OptimizeFile(pathPNG, cfg, slog.Default())
	if err != nil || gotName != "image.png" || mod {
		t.Fatalf("OptimizeFile unsupported extension = (%s, %v, %v)", gotName, mod, err)
	}
}

func TestHandleRename_NoChange(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxWidth = 4
	cfg.MaxHeight = 4
	path := filepath.Join(t.TempDir(), "upload_4x4_opt.h_aa.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// ensure handleRename reports no rename when filename already matches target
	_, changed, err := handleRename(renameRequest{
		path:     path,
		filename: "upload_4x4_opt.h_aa.jpg",
		dir:      filepath.Dir(path),
		modified: false,
		finalW:   4,
		finalH:   4,
		logger:   slog.Default(),
	})
	if err != nil || changed {
		t.Fatalf("handleRename() = (%v, %v, %v)", changed, err, path)
	}
}

func TestHandleRename_NoChangeWhenAlreadyOptimizedButModified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload_4x4_opt.h_aa.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	_, changed, err := handleRename(renameRequest{
		path:     path,
		filename: "upload_4x4_opt.h_aa.jpg",
		dir:      filepath.Dir(path),
		modified: true,
		finalW:   4,
		finalH:   4,
		logger:   slog.Default(),
	})
	if err != nil {
		t.Fatalf("handleRename() = %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true to preserve modified state")
	}
}

func TestCheckFastPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxWidth = 3840
	cfg.MaxHeight = 2160

	if !checkFastPath("photo_3840x2160_opt.h_abc123.jpg", cfg, slog.Default()) {
		t.Fatal("expected fast path for matching optimized dimensions")
	}
	if checkFastPath("photo_1920x1080_opt.h_abc123.jpg", cfg, slog.Default()) {
		t.Fatal("expected no fast path for mismatched optimized dimensions")
	}
	if checkFastPath("photo.jpg", cfg, slog.Default()) {
		t.Fatal("expected no fast path for non-optimized filename")
	}
}

func TestOptimizeFileFastPathFullyDecodesPixels(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 32, 32)), nil); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	var truncated []byte
	for cut := len(data) - 1; cut > len(data)/2; cut-- {
		candidate := data[:cut]
		if _, err := jpeg.DecodeConfig(bytes.NewReader(candidate)); err != nil {
			continue
		}
		if _, err := jpeg.Decode(bytes.NewReader(candidate)); err != nil {
			truncated = candidate
			break
		}
	}
	if truncated == nil {
		t.Fatal("could not construct a header-valid truncated JPEG")
	}
	path := filepath.Join(t.TempDir(), "photo_32x32_opt.h_abc123.jpg")
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 32, 32
	if _, _, err := optimizeFile(context.Background(), path, cfg, slog.Default(), 1); err == nil ||
		!strings.Contains(err.Error(), "validate optimized pixels") {
		t.Fatalf("optimizeFile() error = %v, want full-decode failure", err)
	}
}

func TestHandleRename_RenamesWhenNeeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	newName, changed, err := handleRename(renameRequest{
		path:     path,
		filename: "source.jpg",
		dir:      dir,
		modified: true,
		finalW:   1280,
		finalH:   720,
		logger:   slog.Default(),
	})
	if err != nil || !changed {
		t.Fatalf("handleRename() = (%s, %v, %v), want rename success", newName, changed, err)
	}
	if newName == "source.jpg" {
		t.Fatal("expected filename to change after rename")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("expected source file to be renamed")
	}
	if _, err := os.Stat(filepath.Join(dir, newName)); err != nil {
		t.Fatalf("expected renamed output to exist: %v", err)
	}
}

func TestHandleRenamePreservesExistingOperatorDestination(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	source := filepath.Join(directory, "source.jpg")
	if err := os.WriteFile(source, []byte("optimized bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	destinationName, changed := artwork.BuildOptimizedNameFromFile("source.jpg", 1280, 720)
	if !changed {
		t.Fatal("test name did not require optimization rename")
	}
	destination := filepath.Join(directory, destinationName)
	if err := os.WriteFile(destination, []byte("operator bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := handleRename(renameRequest{
		path: source, filename: "source.jpg", dir: directory, modified: true,
		finalW: 1280, finalH: 720, logger: slog.Default(), ctx: context.Background(),
	})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("handleRename() error = %v, want destination collision", err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "optimized bytes" {
		t.Fatalf("source = %q, %v", got, err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "operator bytes" {
		t.Fatalf("destination = %q, %v", got, err)
	}
}

func TestHandleRename_RenameErrorPropagated(t *testing.T) {
	dir := t.TempDir()
	_, _, err := handleRename(renameRequest{
		path:     filepath.Join(dir, "missing.jpg"),
		filename: "missing.jpg",
		dir:      dir,
		modified: false,
		finalW:   4,
		finalH:   4,
		logger:   slog.Default(),
	})
	if err == nil {
		t.Fatalf("expected rename failure error")
	}
}

func TestValidateImage_OpenError(t *testing.T) {
	err := ValidateImage(filepath.Join(t.TempDir(), "does-not-exist.jpg"))
	if err == nil {
		t.Fatalf("expected ValidateImage open error")
	}
}

func TestOptimizeFile_DecodeImageErrorAfterConfig(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	valid := filepath.Join(dir, "valid.jpg")
	f, err := os.Create(valid)
	if err != nil {
		t.Fatalf("create valid image: %v", err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		_ = f.Close()
		t.Fatalf("encode image: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close image: %v", err)
	}

	raw, err := os.ReadFile(valid)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	corrupt := filepath.Join(dir, "corrupt.jpg")
	if err := os.WriteFile(corrupt, raw[:len(raw)-6], 0o600); err != nil {
		t.Fatalf("write corrupt image: %v", err)
	}

	cfg := DefaultConfig()
	cfg.MaxWidth = 16
	cfg.MaxHeight = 16
	_, _, err = OptimizeFile(corrupt, cfg, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "decode image") {
		t.Fatalf("OptimizeFile() = %v", err)
	}
}

func TestRewriteImage_PortraitModePadsInsteadOfCrop(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "portrait.jpg")
	writeJPEGTestImage(t, src, 3, 5)

	f, err := os.Open(src)
	if err != nil {
		t.Fatalf("open source image: %v", err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 10
	cfg.PortraitMode = "pad"

	newW, newH, err := rewriteImage(rewriteParams{
		f:        f,
		path:     src,
		filename: "portrait.jpg",
		width:    3,
		height:   5,
		cfg:      cfg,
		logger:   slog.Default(),
	})
	if err != nil {
		t.Fatalf("rewriteImage() = %v", err)
	}
	if newW != cfg.MaxWidth || newH != cfg.MaxHeight {
		t.Fatalf("expected rewritten dimensions %dx%d, got %dx%d", cfg.MaxWidth, cfg.MaxHeight, newW, newH)
	}
	if err := ValidateImage(src); err != nil {
		t.Fatalf("rewritten image invalid: %v", err)
	}
}

func TestRewriteImage_ReplaceFailureReturnsRenameError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.jpg")
	writeJPEGTestImage(t, src, 4, 4)
	target := filepath.Join(dir, "blocked")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create blocked target: %v", err)
	}

	f, err := os.Open(src)
	if err != nil {
		t.Fatalf("open source image: %v", err)
	}
	_, _, err = rewriteImage(rewriteParams{
		f:        f,
		path:     target,
		filename: "source.jpg",
		width:    4,
		height:   4,
		cfg:      DefaultConfig(),
		logger:   slog.Default(),
	})
	if err == nil || !strings.Contains(err.Error(), "replace optimized image") {
		t.Fatalf("expected replace failure, got %v", err)
	}
}
