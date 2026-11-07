package optimize

import (
	"image"
	"math"
)

// Highlights roll off smoothly from the knee toward paperWhite instead of
// clipping, so bright detail (clouds, lace, snow) stays distinguishable.
const (
	highlightKnee = 0.8 * 255
	paperWhite    = 235.0
)

// rollOffHighlight compresses values above the knee along a tanh shoulder
// that approaches paperWhite.
func rollOffHighlight(value float32) float32 {
	if value <= highlightKnee {
		return value
	}
	const span = paperWhite - highlightKnee
	return highlightKnee + span*float32(math.Tanh(float64(value-highlightKnee)/span))
}

// polishPixel rolls off highlights to a paper white and adds paper grain noise to a pixel.
func polishPixel(r, g, b float32, state *uint32) (uint8, uint8, uint8) {
	r, g, b = rollOffHighlight(r), rollOffHighlight(g), rollOffHighlight(b)

	avg := (r + g + b) * 0.33333333
	r = r*0.92 + avg*0.08
	g = g*0.92 + avg*0.08
	b = b*0.92 + avg*0.08

	*state ^= *state << 13
	*state ^= *state >> 17
	*state ^= *state << 5

	noise := (float32(*state)/float32(0xFFFFFFFF) - 0.5) * 5.0
	r += noise
	g += noise
	b += noise
	return roundToByte(r), roundToByte(g), roundToByte(b)
}

func galleryMasterPolish(src *image.RGBA) *image.RGBA {
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	runPixelRows(0, height, func(_, startY, endY int) {
		//nolint:gosec // startY is a positive row offset, conversion is mathematically safe
		state := uint32(startY + 1) // Seed based on row
		pix, stride := src.Pix, src.Stride
		for y := startY; y < endY; y++ {
			offset := y * stride
			for x := range width {
				i := offset + x*4
				pix[i], pix[i+1], pix[i+2] = polishPixel(float32(pix[i]), float32(pix[i+1]), float32(pix[i+2]), &state)
			}
		}
	})
	return src
}
