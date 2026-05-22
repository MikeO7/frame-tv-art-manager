package optimize

import (
	"image"
	"math"
	"sync"

	"golang.org/x/image/draw"
)

var (
	lutRgbToLab     [256]float64
	lutRgbToLabOnce sync.Once
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
	searchRange := 0
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
		score := 0.0
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
		{x1, y1}, {x2 - 1, y1}, {x1, y2 - 1}, {x2 - 1, y2 - 1},
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
			colorWeight := ciede2000(lLab, aLab, bLab, meanL, meanA, meanB) / 100.0
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

// calculateDeltaHPrime computes delta H prime for CIEDE2000 color difference.
func calculateDeltaHPrime(cPrime1, cPrime2, hPrime1, hPrime2 float64) float64 {
	if cPrime1*cPrime2 == 0 {
		return 0
	}
	deltahPrime := hPrime2 - hPrime1
	if math.Abs(deltahPrime) > 180.0 {
		if hPrime2 > hPrime1 {
			deltahPrime -= 360.0
		} else {
			deltahPrime += 360.0
		}
	}
	return 2.0 * math.Sqrt(cPrime1*cPrime2) * math.Sin(deltahPrime*math.Pi/360.0)
}

// calculateMeanHPrime computes mean H prime for CIEDE2000 color difference.
func calculateMeanHPrime(cPrime1, cPrime2, hPrime1, hPrime2 float64) float64 {
	if cPrime1*cPrime2 == 0 {
		return hPrime1 + hPrime2
	}
	if math.Abs(hPrime1-hPrime2) <= 180.0 {
		return (hPrime1 + hPrime2) / 2.0
	}
	if hPrime1+hPrime2 < 360.0 {
		return (hPrime1 + hPrime2 + 360.0) / 2.0
	}
	return (hPrime1 + hPrime2 - 360.0) / 2.0
}

// ciede2000 calculates the exact CIE 2000 color-difference standard between two CIELAB colors.
func ciede2000(l1, a1, b1, l2, a2, b2 float64) float64 {
	c1 := math.Sqrt(a1*a1 + b1*b1)
	c2 := math.Sqrt(a2*a2 + b2*b2)
	meanC := (c1 + c2) / 2.0

	// ⚡ Bolt: Replace math.Pow(meanC, 7) with direct multiplication for performance
	meanC2 := meanC * meanC
	meanC4 := meanC2 * meanC2
	meanC7 := meanC4 * meanC2 * meanC
	const e7 = 6103515625.0 // 25^7
	g := 0.5 * (1.0 - math.Sqrt(meanC7/(meanC7+e7)))

	aPrime1 := (1.0 + g) * a1
	aPrime2 := (1.0 + g) * a2

	cPrime1 := math.Sqrt(aPrime1*aPrime1 + b1*b1)
	cPrime2 := math.Sqrt(aPrime2*aPrime2 + b2*b2)

	radToDeg := func(r float64) float64 {
		d := r * 180.0 / math.Pi
		if d < 0 {
			d += 360.0
		}
		return d
	}

	var hPrime1, hPrime2 float64
	if aPrime1 != 0 || b1 != 0 {
		hPrime1 = radToDeg(math.Atan2(b1, aPrime1))
	}
	if aPrime2 != 0 || b2 != 0 {
		hPrime2 = radToDeg(math.Atan2(b2, aPrime2))
	}

	deltaLPrime := l2 - l1
	deltaCPrime := cPrime2 - cPrime1
	deltaHPrime := calculateDeltaHPrime(cPrime1, cPrime2, hPrime1, hPrime2)

	meanL := (l1 + l2) / 2.0
	meanCPrime := (cPrime1 + cPrime2) / 2.0
	meanHPrime := calculateMeanHPrime(cPrime1, cPrime2, hPrime1, hPrime2)

	t := 1.0 - 0.17*math.Cos((meanHPrime-30.0)*math.Pi/180.0) +
		0.24*math.Cos(2.0*meanHPrime*math.Pi/180.0) +
		0.32*math.Cos((3.0*meanHPrime+6.0)*math.Pi/180.0) -
		0.20*math.Cos((4.0*meanHPrime-63.0)*math.Pi/180.0)

	dThetaTmp := (meanHPrime - 275.0) / 25.0
	deltaTheta := 30.0 * math.Exp(-(dThetaTmp * dThetaTmp))

	// ⚡ Bolt: Replace math.Pow(meanCPrime, 7) with direct multiplication for performance
	meanCPrime2 := meanCPrime * meanCPrime
	meanCPrime4 := meanCPrime2 * meanCPrime2
	meanCPrime7 := meanCPrime4 * meanCPrime2 * meanCPrime
	rc := 2.0 * math.Sqrt(meanCPrime7/(meanCPrime7+e7))

	rt := -rc * math.Sin(2.0*deltaTheta*math.Pi/180.0)

	dL := meanL - 50.0
	dL2 := dL * dL
	sl := 1.0 + (0.015*dL2)/math.Sqrt(20.0+dL2)
	sc := 1.0 + 0.045*meanCPrime
	sh := 1.0 + 0.015*meanCPrime*t

	dLPrime_sl := deltaLPrime / sl
	dCPrime_sc := deltaCPrime / sc
	dHPrime_sh := deltaHPrime / sh
	valSq := (dLPrime_sl * dLPrime_sl) +
		(dCPrime_sc * dCPrime_sc) +
		(dHPrime_sh * dHPrime_sh) +
		rt*(deltaCPrime/sc)*(deltaHPrime/sh)

	if valSq < 0 {
		return 0
	}
	return math.Sqrt(valSq)
}

// rgbToLab performs a fast, simplified conversion from RGB to CIE Lab space.
//
//nolint:unparam // result 0 (L) is returned for completeness of the CIE Lab conversion, even if not currently used.
func rgbToLab(r, g, b uint8) (float64, float64, float64) {
	lutRgbToLabOnce.Do(func() {
		for i := 0; i < 256; i++ {
			lutRgbToLab[i] = math.Pow(float64(i)/255.0, 2.2)
		}
	})

	// 1. Linearize RGB
	rf := lutRgbToLab[r]
	gf := lutRgbToLab[g]
	bf := lutRgbToLab[b]

	// 2. RGB to XYZ
	x := rf*0.4124 + gf*0.3576 + bf*0.1805
	y := rf*0.2126 + gf*0.7152 + bf*0.0722
	z := rf*0.0193 + gf*0.1192 + bf*0.9505

	// 3. XYZ to Lab (Simplified D65)
	// ⚡ Bolt: Inline lambda function `f` and precompute constant 16.0/116.0 (0.13793103448275862)
	// to eliminate function call overhead and division inside the tight processing loop.
	fy := y
	if fy > 0.008856 {
		fy = math.Cbrt(fy)
	} else {
		fy = 7.787*fy + 0.13793103448275862
	}

	fx := x
	if fx > 0.008856 {
		fx = math.Cbrt(fx)
	} else {
		fx = 7.787*fx + 0.13793103448275862
	}

	fz := z
	if fz > 0.008856 {
		fz = math.Cbrt(fz)
	} else {
		fz = 7.787*fz + 0.13793103448275862
	}

	l := 116.0*fy - 16.0
	a := 500.0 * (fx - fy)
	bLab := 200.0 * (fy - fz)

	return l, a, bLab
}

// generateBMSMap implements Boolean Map Saliency's surroundedness principle.
// It finds regions that are topologically isolated from the image borders.
// v4.0 is fully parallelized across all threshold channels.
func generateBMSMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	bms := make([]float64, w*h)

	thresholds := []uint8{50, 100, 150, 200, 240}
	results := make([][]float64, len(thresholds))
	var wg sync.WaitGroup

	for i, t := range thresholds {
		wg.Add(1)
		go func(idx int, threshold uint8) {
			defer wg.Done()
			results[idx] = processBMSThreshold(src, threshold, w, h)
		}(i, t)
	}
	wg.Wait()

	// Aggregate parallel results
	for _, res := range results {
		for i := 0; i < w*h; i++ {
			bms[i] += res[i] / float64(len(thresholds))
		}
	}
	return bms
}

