package optimize

import (
	"image"
	"image/color"
	"testing"
)

const smartCropGoldenCorpusVersion = "v1"

func TestSmartCropGoldenCorpusV1(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                         string
		width, height                int
		cropWidth, cropHeight        int
		horizontal                   bool
		subject                      image.Rectangle
		wantMinOffset, wantMaxOffset int
	}{
		{
			name: "subject near left edge", width: 600, height: 300,
			cropWidth: 400, cropHeight: 300, horizontal: true,
			subject: image.Rect(30, 80, 120, 220), wantMinOffset: 0, wantMaxOffset: 30,
		},
		{
			name: "subject near right edge", width: 600, height: 300,
			cropWidth: 400, cropHeight: 300, horizontal: true,
			subject: image.Rect(480, 80, 570, 220), wantMinOffset: 170, wantMaxOffset: 200,
		},
		{
			name: "subject near lower edge", width: 300, height: 600,
			cropWidth: 300, cropHeight: 400, horizontal: false,
			subject: image.Rect(80, 480, 220, 570), wantMinOffset: 170, wantMaxOffset: 200,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := syntheticCropScene(test.width, test.height, test.subject)
			got := findBestDirectorCropWithGain(
				input, test.cropWidth, test.cropHeight, test.horizontal, DefaultConfig().SmartCropMinGain,
			)
			if got < test.wantMinOffset || got > test.wantMaxOffset {
				t.Fatalf(
					"smart crop golden corpus %s offset = %d, want [%d,%d]",
					smartCropGoldenCorpusVersion, got, test.wantMinOffset, test.wantMaxOffset,
				)
			}
		})
	}
}

func TestSmartCropGoldenCorpusV1CentersLowInformationImage(t *testing.T) {
	t.Parallel()
	input := image.NewRGBA(image.Rect(0, 0, 600, 300))
	fillRGBA(input, input.Bounds(), color.RGBA{R: 96, G: 96, B: 96, A: 255})
	if got := findBestDirectorCropWithGain(input, 400, 300, true, DefaultConfig().SmartCropMinGain); got != 100 {
		t.Fatalf("smart crop golden corpus %s low-information offset = %d, want 100", smartCropGoldenCorpusVersion, got)
	}
}

func TestSmartCropRepresentativeSyntheticCorpusV1(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		build                 func() (*image.RGBA, image.Rectangle)
		wantMin, wantMax      int
		centerMustMissSubject bool
	}{
		{name: "portrait warm skin in low light", build: syntheticPortraitCropScene, wantMin: 160, wantMax: 200, centerMustMissSubject: true},
		{name: "animal-like textured subject", build: syntheticAnimalCropScene, wantMin: 0, wantMax: 35, centerMustMissSubject: true},
		{name: "text and line art", build: syntheticLineArtCropScene, wantMin: 0, wantMax: 45, centerMustMissSubject: true},
		{name: "balanced landscape", build: syntheticLandscapeCropScene, wantMin: 85, wantMax: 115},
		{name: "balanced multiple subjects", build: syntheticMultipleSubjectCropScene, wantMin: 85, wantMax: 115},
		{name: "small edge distractor versus primary subject", build: syntheticDistractorCropScene, wantMin: 135, wantMax: 200, centerMustMissSubject: true},
		{name: "already good centered composition", build: syntheticCenteredCropScene, wantMin: 85, wantMax: 115},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input, subject := test.build()
			const cropWidth = 400
			got := findBestDirectorCropWithGain(
				input, cropWidth, input.Bounds().Dy(), true, DefaultConfig().SmartCropMinGain,
			)
			if got < test.wantMin || got > test.wantMax {
				t.Fatalf(
					"representative crop corpus %s offset = %d, want [%d,%d]",
					smartCropGoldenCorpusVersion, got, test.wantMin, test.wantMax,
				)
			}
			if test.centerMustMissSubject {
				center := (input.Bounds().Dx() - cropWidth) / 2
				if cropContainsSubject(center, cropWidth, subject) {
					t.Fatal("invalid corpus case: center crop unexpectedly retains the labeled subject")
				}
				if !cropContainsSubject(got, cropWidth, subject) {
					t.Fatalf("smart crop %d..%d does not retain labeled subject %v", got, got+cropWidth, subject)
				}
			}
		})
	}
}

func syntheticCropScene(width, height int, subject image.Rectangle) *image.RGBA {
	input := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRGBA(input, input.Bounds(), color.RGBA{R: 92, G: 98, B: 104, A: 255})
	for y := subject.Min.Y; y < subject.Max.Y; y++ {
		for x := subject.Min.X; x < subject.Max.X; x++ {
			if (x/8+y/8)%2 == 0 {
				input.SetRGBA(x, y, color.RGBA{R: 238, G: 92, B: 42, A: 255})
			} else {
				input.SetRGBA(x, y, color.RGBA{R: 24, G: 48, B: 188, A: 255})
			}
		}
	}
	return input
}

