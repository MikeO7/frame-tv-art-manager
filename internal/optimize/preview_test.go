package optimize

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestDescribeFitMirrorsTheFitPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		configure     func(*Config)
		wantMode      FitMode
		wantScale     float64
		wantCrop      float64
	}{
		{name: "exact frame", width: 3840, height: 2160, wantMode: FitExact, wantScale: 1},
		{name: "half-size 16:9 crops with 2x upscale", width: 1920, height: 1080, wantMode: FitCrop, wantScale: 2},
		{name: "3:2 photo crops", width: 6000, height: 4000, wantMode: FitCrop, wantScale: 0.64, wantCrop: 15.625},
		{name: "square art is matted inside the mat border", width: 2000, height: 2000, wantMode: FitMat, wantScale: 0.929},
		{name: "tiny art is matted at the upscale cap", width: 400, height: 225, wantMode: FitMat, wantScale: 3},
		{
			name: "blur pad style", width: 2000, height: 2000, wantMode: FitBlur, wantScale: 1.08,
			configure: func(cfg *Config) { cfg.PadStyle = padStyleBlur },
		},
		{
			name: "collage portrait", width: 1000, height: 2000, wantMode: FitCollage, wantScale: 0.929,
			configure: func(cfg *Config) { cfg.PortraitMode = portraitModeCollage },
		},
		{
			name: "optimization off", width: 1000, height: 2000, wantMode: FitUnchanged, wantScale: 1,
			configure: func(cfg *Config) { cfg.Enabled = false },
		},
		{name: "unknown size", width: 0, height: 0, wantMode: FitUnchanged, wantScale: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			if test.configure != nil {
				test.configure(&cfg)
			}
			got := DescribeFit(test.width, test.height, cfg)
			if got.Mode != test.wantMode || math.Abs(got.Scale-test.wantScale) > 0.001 ||
				math.Abs(got.CropPercent-test.wantCrop) > 0.001 {
				t.Fatalf("DescribeFit(%dx%d) = %+v, want %s scale %.3f crop %.3f",
					test.width, test.height, got, test.wantMode, test.wantScale, test.wantCrop)
			}
		})
	}
}

func writePreviewJPEG(t *testing.T, width, height int) string {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "art.jpg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(file, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRenderPreviewRendersTheFrameWithoutWriting(t *testing.T) {
	t.Parallel()
	path := writePreviewJPEG(t, 640, 640)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderPreview(context.Background(), PreviewInput{Path: path}, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("RenderPreview() error = %v", err)
	}
	if bounds := rendered.Bounds(); bounds.Dx() != 3840 || bounds.Dy() != 2160 {
		t.Fatalf("RenderPreview() bounds = %v, want the 3840x2160 frame", bounds)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("RenderPreview() changed the source file: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("RenderPreview() left files behind: %v, %v", entries, err)
	}
}

func TestRenderPreviewPassesThroughOptimizedOrUnoptimizedArtwork(t *testing.T) {
	t.Parallel()
	path := writePreviewJPEG(t, 320, 180)
	disabled := DefaultConfig()
	disabled.Enabled = false
	for name, test := range map[string]struct {
		input PreviewInput
		cfg   Config
	}{
		"current derivative": {input: PreviewInput{Path: path, Optimized: true}, cfg: DefaultConfig()},
		"optimization off":   {input: PreviewInput{Path: path}, cfg: disabled},
	} {
		rendered, err := RenderPreview(context.Background(), test.input, test.cfg, nil)
		if err != nil || rendered.Bounds().Dx() != 320 || rendered.Bounds().Dy() != 180 {
			t.Fatalf("%s: RenderPreview() = %v, %v, want the stored pixels unchanged", name, rendered.Bounds(), err)
		}
	}
}

func TestRenderPreviewEnforcesLimitsAndCancellation(t *testing.T) {
	t.Parallel()
	path := writePreviewJPEG(t, 640, 360)
	tight := DefaultConfig()
	tight.MaxWorkingBytes = 1 << 20
	if _, err := RenderPreview(context.Background(), PreviewInput{Path: path}, tight, nil); err == nil {
		t.Fatal("RenderPreview() ignored the working-memory limit")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderPreview(canceled, PreviewInput{Path: path}, DefaultConfig(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderPreview(canceled) error = %v, want cancellation", err)
	}
	if _, err := RenderPreview(context.Background(), PreviewInput{Path: filepath.Join(t.TempDir(), "missing.jpg")},
		DefaultConfig(), nil); err == nil {
		t.Fatal("RenderPreview() rendered a missing file")
	}
}