func processBMSThreshold(src *image.RGBA, t uint8, w, h int) []float64 {
	res := make([]float64, w*h)
	boolMap := make([]bool, w*h)
	for i := 0; i < w*h; i++ {
		idx := i * 4
		lum := 0.299*float64(src.Pix[idx]) + 0.587*float64(src.Pix[idx+1]) + 0.114*float64(src.Pix[idx+2])
		if uint8(lum) > t {
			boolMap[i] = true
		}
	}

	bg := make([]bool, w*h)
	queue := make([]int, 0, w*h)
	for x := 0; x < w; x++ {
		queue = checkAndPush(boolMap, bg, queue, x, 0, w, h)
		queue = checkAndPush(boolMap, bg, queue, x, h-1, w, h)
	}
	for y := 0; y < h; y++ {
		queue = checkAndPush(boolMap, bg, queue, 0, y, w, h)
		queue = checkAndPush(boolMap, bg, queue, w-1, y, w, h)
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		cx, cy := curr%w, curr/w
		queue = checkAndPush(boolMap, bg, queue, cx-1, cy, w, h)
		queue = checkAndPush(boolMap, bg, queue, cx+1, cy, w, h)
		queue = checkAndPush(boolMap, bg, queue, cx, cy-1, w, h)
		queue = checkAndPush(boolMap, bg, queue, cx, cy+1, w, h)
	}

	for i := 0; i < w*h; i++ {
		if boolMap[i] && !bg[i] {
			res[i] = 1.0
		}
	}
	return res
}

func checkAndPush(boolMap, bg []bool, queue []int, x, y, w, h int) []int {
	if x < 0 || x >= w || y < 0 || y >= h {
		return queue
	}
	idx := y*w + x
	if !boolMap[idx] && !bg[idx] {
		bg[idx] = true
		return append(queue, idx)
	}
	return queue
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
