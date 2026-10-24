package optimize

import (
	"image"
)

// polishPixel limits the maximum brightness and adds paper grain noise to a pixel.
func polishPixel(r, g, b float32, state *uint32) (uint8, uint8, uint8) {
	const maxBright = 235.0
	r, g, b = min(r, maxBright), min(g, maxBright), min(b, maxBright)

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
