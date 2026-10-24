//nolint:revive // crop analysis stays cohesive so its scoring conventions cannot drift across packages
package optimize

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

func findBestDirectorCropWithProtection(
	src *image.RGBA,
	windowW, windowH int,
	horizontal bool,
	minGain float64,
	protect bool,
	protectionStrength float64,
) int {
	srcW, srcH := src.Bounds().Dx(), src.Bounds().Dy()
	srcSize, windowSize := srcH, windowH
	if horizontal {
		srcSize, windowSize = srcW, windowW
	}

	// PASS 1: Global Analysis (256px)
	const workSize = 256
	scale := float64(workSize) / float64(srcSize)
	workW, workH := int(float64(srcW)*scale), int(float64(srcH)*scale)
	workImg := image.NewRGBA(image.Rect(0, 0, workW, workH))
	draw.CatmullRom.Scale(workImg, workImg.Bounds(), src, src.Bounds(), draw.Src, nil)

	windows := cropWindows{
		saliency:   calculateIntegralImage(generateSaliencyMap(workImg), workW, workH),
		workW:      workW,
		workH:      workH,
		windowW:    max(int(float64(windowW)*scale), 1),
		windowH:    max(int(float64(windowH)*scale), 1),
		horizontal: horizontal,
	}
	protected := protect && protectionStrength > 0
	if protected {
		windows.protection = calculateIntegralImage(generateProtectedRegionMap(workImg), workW, workH)
		windows.strength = protectionStrength
	}

	bestMapPos := windows.best()
	bestScore, centerScore := windows.score(bestMapPos), windows.score(windows.center())
	if (bestScore-centerScore)/math.Max(math.Abs(centerScore), 1e-9) < max(minGain, 0) {
		return max((srcSize-windowSize)/2, 0)
	}
	globalOffset := int(float64(bestMapPos) / scale)
	if protected {
		return clampOffset(globalOffset, windowSize, srcSize)
	}

	// PASS 2: High-Res Micro-Refinement (Fine-tuning at the focal point)
	// We search within a +/- 2% range at full resolution to snap to sharp edges.
	return refineOffset(src, globalOffset, windowW, windowH, horizontal)
}

// cropWindows scores crop windows sliding along one axis of an analysis-scale
// saliency integral image. With a protection integral, windows whose
// boundaries cross protected detail are penalized.
type cropWindows struct {
	saliency, protection []float64
	workW, workH         int
	windowW, windowH     int
	horizontal           bool
	strength             float64
}

// span returns the active axis length and the window length along it.
func (c cropWindows) span() (int, int) {
	if c.horizontal {
		return c.workW, c.windowW
	}
	return c.workH, c.windowH
}

// axisRect returns the inclusive integral-image rectangle covering [first, last]
// on the active axis and the full extent of the other axis.
func (c cropWindows) axisRect(first, last int) image.Rectangle {
	if c.horizontal {
		return image.Rect(first, 0, last, c.workH-1)
	}
	return image.Rect(0, first, c.workW-1, last)
}

func (c cropWindows) center() int {
	axis, window := c.span()
	return max((axis-window)/2, 0)
}

// best returns the offset with the greatest score; among equal scores it
// prefers the offset nearest the center.
func (c cropWindows) best() int {
	const tieTolerance = 1e-12
	axis, window := c.span()
	center := c.center()
	best, maxScore := center, math.Inf(-1)
	for offset := 0; offset <= axis-window; offset++ {
		score := c.score(offset)
		if score > maxScore+tieTolerance ||
			(math.Abs(score-maxScore) <= tieTolerance && math.Abs(float64(offset-center)) < math.Abs(float64(best-center))) {
			maxScore, best = score, offset
		}
	}
	return best
}

func (c cropWindows) score(offset int) float64 {
	axis, window := c.span()
	score := getRectSum(c.saliency, c.axisRect(offset, offset+window-1), c.workW)
	if len(c.protection) == 0 || c.strength <= 0 {
		return score
	}
	band := max(2, min(c.windowW, c.windowH)/32)
	edge := func(position int) float64 {
		return getRectSum(c.protection, c.axisRect(max(position-band, 0), min(position+band, axis)-1), c.workW)
	}
	boundary := edge(offset) + edge(offset+window)
	windowArea := float64(c.windowW * c.windowH)
	boundaryArea := float64(max(1, 4*band*max(c.workW, c.workH)))
	return score - 4*c.strength*boundary*windowArea/boundaryArea
}

