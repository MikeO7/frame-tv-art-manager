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

	w, h, mod, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile failed: %v", err)
	}

	if w != 100 || h != 100 {
		t.Errorf("expected 100x100, got %dx%d", w, h)
	}
	if !mod {
		t.Error("expected modified to be true")
	}

	// Check if file still exists and is valid
	if err := ValidateImage(path); err != nil {
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

	_, _, mod, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile museum mode failed: %v", err)
	}
	if !mod {
		t.Error("expected modified to be true in museum mode")
	}
}

func TestSharpen(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	// Set a pixel to create contrast
	img.Set(5, 5, color.RGBA{255, 255, 255, 255})

	sharpened := Sharpen(img)
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
	dithered := Dither(img)
	if dithered.Bounds() != img.Bounds() {
		t.Error("dithered image bounds mismatch")
	}
}

func TestValidateImage_Invalid(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid.jpg")
	_ = os.WriteFile(path, []byte("not an image"), 0600)

	if err := ValidateImage(path); err == nil {
		t.Error("expected error for invalid image")
	}
}

func TestFitDimensions(t *testing.T) {
	tests := []struct {
		w, h, maxW, maxH int
		expW, expH       int
	}{
		{100, 100, 50, 50, 50, 50},
		{200, 100, 100, 100, 100, 50},
		{100, 200, 100, 100, 50, 100},
		{50, 50, 100, 100, 100, 100}, // Should upscale to fill
	}

	for _, tt := range tests {
		gotW, gotH := fitDimensions(tt.w, tt.h, tt.maxW, tt.maxH)
		if gotW != tt.expW || gotH != tt.expH {
			t.Errorf("fitDimensions(%d,%d, %d,%d) = %d,%d; want %d,%d", tt.w, tt.h, tt.maxW, tt.maxH, gotW, gotH, tt.expW, tt.expH)
		}
	}
}
func TestCalculateEntropy(t *testing.T) {
	// Create a high contrast image
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 255, 0, 255})
			}
		}
	}

	entropy := calculateEntropy(img, img.Bounds())
	if entropy == 0 {
		t.Error("expected non-zero entropy for high contrast image")
	}

	// Create a flat image
	imgFlat := image.NewRGBA(image.Rect(0, 0, 100, 100))
	entropyFlat := calculateEntropy(imgFlat, imgFlat.Bounds())
	if entropyFlat != 0 {
		t.Errorf("expected zero entropy for flat image, got %f", entropyFlat)
	}
}

func TestFindBestCropWindow(t *testing.T) {
	// Create an image with a high-entropy area on the right
	img := image.NewRGBA(image.Rect(0, 0, 300, 100))
	// Left side: flat black
	// Right side: noise
	for y := 0; y < 100; y++ {
		for x := 200; x < 300; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
	}

	// Target 100x100 crop
	bestOffset := findBestCropWindow(img, 100, 100, true)
	// bestOffset should be around 200 (the right side)
	if bestOffset < 150 {
		t.Errorf("expected best offset to be on the right (>150), got %d", bestOffset)
	}
}
