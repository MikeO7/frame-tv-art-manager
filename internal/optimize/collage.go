package optimize

import (
	"image"
	"image/color"
	std_draw "image/draw"
)

const (
	collageWidth  = 3840
	collageHeight = 2160
)

// CreateCollage joins two upright portrait images side-by-side into a single
// 4K (3840x2160) landscape canvas, center-cropping each to a half-width panel
// and drawing a clean dark divider line down the seam.
func CreateCollage(img1, img2 *image.RGBA, smart bool) *image.RGBA {
	return CreateCollageForTarget(img1, img2, collageWidth, collageHeight, smart)
}

// CreateCollageForTarget joins two upright portraits into the requested
// landscape pixel contract.
func CreateCollageForTarget(img1, img2 *image.RGBA, targetWidth, targetHeight int, smart bool) *image.RGBA {
	return createCollageForTarget(img1, img2, targetWidth, targetHeight, smart, 0.03, true)
}

//nolint:revive // explicit target and crop-quality controls keep this renderer independent of global config
func createCollageForTarget(
	img1, img2 *image.RGBA,
	targetWidth, targetHeight int,
	smart bool,
	minGain float64,
	linearLight bool,
) *image.RGBA {
	return createCollageForTargetWithSharpen(
		img1, img2, targetWidth, targetHeight, smart, minGain, linearLight, 0, 0, 0, defaultPixelWorkers(),
	)
}

//nolint:revive // per-panel resize and sharpen controls keep mixed-size collages accurate
func createCollageForTargetWithSharpen(
	img1, img2 *image.RGBA,
	targetWidth, targetHeight int,
	smart bool,
	minGain float64,
	linearLight bool,
	leftSharpen, rightSharpen float64,
	sharpenThreshold, pixelWorkers int,
) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	leftWidth := targetWidth / 2
	rightWidth := targetWidth - leftWidth

	left := centerCropWithOptions(img1, leftWidth, targetHeight, smart, minGain, linearLight)
	right := centerCropWithOptions(img2, rightWidth, targetHeight, smart, minGain, linearLight)
	left = sharpenWithOptions(left, leftSharpen, sharpenThreshold, pixelWorkers)
	right = sharpenWithOptions(right, rightSharpen, sharpenThreshold, pixelWorkers)

	std_draw.Draw(canvas, image.Rect(0, 0, leftWidth, targetHeight), left, left.Bounds().Min, std_draw.Src)
	std_draw.Draw(canvas, image.Rect(leftWidth, 0, targetWidth, targetHeight), right, right.Bounds().Min, std_draw.Src)

	// Dark charcoal / off-black seam centered on the panel boundary.
	dividerColor := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	dividerHalfWidth := max(1, targetWidth/1920)
	for y := 0; y < targetHeight; y++ {
		for x := max(0, leftWidth-dividerHalfWidth); x < min(targetWidth, leftWidth+dividerHalfWidth); x++ {
			canvas.Set(x, y, dividerColor)
		}
	}

	return canvas
}