// generateProtectedRegionMap marks high-frequency text/line detail and
// skin-like subject pixels so candidate boundaries avoid bisecting them.
func generateProtectedRegionMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	result := make([]float64, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			pixel := src.RGBAAt(x, y)
			edge := clampUnit(calculateSobelEdge(src, x, y))
			centerLuma := pixelLuma(pixel)
			localContrast := 0.0
			for _, point := range [...]image.Point{{X: -1}, {X: 1}, {Y: -1}, {Y: 1}} {
				localContrast = math.Max(localContrast, math.Abs(centerLuma-pixelLuma(src.RGBAAt(x+point.X, y+point.Y))))
			}
			skin := calculateSkinProbability(pixel.R, pixel.G, pixel.B)
			value := math.Max(math.Max(edge, localContrast), 0.75*skin)
			if value >= 0.18 {
				result[y*w+x] = value
			}
		}
	}
	return result
}

func pixelLuma(pixel color.RGBA) float64 {
	return (0.2126*float64(pixel.R) + 0.7152*float64(pixel.G) + 0.0722*float64(pixel.B)) / 255
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

// refineOffset performs a local search at full resolution to fine-tune the crop.
func refineOffset(src *image.RGBA, globalOffset, windowW, windowH int, horizontal bool) int {
	srcW, srcH := src.Bounds().Dx(), src.Bounds().Dy()
	srcSize, windowSize := srcH, windowH
	if horizontal {
		srcSize, windowSize = srcW, windowW
	}
	// Search range: +/- 2% of the active dimension.
	searchRange := int(float64(srcSize) * 0.02)
	if searchRange < 1 {
		return globalOffset
	}

	bestOffset, minScore := globalOffset, math.Inf(1)
	for delta := -searchRange; delta <= searchRange; delta++ {
		offset := clampOffset(globalOffset+delta, windowSize, srcSize)
		// Prefer crop boundaries that cross less edge energy while remaining near
		// the globally selected saliency window.
		var score float64
		if horizontal {
			score = calculateEdgeScore(src, offset, 0, offset+windowW, srcH)
		} else {
			score = calculateEdgeScore(src, 0, offset, srcW, offset+windowH)
		}
		score += 0.02 * math.Abs(float64(delta)) / float64(searchRange)
		if score < minScore {
			minScore, bestOffset = score, offset
		}
	}
	return bestOffset
}

// calculateEdgeScore estimates the edge energy crossed by the two active crop
// boundaries. Lower values are safer because they are less likely to bisect a
// salient subject.
func calculateEdgeScore(src *image.RGBA, x1, y1, x2, y2 int) float64 {
	const samples = 64
	score := 0.0
	if y1 == src.Bounds().Min.Y && y2 == src.Bounds().Max.Y {
		for i := range samples {
			y := y1 + i*max(y2-y1-1, 0)/(samples-1)
			score += calculateSobelEdge(src, x1, y) + calculateSobelEdge(src, x2-1, y)
		}
	} else {
		for i := range samples {
			x := x1 + i*max(x2-x1-1, 0)/(samples-1)
			score += calculateSobelEdge(src, x, y1) + calculateSobelEdge(src, x, y2-1)
		}
	}
	return score / (2 * samples)
}

// generateSaliencyMap creates a 2D map where each pixel represents a saliency score.
// It combines structural edges (Sobel), skin tone detection, and Boolean Map Saliency (BMS).
//
//nolint:funlen // single fused per-pixel loop kept monolithic to avoid per-pixel call overhead in this hot path
func generateSaliencyMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	mapData := make([]float64, w*h)

	// 1. Generate BMS (Boolean Map Saliency) surroundedness map.
	bmsMap := generateBMSMap(src)

	// 2. Precompute Mean Lab Color of the entire image to measure color distance
	var sumL, sumA, sumB float64
	totalPixels := float64((w - 2) * (h - 2))

	// OPTIMIZATION: Precompute Lab colors to avoid recalculating per-pixel later
	labMap := make([]labColor, w*h)
	srcStride := src.Stride
	srcPix := src.Pix
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
			edge := calculateSobelEdge(src, x, y)

			// 4. Skin Tone Saliency (Heuristic)
			skin := calculateSkinProbability(r, g, b)

			// 5. Object Saliency (BMS)
			object := bmsMap[yW+x]

			// 6. Perceptual Lab Saliency (Color Contrast using CIEDE2000 color difference)
			// Measures true perceptual distance from the average image background color
			c := labMap[yW+x]
			colorWeight := ciede2000(c, labColor{l: meanL, a: meanA, b: meanB}) / 100.0
			if colorWeight > 1.0 {
				colorWeight = 1.0
			}

			// 7. Aesthetic/Compositional Weight (Rule of Thirds + Balance)
			centerBias := 0.1 * (1.0 - math.Sqrt(dxSq[x]+dySqY))
			aesthetic := centerBias + ((thirdX[x] + thirdYY) * 0.25) + balanceX[x]

			fusion := (object * 0.45) + (edge * 0.25) + (skin * 0.10) + (colorWeight * 0.20)
			mapData[yW+x] = min(max(fusion*(1.0+aesthetic), 0), 1)
		}
	}
	return mapData
}
