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

	bestMapPos := scanBestWindow(windowScan{
		integral:   integral,
		workW:      workW,
		workH:      workH,
		mapWinW:    mapWinW,
		mapWinH:    mapWinH,
		horizontal: horizontal,
	})
	globalOffset := int(float64(bestMapPos) / scale)

	// PASS 2: High-Res Micro-Refinement (Fine-tuning at the focal point)
	// We search within a +/- 5% range at a higher resolution to snap to sharp edges.
	return refineOffset(src, globalOffset, windowW, windowH, horizontal)
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

			// Weighted Fusion v4.1
			// BMS is the core, with perceptual CIEDE2000 color distance as the color contrast weight.
			fusion := (object * 0.40) + (edge * 0.20) + (skin * 0.25) + (colorWeight * 0.15)
			mapData[yW+x] = fusion * (1.0 + aesthetic)
		}
	}
	return mapData
}
