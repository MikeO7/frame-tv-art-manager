package optimize

import (
	"image"
	"image/color"
	"reflect"
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

func TestProcessBMSThreshold_IgnoresExtraLuminance(t *testing.T) {
	const width, height = 4, 4
	baseMap := []uint8{0, 255, 0, 255, 255, 0, 255, 0, 0, 255, 0, 255, 255, 0, 255, 0}
	baseResult := processBMSThreshold(baseMap, 128, width, height)

	oversizedMap := make([]uint8, len(baseMap)+6)
	copy(oversizedMap, baseMap)
	for i := len(baseMap); i < len(oversizedMap); i++ {
		oversizedMap[i] = 255
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("processBMSThreshold panicked: %v", r)
		}
	}()

	gotResult := processBMSThreshold(oversizedMap, 128, width, height)
	if len(gotResult) != len(baseMap) {
		t.Fatalf("expected %d results, got %d", len(baseMap), len(gotResult))
	}
	if !reflect.DeepEqual(baseResult, gotResult) {
		t.Fatalf("expected oversized input to be ignored beyond w*h elements")
	}
}

func TestProcessBMSThresholdRemovesBorderConnectedForeground(t *testing.T) {
	t.Parallel()
	const width, height = 5, 5
	values := make([]uint8, width*height)
	values[2] = 255
	values[7] = 255 // connected to the top border
	values[3*width+3] = 255

	got := processBMSThreshold(values, 128, width, height)
	if got[2] != 0 || got[7] != 0 {
		t.Fatalf("border-connected foreground survived: top=%v inner=%v", got[2], got[7])
	}
	if got[3*width+3] != 1 {
		t.Fatalf("enclosed foreground = %v, want 1", got[3*width+3])
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
