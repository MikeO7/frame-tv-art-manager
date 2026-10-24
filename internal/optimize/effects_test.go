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

func TestToRGBANormalizesOriginAndPremultipliesOtherModels(t *testing.T) {
	t.Parallel()
	rgba := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if got := toRGBA(rgba); got != rgba {
		t.Error("expected same pointer for RGBA input")
	}

	nrgba := image.NewNRGBA(image.Rect(2, 3, 6, 7))
	nrgba.SetNRGBA(3, 4, color.NRGBA{R: 200, G: 100, B: 50, A: 128})
	converted := toRGBA(nrgba)
	if converted.Bounds() != image.Rect(0, 0, 4, 4) {
		t.Fatalf("bounds = %v, want zero-origin 4x4", converted.Bounds())
	}
	if got := converted.RGBAAt(1, 1); got != (color.RGBA{R: 100, G: 50, B: 25, A: 128}) {
		t.Fatalf("converted pixel = %+v, want premultiplied {100 50 25 128}", got)
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
