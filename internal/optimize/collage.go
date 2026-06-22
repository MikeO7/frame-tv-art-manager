package optimize

import (
	"image"
	"image/color"
	std_draw "image/draw"
)

// Collage canvas geometry: two portrait panels side-by-side on a 4K landscape
// canvas, separated by a thin dark seam centered on the midline.
const (
	collageWidth      = 3840             // 4K landscape canvas width
	collageHeight     = 2160             // 4K landscape canvas height
	collagePanelWidth = collageWidth / 2 // per-image panel width (1920)
	dividerHalfWidth  = 2                // seam extends dividerHalfWidth px each side of the midline
)

// CreateCollage joins two upright portrait images side-by-side into a single
// 4K (3840x2160) landscape canvas, center-cropping each to a half-width panel
// and drawing a clean dark divider line down the seam.
func CreateCollage(img1, img2 *image.RGBA, smart bool) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, collageWidth, collageHeight))

	left := centerCrop(img1, collagePanelWidth, collageHeight, smart)
	right := centerCrop(img2, collagePanelWidth, collageHeight, smart)

	std_draw.Draw(canvas, image.Rect(0, 0, collagePanelWidth, collageHeight), left, left.Bounds().Min, std_draw.Src)
	std_draw.Draw(canvas, image.Rect(collagePanelWidth, 0, collageWidth, collageHeight), right, right.Bounds().Min, std_draw.Src)

	// Dark charcoal / off-black seam centered on the panel boundary.
	dividerColor := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	for y := 0; y < collageHeight; y++ {
		for x := collagePanelWidth - dividerHalfWidth; x < collagePanelWidth+dividerHalfWidth; x++ {
			canvas.Set(x, y, dividerColor)
		}
	}

	return canvas
}
