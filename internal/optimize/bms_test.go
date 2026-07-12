package optimize

import (
	"image"
	"image/color"
	"testing"
)

func TestProcessBMSThresholdInvalidDims(t *testing.T) {
	if got := processBMSThreshold([]uint8{}, 50, 0, 10); got != nil {
		t.Fatalf("expected nil for zero width, got %v", got)
	}
	if got := processBMSThreshold([]uint8{}, 50, 10, 0); got != nil {
		t.Fatalf("expected nil for zero height, got %v", got)
	}

	got := processBMSThreshold([]uint8{0, 0, 0, 0, 255, 0, 0, 0, 0}, 128, 3, 3)
	if len(got) != 9 {
		t.Fatalf("expected 9 results, got %d", len(got))
	}
	if got[4] != 1.0 {
		t.Fatalf("expected center pixel to remain enclosed foreground, got %f", got[4])
	}
	if got := processBMSThreshold([]uint8{0}, 128, 3, 3); got != nil {
		t.Fatalf("expected nil for a short luminance map, got %v", got)
	}
}

func TestGenerateBMSMap(t *testing.T) {
	src := imageForBMSTest(4, 3)
	res := generateBMSMap(src)
	if len(res) != 12 {
		t.Fatalf("expected 12 saliency values, got %d", len(res))
	}

	for _, v := range res {
		if v < 0 || v > 1 {
			t.Fatalf("unexpected saliency value %f", v)
		}
	}
}

func imageForBMSTest(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 20), 128, 255})
		}
	}
	return img
}
