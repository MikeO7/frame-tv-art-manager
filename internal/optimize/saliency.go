package optimize

import (
	"image"
	"math"

	"golang.org/x/image/draw"
)

// findBestDirectorCrop implements the 'Director's Cut' Smart Crop v4.0.
// It uses a two-pass system:
// 1. Global BMS analysis at 256px to find the primary Region of Interest.
// 2. High-res Micro-Refinement at the focal point to optimize for edge alignment.
func findBestDirectorCrop(src *image.RGBA, windowW, windowH int, horizontal bool) int {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	// PASS 1: Global Analysis (256px)
	const workSize = 256
	scale := float64(workSize) / float64(srcW)
	if !horizontal {
		scale = float64(workSize) / float64(srcH)
	}
	workW, workH := int(float64(srcW)*scale), int(float64(srcH)*scale)
	workImg := image.NewRGBA(image.Rect(0, 0, workW, workH))
	draw.NearestNeighbor.Scale(workImg, workImg.Bounds(), src, src.Bounds(), draw.Src, nil)

	saliencyMap := generateSaliencyMap(workImg)
	integral := calculateIntegralImage(saliencyMap, workW, workH)

	mapWinW := max(int(float64(windowW)*scale), 1)
	mapWinH := max(int(float64(windowH)*scale), 1)

	bestMapPos := scanBestWindow(integral, workW, workH, mapWinW, mapWinH, horizontal)
	globalOffset := int(float64(bestMapPos) / scale)

	// PASS 2: High-Res Micro-Refinement (Fine-tuning at the focal point)
	// We search within a +/- 5% range at a higher resolution to snap to sharp edges.
	return refineOffset(src, globalOffset, windowW, windowH, horizontal)
}

// scanBestWindow slides the crop window along the saliency integral image and
// returns the start offset (on the active axis) whose enclosed saliency is
// greatest.
func scanBestWindow(integral []float64, workW, workH, mapWinW, mapWinH int, horizontal bool) int {
	best, maxScore := 0, -1.0
	if horizontal {
		for mx := 0; mx <= workW-mapWinW; mx++ {
			rect := image.Rect(mx, 0, mx+mapWinW-1, workH-1)
			if score := getRectSum(integral, rect, workW); score > maxScore {
				maxScore, best = score, mx
			}
		}
		return best
	}
	for my := 0; my <= workH-mapWinH; my++ {
		rect := image.Rect(0, my, workW-1, my+mapWinH-1)
		if score := getRectSum(integral, rect, workW); score > maxScore {
			maxScore, best = score, my
		}
	}
	return best
}

// clampOffset ensures a crop offset is within valid image bounds.
func clampOffset(offset, windowSize, srcSize int) int {
	if offset < 0 {
		return 0
	}
	if offset+windowSize > srcSize {
		return srcSize - windowSize
	}
	return offset
}

// refineOffset performs a local search at high resolution to fine-tune the crop.
func refineOffset(src *image.RGBA, globalOffset, windowW, windowH int, horizontal bool) int {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	// Search range: +/- 2% of the total dimension
	var searchRange int
	if horizontal {
		searchRange = int(float64(srcW) * 0.02)
	} else {
		searchRange = int(float64(srcH) * 0.02)
	}
	if searchRange < 1 {
		return globalOffset
	}

	bestOffset := globalOffset
	maxScore := -1.0

	// Local search at high resolution
	for delta := -searchRange; delta <= searchRange; delta++ {
		offset := globalOffset + delta

		if horizontal {
			offset = clampOffset(offset, windowW, srcW)
		} else {
			offset = clampOffset(offset, windowH, srcH)
		}

		// Quick edge-score of the boundary
		var score float64
		if horizontal {
			score = calculateEdgeScore(src, offset, 0, offset+windowW, srcH)
		} else {
			score = calculateEdgeScore(src, 0, offset, srcW, offset+windowH)
		}

		if score > maxScore {
			maxScore = score
			bestOffset = offset
		}
	}

	return bestOffset
}

// calculateEdgeScore calculates a simplified Sobel sum for micro-refinement.
func calculateEdgeScore(src *image.RGBA, x1, y1, x2, y2 int) float64 {
	score := 0.0
	// Sample only the corners and midpoints for speed in refinement
	samples := [][]int{
		{x1, y1},
		{x2 - 1, y1},
		{x1, y2 - 1},
		{x2 - 1, y2 - 1},
		{(x1 + x2) / 2, (y1 + y2) / 2},
	}
	for _, s := range samples {
		score += calculateSobelEdge(src, s[0], s[1])
	}
	return score
}

