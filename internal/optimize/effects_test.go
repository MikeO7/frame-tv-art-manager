package optimize

import (
	"image"
	"image/color"
	"testing"
)

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

func TestSharpen_SmallImage(t *testing.T) {
	small := image.NewRGBA(image.Rect(0, 0, 2, 2))
	small.Set(0, 0, color.RGBA{100, 100, 100, 255})
	out := sharpen(small)
	if out.Bounds().Dx() != 2 {
		t.Errorf("expected 2x2 output, got %v", out.Bounds())
	}
}
