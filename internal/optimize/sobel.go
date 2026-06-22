package optimize

import (
	"image"
	"math"
)

func calculateSobelEdge(src *image.RGBA, x, y int) float64 {
	bounds := src.Bounds()
	minX, maxX, minY, maxY := bounds.Min.X, bounds.Max.X-1, bounds.Min.Y, bounds.Max.Y-1

	// FAST PATH: If the pixel is strictly inside the boundaries, skip all bounds checking
	// and perform fast inline integer arithmetic for luminosity.
	if x > minX && x < maxX && y > minY && y < maxY {
		stride := src.Stride
		pix := src.Pix
		yMinYStride := (y - minY) * stride
		xMinX4 := (x - minX) * 4

		i00 := yMinYStride - stride + xMinX4 - 4
		i10 := i00 + 4
		i20 := i10 + 4

		i01 := yMinYStride + xMinX4 - 4
		i21 := i01 + 8

		i02 := yMinYStride + stride + xMinX4 - 4
		i12 := i02 + 4
		i22 := i12 + 4

		l00 := int(pix[i00])*299 + int(pix[i00+1])*587 + int(pix[i00+2])*114
		l10 := int(pix[i10])*299 + int(pix[i10+1])*587 + int(pix[i10+2])*114
		l20 := int(pix[i20])*299 + int(pix[i20+1])*587 + int(pix[i20+2])*114

		l01 := int(pix[i01])*299 + int(pix[i01+1])*587 + int(pix[i01+2])*114
		l21 := int(pix[i21])*299 + int(pix[i21+1])*587 + int(pix[i21+2])*114

		l02 := int(pix[i02])*299 + int(pix[i02+1])*587 + int(pix[i02+2])*114
		l12 := int(pix[i12])*299 + int(pix[i12+1])*587 + int(pix[i12+2])*114
		l22 := int(pix[i22])*299 + int(pix[i22+1])*587 + int(pix[i22+2])*114

		gx := -l00 - (l01 << 1) - l02 + l20 + (l21 << 1) + l22
		gy := -l00 - (l10 << 1) - l20 + l02 + (l12 << 1) + l22

		// Cast gx and gy to float64 before squaring to prevent 32-bit integer overflow on 32-bit architectures
		// Divide by 255000.0 instead of 255.0 because we scaled our RGB values by 1000
		return math.Sqrt(float64(gx)*float64(gx)+float64(gy)*float64(gy)) / 255000.0
	}

	return calculateSobelEdgeSlow(src, x, y, bounds)
}

func calculateSobelEdgeSlow(src *image.RGBA, x, y int, bounds image.Rectangle) float64 {
	// SLOW PATH: Edges requiring boundary enforcement
	minX := bounds.Min.X
	maxX := bounds.Max.X - 1
	minY := bounds.Min.Y
	maxY := bounds.Max.Y - 1
	stride := src.Stride
	pix := src.Pix

	lum := func(xx, yy int) int {
		if xx < minX {
			xx = minX
		} else if xx > maxX {
			xx = maxX
		}
		if yy < minY {
			yy = minY
		} else if yy > maxY {
			yy = maxY
		}
		i := (yy-minY)*stride + (xx-minX)*4
		// OPTIMIZATION: Replacing floating point multiplication with integer math for much faster luminance calculation
		return int(pix[i])*299 + int(pix[i+1])*587 + int(pix[i+2])*114
	}

	lum00 := lum(x-1, y-1)
	lum10 := lum(x, y-1)
	lum20 := lum(x+1, y-1)
	lum01 := lum(x-1, y)
	lum21 := lum(x+1, y)
	lum02 := lum(x-1, y+1)
	lum12 := lum(x, y+1)
	lum22 := lum(x+1, y+1)

	gx := -lum00 - (lum01 << 1) - lum02 + lum20 + (lum21 << 1) + lum22
	gy := -lum00 - (lum10 << 1) - lum20 + lum02 + (lum12 << 1) + lum22

	gxFloat := float64(gx)
	gyFloat := float64(gy)

	return math.Sqrt(gxFloat*gxFloat+gyFloat*gyFloat) / 255000.0
}

func calculateSkinProbability(r, g, b uint8) float64 {
	rf, gf, bf := float64(r), float64(g), float64(b)
	cb := 128 - 0.168736*rf - 0.331264*gf + 0.5*bf
	cr := 128 + 0.5*rf - 0.418688*gf - 0.081312*bf
	if cb >= 77 && cb <= 127 && cr >= 133 && cr <= 173 {
		return 1.0
	}
	return 0.0
}
