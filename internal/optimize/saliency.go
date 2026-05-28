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
//
//nolint:nestif // complexity justified for this domain-specific path
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

	mapWinW := int(float64(windowW) * scale)
	mapWinH := int(float64(windowH) * scale)
	if mapWinW < 1 {
		mapWinW = 1
	}
	if mapWinH < 1 {
		mapWinH = 1
	}

	bestMapPos := 0
	maxScore := -1.0

	if horizontal {
		maxMapX := workW - mapWinW
		for mx := 0; mx <= maxMapX; mx++ {
			score := getRectSum(integral, mx, 0, mx+mapWinW-1, workH-1, workW)
			if score > maxScore {
				maxScore = score
				bestMapPos = mx
			}
		}
	} else {
		maxMapY := workH - mapWinH
		for my := 0; my <= maxMapY; my++ {
			score := getRectSum(integral, 0, my, workW-1, my+mapWinH-1, workW)
			if score > maxScore {
				maxScore = score
				bestMapPos = my
			}
		}
	}

	globalOffset := int(float64(bestMapPos) / scale)

	// PASS 2: High-Res Micro-Refinement (Fine-tuning at the focal point)
	// We search within a +/- 5% range at a higher resolution to snap to sharp edges.
	return refineOffset(src, globalOffset, windowW, windowH, horizontal)
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
func generateSaliencyMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	mapData := make([]float64, w*h)

	// 1. Generate BMS (Boolean Map Saliency) surroundedness map.
	bmsMap := generateBMSMap(src)

	// 2. Precompute Mean Lab Color of the entire image to measure color distance
	var sumL, sumA, sumB float64
	totalPixels := float64((w - 2) * (h - 2))
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*src.Stride + x*4
			r, g, b := src.Pix[idx], src.Pix[idx+1], src.Pix[idx+2]
			l, aVal, bVal := rgbToLab(r, g, b)
			sumL += l
			sumA += aVal
			sumB += bVal
		}
	}
	meanL := sumL / totalPixels
	meanA := sumA / totalPixels
	meanB := sumB / totalPixels

	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*src.Stride + x*4
			r, g, b := src.Pix[idx], src.Pix[idx+1], src.Pix[idx+2]

			// 3. Structural Saliency (Edge Detection via 3x3 Sobel)
			edge := calculateSobelEdge(src, x, y)

			// 4. Skin Tone Saliency (Heuristic)
			skin := calculateSkinProbability(r, g, b)

			// 5. Object Saliency (BMS)
			object := bmsMap[y*w+x]

			// 6. Perceptual Lab Saliency (Color Contrast using CIEDE2000 color difference)
			// Measures true perceptual distance from the average image background color
			lLab, aLab, bLab := rgbToLab(r, g, b)
			colorWeight := ciede2000(labColor{l: lLab, a: aLab, b: bLab}, labColor{l: meanL, a: meanA, b: meanB}) / 100.0
			if colorWeight > 1.0 {
				colorWeight = 1.0
			}

			// 7. Aesthetic/Compositional Weight (Rule of Thirds + Balance)
			aesthetic := calculateAestheticScore(x, y, w, h)

			// Weighted Fusion v4.1
			// BMS is the core, with perceptual CIEDE2000 color distance as the color contrast weight.
			fusion := (object * 0.40) + (edge * 0.20) + (skin * 0.25) + (colorWeight * 0.15)
			mapData[y*w+x] = fusion * (1.0 + aesthetic)
		}
	}
	return mapData
}

func calculateSobelEdge(src *image.RGBA, x, y int) float64 {
	lum := func(xx, yy int) float64 {
		bounds := src.Bounds()
		if xx < bounds.Min.X {
			xx = bounds.Min.X
		}
		if xx >= bounds.Max.X {
			xx = bounds.Max.X - 1
		}
		if yy < bounds.Min.Y {
			yy = bounds.Min.Y
		}
		if yy >= bounds.Max.Y {
			yy = bounds.Max.Y - 1
		}
		i := (yy-bounds.Min.Y)*src.Stride + (xx-bounds.Min.X)*4
		return 0.299*float64(src.Pix[i]) + 0.587*float64(src.Pix[i+1]) + 0.114*float64(src.Pix[i+2])
	}
	gx := -lum(x-1, y-1) - 2*lum(x-1, y) - lum(x-1, y+1) + lum(x+1, y-1) + 2*lum(x+1, y) + lum(x+1, y+1)
	gy := -lum(x-1, y-1) - 2*lum(x, y-1) - lum(x+1, y-1) + lum(x-1, y+1) + 2*lum(x, y+1) + lum(x+1, y+1)
	return math.Sqrt(gx*gx+gy*gy) / 255.0
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

func calculateAestheticScore(x, y, w, h int) float64 {
	nx, ny := float64(x)/float64(w), float64(y)/float64(h)
	dx, dy := nx-0.5, ny-0.5
	centerBias := 0.1 * (1.0 - math.Sqrt(dx*dx+dy*dy))

	// 1. Rule of Thirds (Gaussian weight around 0.33 and 0.66)
	tx1, tx2 := nx-0.33, nx-0.66
	ty1, ty2 := ny-0.33, ny-0.66
	thirdX := math.Exp(-(tx1*tx1)/0.02) + math.Exp(-(tx2*tx2)/0.02)
	thirdY := math.Exp(-(ty1*ty1)/0.02) + math.Exp(-(ty2*ty2)/0.02)

	// 2. Visual Mass Balance
	balanceBias := 0.0
	if math.Abs(dx) > 0.4 {
		balanceBias = -0.05
	}

	return centerBias + ((thirdX + thirdY) * 0.25) + balanceBias
}

func calculateIntegralImage(saliencyMap []float64, w, h int) []float64 {
	integral := make([]float64, w*h)
	for y := 0; y < h; y++ {
		rowSum := 0.0
		for x := 0; x < w; x++ {
			rowSum += saliencyMap[y*w+x]
			if y == 0 {
				integral[y*w+x] = rowSum
			} else {
				integral[y*w+x] = integral[(y-1)*w+x] + rowSum
			}
		}
	}
	return integral
}

//nolint:revive // complexity justified for this domain-specific path
func getRectSum(integral []float64, x1, y1, x2, y2, w int) float64 {
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
