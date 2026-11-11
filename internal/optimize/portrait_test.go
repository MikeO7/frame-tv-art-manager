package optimize

import (
	"context"
	"image"
	"image/color"
	"testing"
)

func TestPadOnBlurKeepsWholeImageWithinUpscaleLimit(t *testing.T) {
	t.Parallel()
	red := color.RGBA{R: 220, A: 255}
	for _, test := range []struct {
		name               string
		width, height      int
		maxUpscale         float64
		wantWidth, wantTop int
	}{
		{name: "portrait fills height", width: 100, height: 200, maxUpscale: 3, wantWidth: 108, wantTop: 0},
		{name: "wide image fills width", width: 3000, height: 700, maxUpscale: 3, wantWidth: 384, wantTop: 63},
		{name: "small image capped", width: 20, height: 40, maxUpscale: 2, wantWidth: 40, wantTop: 68},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			cfg.MaxWidth, cfg.MaxHeight, cfg.MaxUpscale, cfg.PadStyle = 384, 216, test.maxUpscale, padStyleBlur
			padded := padOnBlur(solidImage(test.width, test.height, red), cfg)
			if padded.Bounds() != image.Rect(0, 0, 384, 216) {
				t.Fatalf("bounds = %v, want 384x216", padded.Bounds())
			}
			if got := redSpan(padded, 108); got != test.wantWidth {
				t.Errorf("visible image width = %d, want %d", got, test.wantWidth)
			}
			if got := firstRedRow(padded, 192); got != test.wantTop {
				t.Errorf("image top = %d, want %d", got, test.wantTop)
			}
		})
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
	cfg.PadStyle = padStyleBlur
	return renderCollage(context.Background(), left, right, newRenderEnv(cfg, discardLogger()))
}
