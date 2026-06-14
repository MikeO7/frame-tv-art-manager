package optimize

import (
	"image"
	"image/color"
	std_draw "image/draw"
)

// CreateCollage joins two upright portrait images side-by-side into a single 3840x2160 landscape canvas,
// utilizing center cropping on each image to fit the 1920x2160 bounds and drawing a clean dark divider line.
func CreateCollage(img1, img2 *image.RGBA, smart bool) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, 3840, 2160))

	// Crop and scale both to 1920x2160
	left := centerCrop(img1, 1920, 2160, smart)
	right := centerCrop(img2, 1920, 2160, smart)

	// Draw them side-by-side
	std_draw.Draw(canvas, image.Rect(0, 0, 1920, 2160), left, left.Bounds().Min, std_draw.Src)
	std_draw.Draw(canvas, image.Rect(1920, 0, 3840, 2160), right, right.Bounds().Min, std_draw.Src)

	// Draw a thin 4-pixel separating vertical line (dark charcoal / off-black)
	dividerColor := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	for y := 0; y < 2160; y++ {
		for x := 1918; x <= 1921; x++ {
			canvas.Set(x, y, dividerColor)
		}
	}

	return canvas
}
