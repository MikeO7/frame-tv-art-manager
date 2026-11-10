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
		{name: "mild upscale", amount: 0.25, sourceWidth: 100, sourceHeight: 100, targetWidth: 125, targetHeight: 125, want: 0.25 / math.Sqrt(1.25)},
		{name: "fourfold upscale is not sharpened", amount: 0.25, sourceWidth: 100, sourceHeight: 100, targetWidth: 400, targetHeight: 400, want: 0},
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

func TestSharpenAddsEdgeContrastWithoutHalos(t *testing.T) {
	t.Parallel()
	// A soft, S-shaped vertical edge from 60 to 180, as left by resampling.
	ramp := []uint8{60, 60, 62, 70, 100, 150, 172, 180, 180}
	src := image.NewRGBA(image.Rect(0, 0, len(ramp), 5))
	for y := range 5 {
		for x, value := range ramp {
			src.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	out := sharpen(src, 1, 0)
	for x := range ramp {
		got := out.RGBAAt(x, 2).R
		if got < 60 || got > 180 {
			t.Fatalf("pixel %d = %d overshoots the source range 60..180 (halo)", x, got)
		}
	}
	if before, after := int(ramp[5])-int(ramp[4]), int(out.RGBAAt(5, 2).R)-int(out.RGBAAt(4, 2).R); after <= before {
		t.Fatalf("edge contrast %d not increased from %d", after, before)
	}
}

func TestSharpenLeavesFlatAndNoiseFloorUntouched(t *testing.T) {
	t.Parallel()
	src := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for y := range 6 {
		for x := range 6 {
			value := uint8(100 + (x+y)%2*2) // a 2-level ripple, below threshold 4
			src.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	out := sharpen(src, 1, 4)
	for y := range 6 {
		for x := range 6 {
			if out.RGBAAt(x, y) != src.RGBAAt(x, y) {
				t.Fatalf("pixel %d,%d changed below the noise threshold", x, y)
			}
		}
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

// IMAGE_SHARPEN_AMOUNT is a dial: the edge contrast it adds grows with the
// amount, and a small amount adds a small fraction of what full strength
// does. It used to add most of full strength at any non-zero setting.
func TestSharpenStrengthFollowsAmount(t *testing.T) {
	t.Parallel()
	ramp := []uint8{60, 60, 62, 70, 100, 150, 172, 180, 180}
	src := image.NewRGBA(image.Rect(0, 0, len(ramp), 5))
	for y := range 5 {
		for x, value := range ramp {
			src.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	added := func(amount float64) int {
		out := sharpen(src, amount, 0)
		total := 0
		for x := range ramp {
			total += max(int(out.RGBAAt(x, 2).R)-int(ramp[x]), int(ramp[x])-int(out.RGBAAt(x, 2).R))
		}
		return total
	}
	tenth, quarter, half, full, double := added(0.1), added(0.25), added(0.5), added(1), added(2)
	if 0 >= tenth || tenth >= quarter || quarter >= half || half >= full || full > double {
		t.Fatalf("change by amount 0.1, 0.25, 0.5, 1, 2 = %d, %d, %d, %d, %d; want it to grow", tenth, quarter, half, full, double)
	}
	if tenth*4 > full {
		t.Fatalf("amount 0.1 changes %d levels, more than a quarter of full strength's %d", tenth, full)
	}
}
