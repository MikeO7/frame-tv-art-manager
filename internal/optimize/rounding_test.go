package optimize

import (
	"image"
	"testing"
)

func TestMuseumContrastIdentityPreservesNeutralLevels(t *testing.T) {
	t.Parallel()
	src := image.NewRGBA(image.Rect(0, 0, 256, 1))
	for value := range 256 {
		copy(src.Pix[value*4:], []uint8{uint8(value), uint8(value), uint8(value), 255})
	}
	applyContrastAndGamut(src, 1)
	for value := range 256 {
		if got := src.Pix[value*4]; int(got) != value {
			t.Fatalf("neutral level %d mapped to %d, want unchanged", value, got)
		}
	}
}

func TestEffectQuantizationRoundsToNearest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  uint8
		want uint8
	}{
		{name: "box mean rounds up", got: avgChannel(17*10+9, 17), want: 11},
		{name: "box mean rounds down", got: avgChannel(17*10+8, 17), want: 10},
		{name: "box mean maximum", got: avgChannel(17*255, 17), want: 255},
		{name: "soft light rounds", got: applySoftLight(100.6/255, 0.5, 0), want: 101},
		{name: "soft light maximum", got: applySoftLight(1, 0.5, 0), want: 255},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Fatalf("%s = %d, want %d", test.name, test.got, test.want)
		}
	}
	state := uint32(1)
	red, _, _ := polishPixel(100, 100, 100, &state)
	stateCopy := uint32(1)
	stateCopy ^= stateCopy << 13
	stateCopy ^= stateCopy >> 17
	stateCopy ^= stateCopy << 5
	noise := (float32(stateCopy)/float32(0xFFFFFFFF) - 0.5) * 5.0
	if want := uint8(float32(100) + noise + 0.5); red != want {
		t.Fatalf("polishPixel red = %d, want rounded %d", red, want)
	}
}
