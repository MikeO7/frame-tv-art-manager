package optimize

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
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
	if name == "gradient.jpg" || filepath.Ext(name) != extJPG {
		t.Fatalf("optimized content-addressed name = %q", name)
	}
	digest, err := fileDigest(context.Background(), filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	wantName := artwork.BuildContentName("gradient.jpg", digest, extJPG, 8)
	if name != wantName {
		t.Fatalf("optimized name = %q, want post-encode digest name %q", name, wantName)
	}
	output, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("open optimized output: %v", err)
	}
	defer func() { _ = output.Close() }()
	decoded, format, err := image.Decode(output)
	if err != nil {
		t.Fatalf("decode optimized output: %v", err)
	}
	if format != "jpeg" || decoded.Bounds().Dx() != 16 || decoded.Bounds().Dy() != 9 {
		t.Fatalf("optimized output = %s %v, want jpeg 16x9", format, decoded.Bounds())
	}
}
