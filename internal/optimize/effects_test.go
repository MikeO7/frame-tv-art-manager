package optimize

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestScaleAwareSharpenAmount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		amount                    float64
		sourceWidth, sourceHeight int
		targetWidth, targetHeight int
		want                      float64
	}{
		{name: "one to one", amount: 0.25, sourceWidth: 100, sourceHeight: 100, targetWidth: 100, targetHeight: 100, want: 0.25},
		{name: "fourfold downscale", amount: 0.25, sourceWidth: 400, sourceHeight: 400, targetWidth: 100, targetHeight: 100, want: 0.375},
		{name: "fourfold upscale", amount: 0.25, sourceWidth: 100, sourceHeight: 100, targetWidth: 400, targetHeight: 400, want: 0.125},
		{name: "disabled", sourceWidth: 100, sourceHeight: 100, targetWidth: 400, targetHeight: 400, want: 0},
		{name: "invalid geometry", amount: 0.25, sourceHeight: 100, targetWidth: 400, targetHeight: 400, want: 0},
		{name: "bounded operator maximum", amount: 2, sourceWidth: 400, sourceHeight: 400, targetWidth: 100, targetHeight: 100, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := scaleAwareSharpenAmount(
				test.amount, test.sourceWidth, test.sourceHeight, test.targetWidth, test.targetHeight,
			)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("scaleAwareSharpenAmount() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestToRGBA(t *testing.T) {
	rgba := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if got := toRGBA(rgba); got != rgba {
		t.Error("expected same pointer for RGBA input")
	}

	nrgba := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	nrgba.Set(1, 1, color.RGBA{10, 20, 30, 255})
	converted := toRGBA(nrgba)
	if converted.Bounds().Dx() != 4 {
		t.Errorf("bounds = %v", converted.Bounds())
	}
}

func TestCenterCrop(t *testing.T) {
	wide := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			wide.Set(x, y, color.RGBA{uint8(x), uint8(y), 50, 255})
		}
	}
	croppedWide := centerCrop(wide, 16, 9, false)
	if croppedWide.Bounds().Dx() != 16 || croppedWide.Bounds().Dy() != 9 {
		t.Errorf("wide crop = %dx%d", croppedWide.Bounds().Dx(), croppedWide.Bounds().Dy())
	}

	tall := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			tall.Set(x, y, color.RGBA{uint8(x), uint8(y), 50, 255})
		}
	}
	croppedTall := centerCrop(tall, 16, 9, true)
	if croppedTall.Bounds().Dx() != 16 || croppedTall.Bounds().Dy() != 9 {
		t.Errorf("tall crop = %dx%d", croppedTall.Bounds().Dx(), croppedTall.Bounds().Dy())
	}
}

func TestLinearLightResizePreservesAverageLight(t *testing.T) {
	t.Parallel()
	source := image.NewRGBA(image.Rect(0, 0, 2, 1))
	source.SetRGBA(0, 0, color.RGBA{A: 255})
	source.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	output := resizeCrop(source, source.Bounds(), 1, 1, true)
	if got := output.RGBAAt(0, 0).R; got < 175 || got > 195 {
		t.Fatalf("linear-light black/white average = %d, want approximately 188", got)
	}
}

func TestSharpen_SmallImage(t *testing.T) {
	small := image.NewRGBA(image.Rect(0, 0, 2, 2))
	small.Set(0, 0, color.RGBA{100, 100, 100, 255})
	out := sharpen(small)
	if out.Bounds().Dx() != 2 {
		t.Errorf("expected 2x2 output, got %v", out.Bounds())
	}
}
