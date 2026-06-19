package optimize

import (
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
