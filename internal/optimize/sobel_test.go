package optimize

import (
	"image"
	"image/color"
	"testing"
)

func TestCalculateSobelEdge_Paths(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 20), G: uint8(y * 20), B: 100, A: 255})
		}
	}

	interior := calculateSobelEdge(img, 1, 1)
	if interior <= 0 {
		t.Fatalf("expected positive interior edge response, got %f", interior)
	}

	interiorSlow := calculateSobelEdgeSlow(img, 1, 1, img.Bounds())
	if interior != interiorSlow {
		t.Fatalf("interior fast=%f slow=%f", interior, interiorSlow)
	}

	boundary := calculateSobelEdge(img, 0, 2)
	boundarySlow := calculateSobelEdgeSlow(img, 0, 2, img.Bounds())
	if boundary != boundarySlow {
		t.Fatalf("boundary fast=%f slow=%f", boundary, boundarySlow)
	}
}

func TestCalculateSkinProbability(t *testing.T) {
	if got := calculateSkinProbability(200, 120, 100); got != 1.0 {
		t.Fatalf("expected high confidence skin, got %f", got)
	}
	if got := calculateSkinProbability(0, 0, 0); got != 0.0 {
		t.Fatalf("expected non-skin, got %f", got)
	}
}