func fillRGBA(target *image.RGBA, rectangle image.Rectangle, value color.RGBA) {
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
			target.SetRGBA(x, y, value)
		}
	}
}

func syntheticPortraitCropScene() (*image.RGBA, image.Rectangle) {
	input := newCropScene()
	subject := image.Rect(465, 55, 575, 245)
	paintEllipse(input, 520, 135, 48, 70, color.RGBA{R: 166, G: 105, B: 76, A: 255})
	paintEllipse(input, 520, 232, 58, 45, color.RGBA{R: 62, G: 32, B: 27, A: 255})
	paintEllipse(input, 503, 125, 5, 5, color.RGBA{R: 20, G: 16, B: 14, A: 255})
	paintEllipse(input, 537, 125, 5, 5, color.RGBA{R: 20, G: 16, B: 14, A: 255})
	return input, subject
}

func syntheticAnimalCropScene() (*image.RGBA, image.Rectangle) {
	input := newCropScene()
	subject := image.Rect(20, 65, 125, 240)
	paintEllipse(input, 72, 155, 50, 78, color.RGBA{R: 202, G: 142, B: 58, A: 255})
	for y := 85; y < 220; y += 14 {
		fillRGBA(input, image.Rect(35, y, 110, y+5), color.RGBA{R: 52, G: 34, B: 22, A: 255})
	}
	return input, subject
}

func syntheticLineArtCropScene() (*image.RGBA, image.Rectangle) {
	input := newCropScene()
	subject := image.Rect(18, 55, 145, 245)
	for y := 65; y < 235; y += 24 {
		fillRGBA(input, image.Rect(24, y, 138, y+7), color.RGBA{R: 235, G: 235, B: 225, A: 255})
	}
	for x := 25; x < 140; x += 28 {
		fillRGBA(input, image.Rect(x, 55, x+5, 245), color.RGBA{R: 35, G: 45, B: 52, A: 255})
	}
	return input, subject
}

func syntheticLandscapeCropScene() (*image.RGBA, image.Rectangle) {
	input := image.NewRGBA(image.Rect(0, 0, 600, 300))
	fillRGBA(input, image.Rect(0, 0, 600, 175), color.RGBA{R: 115, G: 151, B: 178, A: 255})
	fillRGBA(input, image.Rect(0, 175, 600, 300), color.RGBA{R: 62, G: 89, B: 54, A: 255})
	for y := 90; y < 210; y++ {
		halfWidth := (y - 90) * 2
		fillRGBA(input, image.Rect(max(0, 300-halfWidth), y, min(600, 300+halfWidth), y+1), color.RGBA{R: 77, G: 71, B: 68, A: 255})
	}
	return input, image.Rect(240, 90, 360, 220)
}

func syntheticMultipleSubjectCropScene() (*image.RGBA, image.Rectangle) {
	input := newCropScene()
	paintEllipse(input, 215, 150, 45, 75, color.RGBA{R: 205, G: 75, B: 58, A: 255})
	paintEllipse(input, 385, 150, 45, 75, color.RGBA{R: 53, G: 105, B: 205, A: 255})
	return input, image.Rect(170, 75, 430, 225)
}

func syntheticDistractorCropScene() (*image.RGBA, image.Rectangle) {
	input := newCropScene()
	fillRGBA(input, image.Rect(0, 0, 14, 300), color.RGBA{R: 255, G: 255, B: 255, A: 255})
	subject := image.Rect(445, 65, 565, 240)
	paintEllipse(input, 505, 150, 58, 84, color.RGBA{R: 47, G: 176, B: 102, A: 255})
	for y := 90; y < 220; y += 18 {
		fillRGBA(input, image.Rect(460, y, 550, y+6), color.RGBA{R: 20, G: 54, B: 38, A: 255})
	}
	return input, subject
}

func syntheticCenteredCropScene() (*image.RGBA, image.Rectangle) {
	input := newCropScene()
	paintEllipse(input, 300, 150, 72, 92, color.RGBA{R: 180, G: 78, B: 145, A: 255})
	return input, image.Rect(228, 58, 372, 242)
}

func newCropScene() *image.RGBA {
	input := image.NewRGBA(image.Rect(0, 0, 600, 300))
	fillRGBA(input, input.Bounds(), color.RGBA{R: 58, G: 64, B: 69, A: 255})
	return input
}

func paintEllipse(target *image.RGBA, centerX, centerY, radiusX, radiusY int, value color.RGBA) {
	for y := centerY - radiusY; y <= centerY+radiusY; y++ {
		for x := centerX - radiusX; x <= centerX+radiusX; x++ {
			dx, dy := x-centerX, y-centerY
			if dx*dx*radiusY*radiusY+dy*dy*radiusX*radiusX <= radiusX*radiusX*radiusY*radiusY {
				target.SetRGBA(x, y, value)
			}
		}
	}
}

func cropContainsSubject(offset, width int, subject image.Rectangle) bool {
	return subject.Min.X >= offset && subject.Max.X <= offset+width
}
