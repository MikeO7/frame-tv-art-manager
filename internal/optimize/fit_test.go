package optimize

import (
	"context"
	"image"
	"image/color"
	"log/slog"
	"testing"
)

func TestPlanFit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		width, height int
		portraitMode  string
		maxCrop       int
		maxUpscale    float64
		want          fitMode
	}{
		{name: "exact 16:9 crops", width: 3840, height: 2160, maxCrop: 25, maxUpscale: 3, want: fitCrop},
		{name: "3:2 photo crops", width: 6000, height: 4000, maxCrop: 25, maxUpscale: 3, want: fitCrop},
		{name: "4:3 painting at the limit crops", width: 4000, height: 3000, maxCrop: 25, maxUpscale: 3, want: fitCrop},
		{name: "square art is matted", width: 3000, height: 3000, maxCrop: 25, maxUpscale: 3, want: fitPad},
		{name: "panorama is matted", width: 9000, height: 3000, maxCrop: 25, maxUpscale: 3, want: fitPad},
		{name: "crop limit 100 always crops landscapes", width: 3000, height: 3000, maxCrop: 100, maxUpscale: 3, want: fitCrop},
		{name: "small image beyond upscale limit is matted", width: 843, height: 461, maxCrop: 25, maxUpscale: 3, want: fitPad},
		{name: "small image within upscale limit crops", width: 1920, height: 1080, maxCrop: 25, maxUpscale: 3, want: fitCrop},
		{name: "portrait pads by default", width: 2000, height: 3000, portraitMode: "pad", maxCrop: 25, maxUpscale: 3, want: fitPad},
		{name: "portrait collage leftover pads", width: 2000, height: 3000, portraitMode: "collage", maxCrop: 25, maxUpscale: 3, want: fitPad},
		{name: "portrait crop mode crops", width: 2000, height: 3000, portraitMode: "crop", maxCrop: 25, maxUpscale: 3, want: fitCrop},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			cfg.PortraitMode, cfg.MaxCropPercent, cfg.MaxUpscale = test.portraitMode, test.maxCrop, test.maxUpscale
			if got := planFit(test.width, test.height, cfg); got != test.want {
				t.Fatalf("planFit(%dx%d) = %v, want %v", test.width, test.height, got, test.want)
			}
		})
	}
}

func TestFitImageMatsPortraitWithoutCroppingIt(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 1920, 1080
	src := solidImage(200, 300, color.RGBA{R: 220, A: 255})
	// A green stripe along the top and bottom edges must survive: cropping
	// would remove it, matting keeps it.
	for x := range 200 {
		for y := range 10 {
			src.SetRGBA(x, y, color.RGBA{G: 200, A: 255})
			src.SetRGBA(x, 299-y, color.RGBA{G: 200, A: 255})
		}
	}
	out := fitImage(context.Background(), src, cfg, slog.New(slog.DiscardHandler))
	if out.Bounds() != image.Rect(0, 0, 1920, 1080) {
		t.Fatalf("bounds = %v", out.Bounds())
	}
	top, bottom := -1, -1
	for y := range 1080 {
		if pixel := out.RGBAAt(960, y); pixel.G > 150 && pixel.R < 80 {
			if top < 0 {
				top = y
			}
			bottom = y
		}
	}
	if top < 0 || bottom-top < 850 {
		t.Fatalf("top and bottom edges not both visible: first green row %d, last %d", top, bottom)
	}
	if corner := out.RGBAAt(5, 5); corner == (color.RGBA{R: 220, A: 255}) {
		t.Fatalf("corner shows art instead of mat: %+v", corner)
	}
}

func solidImage(width, height int, fill color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, fill)
		}
	}
	return img
}

func isRed(pixel color.RGBA) bool {
	return pixel.R > 180 && pixel.G < 60 && pixel.B < 60
}

// redSpan counts the red pixels on row y.
func redSpan(img *image.RGBA, y int) int {
	count := 0
	for x := range img.Bounds().Dx() {
		if isRed(img.RGBAAt(x, y)) {
			count++
		}
	}
	return count
}

// firstRedRow returns the first row whose pixel in column x is red, or -1.
func firstRedRow(img *image.RGBA, x int) int {
	for y := range img.Bounds().Dy() {
		if isRed(img.RGBAAt(x, y)) {
			return y
		}
	}
	return -1
}
