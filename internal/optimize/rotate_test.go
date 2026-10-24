package optimize

import (
	"image"
	"image/color"
	"testing"
)

// orientationReference maps destination pixel (u, v) of an oriented W x H
// source back to its source coordinate, following the EXIF definitions.
func orientationReference(orientation, u, v, width, height int) (int, int) {
	switch orientation {
	case 2:
		return width - 1 - u, v
	case 3:
		return width - 1 - u, height - 1 - v
	case 4:
		return u, height - 1 - v
	case 5:
		return v, u
	case 6:
		return v, height - 1 - u
	case 7:
		return width - 1 - v, height - 1 - u
	case 8:
		return width - 1 - v, u
	default:
		return u, v
	}
}

func orientationFixture(bounds image.Rectangle) *image.RGBA {
	img := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 11), B: uint8(x ^ y), A: uint8(200 + (x+y)%56)})
		}
	}
	return img
}

func TestRotateImageMatchesEXIFOrientations(t *testing.T) {
	t.Parallel()
	zeroOrigin := orientationFixture(image.Rect(0, 0, 131, 70))
	parent := orientationFixture(image.Rect(-9, 5, 200, 120))
	offsetOrigin, ok := parent.SubImage(image.Rect(13, 17, 144, 87)).(*image.RGBA)
	if !ok {
		t.Fatal("SubImage did not return *image.RGBA")
	}
	sources := []struct {
		name string
		img  *image.RGBA
	}{{name: "zero origin", img: zeroOrigin}, {name: "non-zero origin", img: offsetOrigin}}
	for _, source := range sources {
		for orientation := 1; orientation <= 8; orientation++ {
			t.Run(source.name+"/"+string(rune('0'+orientation)), func(t *testing.T) {
				t.Parallel()
				assertOriented(t, source.img, RotateImage(source.img, orientation), orientation)
			})
		}
	}
}

func assertOriented(t *testing.T, src *image.RGBA, got image.Image, orientation int) {
	t.Helper()
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	wantWidth, wantHeight := width, height
	if orientation >= 5 {
		wantWidth, wantHeight = height, width
	}
	bounds := got.Bounds()
	if bounds.Dx() != wantWidth || bounds.Dy() != wantHeight {
		t.Fatalf("orientation %d bounds %v, want %dx%d", orientation, bounds, wantWidth, wantHeight)
	}
	if orientation > 1 && bounds.Min != (image.Point{}) {
		t.Fatalf("orientation %d returned non-zero origin %v", orientation, bounds.Min)
	}
	for v := 0; v < wantHeight; v++ {
		for u := 0; u < wantWidth; u++ {
			x, y := orientationReference(orientation, u, v, width, height)
			want := src.RGBAAt(src.Bounds().Min.X+x, src.Bounds().Min.Y+y)
			if pixel := color.RGBAModel.Convert(got.At(bounds.Min.X+u, bounds.Min.Y+v)); pixel != want {
				t.Fatalf("orientation %d pixel (%d,%d) = %v, want source (%d,%d) %v", orientation, u, v, pixel, x, y, want)
			}
		}
	}
}

func TestRotateImageConvertsNonRGBAAndKeepsUnknownOrientations(t *testing.T) {
	t.Parallel()
	gray := image.NewGray(image.Rect(0, 0, 3, 2))
	gray.Pix = []uint8{1, 2, 3, 4, 5, 6}
	rotated := RotateImage(gray, 6)
	if rotated.Bounds() != image.Rect(0, 0, 2, 3) {
		t.Fatalf("rotated gray bounds = %v", rotated.Bounds())
	}
	if pixel := color.GrayModel.Convert(rotated.At(0, 0)).(color.Gray); pixel.Y != 4 {
		t.Fatalf("rotated gray (0,0) = %d, want bottom-left source value 4", pixel.Y)
	}
	for _, orientation := range []int{0, 1, 9, -3} {
		if RotateImage(gray, orientation) != image.Image(gray) {
			t.Fatalf("orientation %d did not return the source image", orientation)
		}
	}
	empty := image.NewRGBA(image.Rect(0, 0, 0, 4))
	if got := RotateImage(empty, 6).Bounds(); got != image.Rect(0, 0, 4, 0) {
		t.Fatalf("empty rotation bounds = %v", got)
	}
}