// generateSaliencyMap creates a 2D map where each pixel represents a saliency score.
// It combines structural edges (Sobel), skin tone detection, and Boolean Map Saliency (BMS).
//
//nolint:funlen // single fused per-pixel loop kept monolithic to avoid per-pixel call overhead in this hot path
func generateSaliencyMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	mapData := make([]float64, w*h)

	// 1. Generate BMS (Boolean Map Saliency) surroundedness map.
	// But first, precompute luminance for everything
	srcPix := src.Pix
	lumMapInt := make([]int, w*h)
	for i := 0; i < w*h; i++ {
		idx := i * 4
		lumMapInt[i] = int(srcPix[idx])*299 + int(srcPix[idx+1])*587 + int(srcPix[idx+2])*114
	}

	bmsMap := generateBMSMapFromLum(lumMapInt, w, h)

	// 2. Precompute Mean Lab Color of the entire image to measure color distance
	var sumL, sumA, sumB float64
	totalPixels := float64((w - 2) * (h - 2))

	// OPTIMIZATION: Precompute Lab colors to avoid recalculating per-pixel later
	labMap := make([]labColor, w*h)
	srcStride := src.Stride
	for y := 1; y < h-1; y++ {
		yStride := y * srcStride
		yW := y * w
		for x := 1; x < w-1; x++ {
			idx := yStride + x*4
			r, g, b := srcPix[idx], srcPix[idx+1], srcPix[idx+2]
			l, aVal, bVal := rgbToLab(r, g, b)
			labMap[yW+x] = labColor{l: l, a: aVal, b: bVal}
			sumL += l
			sumA += aVal
			sumB += bVal
		}
	}
	meanL := sumL / totalPixels
	meanA := sumA / totalPixels
	meanB := sumB / totalPixels
	meanLabColor := labColor{l: meanL, a: meanA, b: meanB}

	// OPTIMIZATION: Precalculate coordinate-dependent aesthetic factors to avoid heavy math in inner loop
	thirdX := make([]float64, w)
	dxSq := make([]float64, w)
	balanceX := make([]float64, w)
	for x := 1; x < w-1; x++ {
		nx := float64(x) / float64(w)
		dx := nx - 0.5
		dxSq[x] = dx * dx
		tx1, tx2 := nx-0.33, nx-0.66
		thirdX[x] = math.Exp(-(tx1*tx1)/0.02) + math.Exp(-(tx2*tx2)/0.02)
		if math.Abs(dx) > 0.4 {
			balanceX[x] = -0.05
		}
	}
	thirdY := make([]float64, h)
	dySq := make([]float64, h)
	for y := 1; y < h-1; y++ {
		ny := float64(y) / float64(h)
		dy := ny - 0.5
		dySq[y] = dy * dy
		ty1, ty2 := ny-0.33, ny-0.66
		thirdY[y] = math.Exp(-(ty1*ty1)/0.02) + math.Exp(-(ty2*ty2)/0.02)
	}

	for y := 1; y < h-1; y++ {
		yStride := y * srcStride
		yW := y * w
		dySqY := dySq[y]
		thirdYY := thirdY[y]
		for x := 1; x < w-1; x++ {
			idx := yStride + x*4
			r, g, b := srcPix[idx], srcPix[idx+1], srcPix[idx+2]

			// 3. Structural Saliency (Edge Detection via 3x3 Sobel)
			i00 := yW - w + x - 1
			i10 := i00 + 1
			i20 := i10 + 1

			i01 := yW + x - 1
			i21 := i01 + 2

			i02 := yW + w + x - 1
			i12 := i02 + 1
			i22 := i12 + 1

			l00 := lumMapInt[i00]
			l10 := lumMapInt[i10]
			l20 := lumMapInt[i20]

			l01 := lumMapInt[i01]
			l21 := lumMapInt[i21]

			l02 := lumMapInt[i02]
			l12 := lumMapInt[i12]
			l22 := lumMapInt[i22]

			gx := -l00 - (l01 << 1) - l02 + l20 + (l21 << 1) + l22
			gy := -l00 - (l10 << 1) - l20 + l02 + (l12 << 1) + l22

			edge := math.Sqrt(float64(gx)*float64(gx)+float64(gy)*float64(gy)) / 255000.0

			// 4. Skin Tone Saliency (Heuristic)
			rf, gf, bf := float64(r), float64(g), float64(b)
			cb := 128 - 0.168736*rf - 0.331264*gf + 0.5*bf
			cr := 128 + 0.5*rf - 0.418688*gf - 0.081312*bf
			skin := 0.0
			if cb >= 77 && cb <= 127 && cr >= 133 && cr <= 173 {
				skin = 1.0
			}

			// 5. Object Saliency (BMS)
			object := bmsMap[yW+x]

			// 6. Perceptual Lab Saliency (Color Contrast using CIEDE2000 color difference)
			// Measures true perceptual distance from the average image background color
			c := labMap[yW+x]
			colorWeight := ciede2000(c, meanLabColor) / 100.0
			if colorWeight > 1.0 {
				colorWeight = 1.0
			}

			// 7. Aesthetic/Compositional Weight (Rule of Thirds + Balance)
			centerBias := 0.1 * (1.0 - math.Sqrt(dxSq[x]+dySqY))
			aesthetic := centerBias + ((thirdX[x] + thirdYY) * 0.25) + balanceX[x]

			// Weighted Fusion v4.1
			// BMS is the core, with perceptual CIEDE2000 color distance as the color contrast weight.
			fusion := (object * 0.40) + (edge * 0.20) + (skin * 0.25) + (colorWeight * 0.15)
			mapData[yW+x] = fusion * (1.0 + aesthetic)
		}
	}
	return mapData
}

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

func calculateIntegralImage(saliencyMap []float64, w, h int) []float64 {
	integral := make([]float64, w*h)
	for y := 0; y < h; y++ {
		rowSum := 0.0
		yW := y * w
		prevYW := (y - 1) * w
		for x := 0; x < w; x++ {
			rowSum += saliencyMap[yW+x]
			if y == 0 {
				integral[yW+x] = rowSum
			} else {
				integral[yW+x] = integral[prevYW+x] + rowSum
			}
		}
	}
	return integral
}

// getRectSum returns the saliency sum over the rectangle r using the
// summed-area table. NOTE: r.Max is treated as inclusive (matching
// integral-image indexing), not the usual exclusive image.Rectangle bound.
func getRectSum(integral []float64, r image.Rectangle, w int) float64 {
	x1, y1, x2, y2 := r.Min.X, r.Min.Y, r.Max.X, r.Max.Y
	res := integral[y2*w+x2]
	if x1 > 0 {
		res -= integral[y2*w+x1-1]
	}
	if y1 > 0 {
		res -= integral[(y1-1)*w+x2]
	}
	if x1 > 0 && y1 > 0 {
		res += integral[(y1-1)*w+x1-1]
	}
	return res
}
