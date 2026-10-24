package optimize

import (
	"context"
	"image"
	"image/color"
	"testing"
)

func TestPadPortrait(t *testing.T) {
	// Create a vertical portrait image 100x200
	src := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			src.Set(x, y, color.RGBA{uint8(x * 2), uint8(y), 100, 255})
		}
	}

	padded := padPortraitWithOptions(src, 384, 216, true)
	if padded.Bounds().Dx() != 384 || padded.Bounds().Dy() != 216 {
		t.Errorf("expected padded dimensions 384x216, got %dx%d", padded.Bounds().Dx(), padded.Bounds().Dy())
	}
}

func TestPadPortrait_WidthClamp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3000, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 3000; x++ {
			src.Set(x, y, color.RGBA{uint8(x / 8), uint8(y / 4), 100, 255})
		}
	}

	padded := padPortraitWithOptions(src, 1280, 720, true)
	if padded.Bounds().Dx() != 1280 || padded.Bounds().Dy() != 720 {
		t.Fatalf("expected padded dimensions 1280x720, got %dx%d", padded.Bounds().Dx(), padded.Bounds().Dy())
	}
}

func TestCreateCollageForTargetHonorsConfiguredPixelContract(t *testing.T) {
	t.Parallel()
	left := image.NewRGBA(image.Rect(0, 0, 8, 12))
	right := image.NewRGBA(image.Rect(0, 0, 8, 12))
	got := renderTestCollage(left, right, 16, 9)
	if got.Bounds() != image.Rect(0, 0, 16, 9) {
		t.Fatalf("collage bounds = %v, want 16x9", got.Bounds())
	}
}

func TestRenderCollageSplitsPanelsAroundDivider(t *testing.T) {
	t.Parallel()
	left := image.NewRGBA(image.Rect(0, 0, 100, 200))
	right := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			left.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			right.SetRGBA(x, y, color.RGBA{G: 255, A: 255})
		}
	}

	collage := renderTestCollage(left, right, 384, 216)
	if collage.Bounds() != image.Rect(0, 0, 384, 216) {
		t.Fatalf("collage bounds = %v, want 384x216", collage.Bounds())
	}
	divider := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	for _, check := range []struct {
		x    int
		want color.RGBA
	}{
		{x: 10, want: color.RGBA{R: 255, A: 255}},
		{x: 191, want: divider},
		{x: 192, want: divider},
		{x: 300, want: color.RGBA{G: 255, A: 255}},
	} {
		if got := collage.RGBAAt(check.x, 100); got != check.want {
			t.Errorf("pixel at x=%d = %+v, want %+v", check.x, got, check.want)
		}
	}
}

func renderTestCollage(left, right *image.RGBA, width, height int) *image.RGBA {
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled = width, height, false
	return renderCollage(left, right, collageRenderRequest{
		ctx: context.Background(), cfg: cfg, logger: discardLogger(),
	})
}
