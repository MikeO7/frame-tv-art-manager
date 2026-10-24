package optimize

import (
	"image"
	"image/color"
	"testing"
)

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

func TestBMSMorphologyRemovesSpeckleAndDilatesStableObjects(t *testing.T) {
	t.Parallel()
	const width, height = 9, 9
	values := make([]uint8, width*height)
	values[1*width+1] = 255 // isolated threshold noise
	for y := 3; y <= 5; y++ {
		for x := 3; x <= 5; x++ {
			values[y*width+x] = 255
		}
	}
	attention := processBMSAttention(values, 128, width, height, false)
	if attention[1*width+1] != 0 {
		t.Fatalf("isolated threshold speckle survived opening: %v", attention[1*width+1])
	}
	if attention[4*width+4] != 1 || attention[2*width+4] != 1 {
		t.Fatalf("stable object was not retained and dilated: center=%v margin=%v",
			attention[4*width+4], attention[2*width+4])
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
