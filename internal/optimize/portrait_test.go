package optimize

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

func TestRotateImage(t *testing.T) {
	// Create a simple 2x3 image (width=2, height=3)
	// Pixel values:
	// A B
	// C D
	// E F
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{1, 0, 0, 255}) // A
	img.Set(1, 0, color.RGBA{2, 0, 0, 255}) // B
	img.Set(0, 1, color.RGBA{3, 0, 0, 255}) // C
	img.Set(1, 1, color.RGBA{4, 0, 0, 255}) // D
	img.Set(0, 2, color.RGBA{5, 0, 0, 255}) // E
	img.Set(1, 2, color.RGBA{6, 0, 0, 255}) // F

	// Test 90 CW rotation (orientation 6)
	// Expected:
	// E C A
	// F D B
	rot90 := RotateImage(img, 6)
	if rot90.Bounds().Dx() != 3 || rot90.Bounds().Dy() != 2 {
		t.Errorf("expected bounds 3x2, got %dx%d", rot90.Bounds().Dx(), rot90.Bounds().Dy())
	}
	if r, _, _, _ := rot90.At(0, 0).RGBA(); uint8(r) != 5 {
		t.Errorf("expected At(0,0) to be 5, got %d", uint8(r))
	}
	if r, _, _, _ := rot90.At(2, 0).RGBA(); uint8(r) != 1 {
		t.Errorf("expected At(2,0) to be 1, got %d", uint8(r))
	}

	// Test 180 rotation (orientation 3)
	// Expected:
	// F E
	// D C
	// B A
	rot180 := RotateImage(img, 3)
	if rot180.Bounds().Dx() != 2 || rot180.Bounds().Dy() != 3 {
		t.Errorf("expected bounds 2x3, got %dx%d", rot180.Bounds().Dx(), rot180.Bounds().Dy())
	}
	if r, _, _, _ := rot180.At(0, 0).RGBA(); uint8(r) != 6 {
		t.Errorf("expected At(0,0) to be 6, got %d", uint8(r))
	}
	if r, _, _, _ := rot180.At(1, 2).RGBA(); uint8(r) != 1 {
		t.Errorf("expected At(1,2) to be 1, got %d", uint8(r))
	}

	// Test 270 CW (90 CCW) rotation (orientation 8)
	// Expected:
	// B D F
	// A C E
	rot270 := RotateImage(img, 8)
	if rot270.Bounds().Dx() != 3 || rot270.Bounds().Dy() != 2 {
		t.Errorf("expected bounds 3x2, got %dx%d", rot270.Bounds().Dx(), rot270.Bounds().Dy())
	}
	if r, _, _, _ := rot270.At(0, 0).RGBA(); uint8(r) != 2 {
		t.Errorf("expected At(0,0) to be 2, got %d", uint8(r))
	}
	if r, _, _, _ := rot270.At(2, 1).RGBA(); uint8(r) != 5 {
		t.Errorf("expected At(2,1) to be 5, got %d", uint8(r))
	}

	// Test Mirror horizontal (orientation 2)
	// Expected:
	// B A
	// D C
	// F E
	orient2 := RotateImage(img, 2)
	if orient2.Bounds().Dx() != 2 || orient2.Bounds().Dy() != 3 {
		t.Errorf("expected bounds 2x3, got %dx%d", orient2.Bounds().Dx(), orient2.Bounds().Dy())
	}
	if r, _, _, _ := orient2.At(0, 0).RGBA(); uint8(r) != 2 {
		t.Errorf("expected At(0,0) to be 2, got %d", uint8(r))
	}
	if r, _, _, _ := orient2.At(1, 0).RGBA(); uint8(r) != 1 {
		t.Errorf("expected At(1,0) to be 1, got %d", uint8(r))
	}

	// Test Mirror vertical (orientation 4)
	// Expected:
	// E F
	// C D
	// A B
	orient4 := RotateImage(img, 4)
	if orient4.Bounds().Dx() != 2 || orient4.Bounds().Dy() != 3 {
		t.Errorf("expected bounds 2x3, got %dx%d", orient4.Bounds().Dx(), orient4.Bounds().Dy())
	}
	if r, _, _, _ := orient4.At(0, 0).RGBA(); uint8(r) != 5 {
		t.Errorf("expected At(0,0) to be 5, got %d", uint8(r))
	}
	if r, _, _, _ := orient4.At(1, 2).RGBA(); uint8(r) != 2 {
		t.Errorf("expected At(1,2) to be 2, got %d", uint8(r))
	}

	// Test Mirror horizontal and rotate 270 CW (orientation 5)
	// Expected:
	// A C E
	// B D F
	orient5 := RotateImage(img, 5)
	if orient5.Bounds().Dx() != 3 || orient5.Bounds().Dy() != 2 {
		t.Errorf("expected bounds 3x2, got %dx%d", orient5.Bounds().Dx(), orient5.Bounds().Dy())
	}
	if r, _, _, _ := orient5.At(0, 0).RGBA(); uint8(r) != 1 {
		t.Errorf("expected At(0,0) to be 1, got %d", uint8(r))
	}
	if r, _, _, _ := orient5.At(2, 1).RGBA(); uint8(r) != 6 {
		t.Errorf("expected At(2,1) to be 6, got %d", uint8(r))
	}

	// Test Mirror horizontal and rotate 90 CW (orientation 7)
	// Expected:
	// F D B
	// E C A
	orient7 := RotateImage(img, 7)
	if orient7.Bounds().Dx() != 3 || orient7.Bounds().Dy() != 2 {
		t.Errorf("expected bounds 3x2, got %dx%d", orient7.Bounds().Dx(), orient7.Bounds().Dy())
	}
	if r, _, _, _ := orient7.At(0, 0).RGBA(); uint8(r) != 6 {
		t.Errorf("expected At(0,0) to be 6, got %d", uint8(r))
	}
	if r, _, _, _ := orient7.At(2, 1).RGBA(); uint8(r) != 1 {
		t.Errorf("expected At(2,1) to be 1, got %d", uint8(r))
	}
}

