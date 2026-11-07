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
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
)

func TestValidateImageOutputChecksFormatAndDimensions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.jpg")
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 8, 6)), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := validateImageOutput(ctx, path, formatJPEG, 8, 6); err != nil {
		t.Fatalf("validateImageOutput(valid) = %v", err)
	}
	if err := validateImageOutput(ctx, path, formatPNG, 8, 6); err == nil {
		t.Fatal("validateImageOutput(format mismatch) = nil")
	}
	if err := validateImageOutput(ctx, path, formatJPEG, 7, 6); err == nil {
		t.Fatal("validateImageOutput(dimension mismatch) = nil")
	}
}

func TestWriteValidatedOutputHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := os.Create(filepath.Join(t.TempDir(), "output.png"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 8, 6
	if err := writeValidatedOutput(ctx, out, extPNG, image.NewRGBA(image.Rect(0, 0, 8, 6)), cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeValidatedOutput() error = %v, want context.Canceled", err)
	}
}

func TestWriteValidatedOutputUsesConfiguredChromaSubsampling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		subsampling jpegenc.Subsampling
		want        image.YCbCrSubsampleRatio
	}{
		{name: "default full-resolution chroma", subsampling: jpegenc.Subsampling444, want: image.YCbCrSubsampleRatio444},
		{name: "operator-selected 4:2:0", subsampling: jpegenc.Subsampling420, want: image.YCbCrSubsampleRatio420},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "output.jpg")
			out, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig()
			cfg.MaxWidth, cfg.MaxHeight, cfg.JPEGSubsampling = 24, 16, test.subsampling
			img := image.NewRGBA(image.Rect(0, 0, 24, 16))
			for y := range 16 {
				for x := range 24 {
					img.SetRGBA(x, y, color.RGBA{R: uint8(200 * (x % 2)), G: 30, B: uint8(200 * (1 - x%2)), A: 255})
				}
			}
			if err := writeValidatedOutput(context.Background(), out, extJPG, img, cfg); err != nil {
				t.Fatalf("writeValidatedOutput() = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			ycc, ok := decoded.(*image.YCbCr)
			if !ok || ycc.SubsampleRatio != test.want {
				t.Fatalf("decoded %T with ratio %v, want *image.YCbCr with %v", decoded, ycc.SubsampleRatio, test.want)
			}
		})
	}
}

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
