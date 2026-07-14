package optimize

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestOrdinaryTransformOutputContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gradient.jpg")
	input := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := range 24 {
		for x := range 32 {
			input.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 7),
				G: uint8(y * 9),
				B: uint8((x + y) * 4),
				A: 255,
			})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create input: %v", err)
	}
	encodeErr := jpeg.Encode(file, input, &jpeg.Options{Quality: 95})
	closeErr := file.Close()
	if encodeErr != nil {
		if closeErr != nil {
			t.Fatalf("encode input: %v; close input: %v", encodeErr, closeErr)
		}
		t.Fatalf("encode input: %v", encodeErr)
	}
	if closeErr != nil {
		t.Fatalf("close input: %v", closeErr)
	}

	cfg := DefaultConfig()
	cfg.MaxWidth = 16
	cfg.MaxHeight = 9
	cfg.OptimizeJPEGQuality = 90
	cfg.SmartCropEnabled = false
	cfg.MuseumModeEnabled = false

	name, changed, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile() error = %v", err)
	}
	if !changed {
		t.Fatal("OptimizeFile() changed = false, want true")
	}
	if name != "gradient_16x9_opt.h_local.jpg" {
		t.Fatalf("optimized name = %q", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read optimized output: %v", err)
	}
	gotDigest := fmt.Sprintf("%x", sha256.Sum256(raw))
	const wantDigest = "7816464f4f0711c7b58e07811b73c9d16691cad829082f5029e4e9dc0bf0405b"
	if gotDigest != wantDigest {
		t.Fatalf("optimized SHA-256 = %s, want %s", gotDigest, wantDigest)
	}
}