func TestPadPortrait(t *testing.T) {
	// Create a vertical portrait image 1000x2000
	src := image.NewRGBA(image.Rect(0, 0, 1000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 1000; x++ {
			src.Set(x, y, color.RGBA{uint8(x / 4), uint8(y / 8), 100, 255})
		}
	}

	padded := padPortrait(src, 3840, 2160)
	if padded.Bounds().Dx() != 3840 || padded.Bounds().Dy() != 2160 {
		t.Errorf("expected padded dimensions 3840x2160, got %dx%d", padded.Bounds().Dx(), padded.Bounds().Dy())
	}
}

func TestPadPortrait_WidthClamp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3000, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 3000; x++ {
			src.Set(x, y, color.RGBA{uint8(x / 8), uint8(y / 4), 100, 255})
		}
	}

	padded := padPortrait(src, 1280, 720)
	if padded.Bounds().Dx() != 1280 || padded.Bounds().Dy() != 720 {
		t.Fatalf("expected padded dimensions 1280x720, got %dx%d", padded.Bounds().Dx(), padded.Bounds().Dy())
	}
}

func TestCreateCollage(t *testing.T) {
	img1 := image.NewRGBA(image.Rect(0, 0, 1000, 2000))
	img2 := image.NewRGBA(image.Rect(0, 0, 1000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 1000; x++ {
			img1.Set(x, y, color.RGBA{255, 0, 0, 255})
			img2.Set(x, y, color.RGBA{0, 255, 0, 255})
		}
	}

	collage := CreateCollage(img1, img2, false)
	if collage.Bounds().Dx() != 3840 || collage.Bounds().Dy() != 2160 {
		t.Errorf("expected collage dimensions 3840x2160, got %dx%d", collage.Bounds().Dx(), collage.Bounds().Dy())
	}

	// Left half should be red, right half should be green, separator at 1918-1921 should be dividerColor (18, 18, 18)
	r, g, _, _ := collage.At(100, 100).RGBA()
	if uint8(r) != 255 || uint8(g) != 0 {
		t.Errorf("expected left side to be red, got R=%d G=%d", uint8(r), uint8(g))
	}

	r, g, _, _ = collage.At(3000, 100).RGBA()
	if uint8(r) != 0 || uint8(g) != 255 {
		t.Errorf("expected right side to be green, got R=%d G=%d", uint8(r), uint8(g))
	}

	r, g, b, _ := collage.At(1920, 100).RGBA()
	if uint8(r) != 18 || uint8(g) != 18 || uint8(b) != 18 {
		t.Errorf("expected divider line to be 18,18,18, got %d,%d,%d", uint8(r), uint8(g), uint8(b))
	}
}

func TestCreateCollageForTargetHonorsConfiguredPixelContract(t *testing.T) {
	t.Parallel()
	left := image.NewRGBA(image.Rect(0, 0, 8, 12))
	right := image.NewRGBA(image.Rect(0, 0, 8, 12))
	got := CreateCollageForTarget(left, right, 16, 9, false)
	if got.Bounds() != image.Rect(0, 0, 16, 9) {
		t.Fatalf("collage bounds = %v, want 16x9", got.Bounds())
	}
}

func TestReadOrientation_NoEXIF(t *testing.T) {
	// A mock JPEG with SOI but no Exif APP1 segment, ending in EOI
	noExifJpeg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	r := bytes.NewReader(noExifJpeg)
	orient, err := ReadOrientation(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if orient != 1 {
		t.Errorf("expected default orientation 1, got %d", orient)
	}
}
