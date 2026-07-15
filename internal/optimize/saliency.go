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
	return findBestDirectorCropWithGain(src, windowW, windowH, horizontal, 0.03)
}

func findBestDirectorCropWithGain(src *image.RGBA, windowW, windowH int, horizontal bool, minGain float64) int {
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
	draw.CatmullRom.Scale(workImg, workImg.Bounds(), src, src.Bounds(), draw.Src, nil)

	saliencyMap := generateSaliencyMap(workImg)
	integral := calculateIntegralImage(saliencyMap, workW, workH)

	mapWinW := max(int(float64(windowW)*scale), 1)
	mapWinH := max(int(float64(windowH)*scale), 1)

	bestMapPos := scanBestWindow(windowScan{
		integral:   integral,
		workW:      workW,
		workH:      workH,
		mapWinW:    mapWinW,
		mapWinH:    mapWinH,
		horizontal: horizontal,
	})
	centerMapPos := max((workW-mapWinW)/2, 0)
	if !horizontal {
		centerMapPos = max((workH-mapWinH)/2, 0)
	}
	bestScore := cropWindowScore(integral, workW, workH, mapWinW, mapWinH, horizontal, bestMapPos)
	centerScore := cropWindowScore(integral, workW, workH, mapWinW, mapWinH, horizontal, centerMapPos)
	denominator := math.Max(math.Abs(centerScore), 1e-9)
	if (bestScore-centerScore)/denominator < max(minGain, 0) {
		if horizontal {
			return max((srcW-windowW)/2, 0)
		}
		return max((srcH-windowH)/2, 0)
	}
	globalOffset := int(float64(bestMapPos) / scale)

	// PASS 2: High-Res Micro-Refinement (Fine-tuning at the focal point)
	// We search within a +/- 5% range at a higher resolution to snap to sharp edges.
	return refineOffset(src, globalOffset, windowW, windowH, horizontal)
}

//nolint:revive // integral-image geometry is explicit to keep scoring allocation-free
func cropWindowScore(integral []float64, workW, workH, windowW, windowH int, horizontal bool, offset int) float64 {
	if horizontal {
		return getRectSum(integral, image.Rect(offset, 0, offset+windowW-1, workH-1), workW)
	}
	return getRectSum(integral, image.Rect(0, offset, workW-1, offset+windowH-1), workW)
}

// windowScan bundles the inputs for sliding a crop window across a saliency
// integral image.
type windowScan struct {
	integral         []float64
	workW, workH     int
	mapWinW, mapWinH int
	horizontal       bool
}

// scanBestWindow slides the crop window along the saliency integral image and
// returns the start offset (on the active axis) whose enclosed saliency is
// greatest.
func scanBestWindow(scan windowScan) int {
	integral := scan.integral
	workW, workH := scan.workW, scan.workH
	mapWinW, mapWinH := scan.mapWinW, scan.mapWinH
	horizontal := scan.horizontal

	axisSize, windowSize := workH, mapWinH
	if horizontal {
		axisSize, windowSize = workW, mapWinW
	}
	center := max((axisSize-windowSize)/2, 0)
	best, maxScore := center, -1.0
	isBetter := func(score float64, position int) bool {
		const tieTolerance = 1e-12
		return score > maxScore+tieTolerance ||
			(math.Abs(score-maxScore) <= tieTolerance && math.Abs(float64(position-center)) < math.Abs(float64(best-center)))
	}
	if horizontal {
		for mx := 0; mx <= workW-mapWinW; mx++ {
			rect := image.Rect(mx, 0, mx+mapWinW-1, workH-1)
			if score := getRectSum(integral, rect, workW); isBetter(score, mx) {
				maxScore, best = score, mx
			}
		}
		return best
	}
	for my := 0; my <= workH-mapWinH; my++ {
		rect := image.Rect(0, my, workW-1, my+mapWinH-1)
		if score := getRectSum(integral, rect, workW); isBetter(score, my) {
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
	minScore := math.Inf(1)

	// Local search at high resolution
	for delta := -searchRange; delta <= searchRange; delta++ {
		offset := globalOffset + delta

		if horizontal {
			offset = clampOffset(offset, windowW, srcW)
		} else {
			offset = clampOffset(offset, windowH, srcH)
		}

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
			minScore = score
			bestOffset = offset
		}
	}

	return bestOffset
}

// calculateEdgeScore estimates the edge energy crossed by the two active crop
// boundaries. Lower values are safer because they are less likely to bisect a
// salient subject.
func calculateEdgeScore(src *image.RGBA, x1, y1, x2, y2 int) float64 {
	const samples = 64
	score, count := 0.0, 0
	if y1 == src.Bounds().Min.Y && y2 == src.Bounds().Max.Y {
		for i := 0; i < samples; i++ {
			y := y1 + i*max(y2-y1-1, 0)/max(samples-1, 1)
			score += calculateSobelEdge(src, x1, y) + calculateSobelEdge(src, x2-1, y)
			count += 2
		}
	} else {
		for i := 0; i < samples; i++ {
			x := x1 + i*max(x2-x1-1, 0)/max(samples-1, 1)
			score += calculateSobelEdge(src, x, y1) + calculateSobelEdge(src, x, y2-1)
			count += 2
		}
	}
	if count == 0 {
		return 0
	}
	return score / float64(count)
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
