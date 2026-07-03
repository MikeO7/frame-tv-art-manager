package optimize

import (
	"image"
	"math"
	"sync"

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

// generateBMSMap implements Boolean Map Saliency's surroundedness principle.
// It finds regions that are topologically isolated from the image borders.
// v4.0 is fully parallelized across all threshold channels.
func generateBMSMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	bms := make([]float64, w*h)

	// Precompute luminance map once for all thresholds to avoid redundant
	// calculation overhead across concurrent goroutines
	pix := src.Pix
	lumMap := make([]uint8, w*h)
	for i := 0; i < w*h; i++ {
		idx := i * 4
		lum := (int(pix[idx])*299 + int(pix[idx+1])*587 + int(pix[idx+2])*114) / 1000
		lumMap[i] = uint8(lum) //nolint:gosec // weighted luminance of 0-255 channels stays within byte range
	}

	thresholds := []uint8{50, 100, 150, 200, 240}
	results := make([][]float64, len(thresholds))
	var wg sync.WaitGroup

	for i, t := range thresholds {
		wg.Add(1)
		go func(idx int, threshold uint8) {
			defer wg.Done()
			results[idx] = processBMSThreshold(lumMap, threshold, w, h)
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

type bmsState struct {
	queue   []int
	head    int
	tail    int
	boolMap []bool
	bg      []bool
	w       int
	h       int
}

func (s *bmsState) tryEnqueue(idx int) {
	if !s.boolMap[idx] && !s.bg[idx] {
		s.bg[idx] = true
		s.queue[s.tail] = idx
		s.tail++
	}
}

func (s *bmsState) seedBorders() {
	for x := 0; x < s.w; x++ {
		s.tryEnqueue(x)
		s.tryEnqueue((s.h-1)*s.w + x)
	}
	for y := 0; y < s.h; y++ {
		s.tryEnqueue(y * s.w)
		s.tryEnqueue(y*s.w + s.w - 1)
	}
}

func (s *bmsState) floodFill() {
	for s.head < s.tail {
		curr := s.queue[s.head]
		s.head++
		cx, cy := curr%s.w, curr/s.w

		if cx-1 >= 0 {
			s.tryEnqueue(cy*s.w + cx - 1)
		}
		if cx+1 < s.w {
			s.tryEnqueue(cy*s.w + cx + 1)
		}
		if cy-1 >= 0 {
			s.tryEnqueue((cy-1)*s.w + cx)
		}
		if cy+1 < s.h {
			s.tryEnqueue((cy+1)*s.w + cx)
		}
	}
}

func processBMSThreshold(lumMap []uint8, t uint8, w, h int) []float64 {
	if w <= 0 || h <= 0 {
		return nil
	}
	res := make([]float64, w*h)
	boolMap := make([]bool, w*h)
	for i := 0; i < w*h; i++ {
		if lumMap[i] > t {
			boolMap[i] = true
		}
	}

	state := bmsState{
		queue:   make([]int, w*h),
		boolMap: boolMap,
		bg:      make([]bool, w*h),
		w:       w,
		h:       h,
	}

	state.seedBorders()
	state.floodFill()

	for i := 0; i < w*h; i++ {
		if state.boolMap[i] && !state.bg[i] {
			res[i] = 1.0
		}
	}
	return res
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

//nolint:gochecknoglobals // global read-only lookup table for performance-critical RGB-to-Lab calculations
var (
	lutRgbToLab     [256]float64
	lutRgbToLabOnce sync.Once
)

type labColor struct {
	l float64
	a float64
	b float64
}

// ciede2000 calculates the exact CIE 2000 color-difference standard between two CIELAB colors.
//
//nolint:funlen,gocyclo // mathematical formula requiring monolithic execution flow for performance and readability
func ciede2000(color1, color2 labColor) float64 {
	const degToRad = math.Pi / 180.0
	const e7 = 6103515625.0 // 25^7

	l1, a1, b1 := color1.l, color1.a, color1.b
	l2, a2, b2 := color2.l, color2.a, color2.b
	c1c := math.Sqrt(a1*a1 + b1*b1)
	c2c := math.Sqrt(a2*a2 + b2*b2)
	meanC := (c1c + c2c) * 0.5

	// explicit multiplication is significantly faster than math.Pow(x, 7)
	meanC2 := meanC * meanC
	meanC4 := meanC2 * meanC2
	meanC7 := meanC4 * meanC2 * meanC
	g := 0.5 * (1.0 - math.Sqrt(meanC7/(meanC7+e7)))

	aPrime1 := (1.0 + g) * a1
	aPrime2 := (1.0 + g) * a2

	cPrime1 := math.Sqrt(aPrime1*aPrime1 + b1*b1)
	cPrime2 := math.Sqrt(aPrime2*aPrime2 + b2*b2)

	// Calculate hues in radians directly to avoid repeated deg/rad conversions
	var hPrime1, hPrime2 float64
	if aPrime1 != 0 || b1 != 0 {
		hPrime1 = math.Atan2(b1, aPrime1)
		if hPrime1 < 0 {
			hPrime1 += 2 * math.Pi
		}
	}
	if aPrime2 != 0 || b2 != 0 {
		hPrime2 = math.Atan2(b2, aPrime2)
		if hPrime2 < 0 {
			hPrime2 += 2 * math.Pi
		}
	}

	deltaLPrime := l2 - l1
	deltaCPrime := cPrime2 - cPrime1

	deltaHPrime := 0.0
	if cPrime1*cPrime2 != 0 {
		deltahPrime := hPrime2 - hPrime1
		if deltahPrime > math.Pi {
			deltahPrime -= 2 * math.Pi
		} else if deltahPrime < -math.Pi {
			deltahPrime += 2 * math.Pi
		}
		deltaHPrime = 2.0 * math.Sqrt(cPrime1*cPrime2) * math.Sin(deltahPrime*0.5)
	}

	meanL := (l1 + l2) * 0.5
	meanCPrime := (cPrime1 + cPrime2) * 0.5

	var meanHPrime float64
	switch {
	case cPrime1*cPrime2 == 0:
		meanHPrime = hPrime1 + hPrime2
	case math.Abs(hPrime1-hPrime2) <= math.Pi:
		meanHPrime = (hPrime1 + hPrime2) * 0.5
	case hPrime1+hPrime2 < 2*math.Pi:
		meanHPrime = (hPrime1 + hPrime2 + 2*math.Pi) * 0.5
	default:
		meanHPrime = (hPrime1 + hPrime2 - 2*math.Pi) * 0.5
	}

	hRad := meanHPrime
	t := 1.0 - 0.17*math.Cos(hRad-30.0*degToRad) +
		0.24*math.Cos(2.0*hRad) +
		0.32*math.Cos(3.0*hRad+6.0*degToRad) -
		0.20*math.Cos(4.0*hRad-63.0*degToRad)

	// The formula constants here are defined in degrees, so convert meanHPrime for the calc
	dtCalc := (meanHPrime/degToRad - 275.0) / 25.0
	deltaTheta := 30.0 * math.Exp(-(dtCalc * dtCalc)) // replaced math.Pow(..., 2) with dtCalc*dtCalc for speed

	// explicit multiplication is significantly faster than math.Pow(x, 7)
	meanCP2 := meanCPrime * meanCPrime
	meanCP4 := meanCP2 * meanCP2
	meanCPrime7 := meanCP4 * meanCP2 * meanCPrime
	rc := 2.0 * math.Sqrt(meanCPrime7/(meanCPrime7+e7))

	rt := -rc * math.Sin(2.0*deltaTheta*degToRad)

	// replaced math.Pow(..., 2) with explicit multiplication for speed
	mL50 := meanL - 50.0
	mL50Sq := mL50 * mL50
	sl := 1.0 + (0.015*mL50Sq)/math.Sqrt(20.0+mL50Sq)
	sc := 1.0 + 0.045*meanCPrime
	sh := 1.0 + 0.015*meanCPrime*t

	// replaced math.Pow(..., 2) with explicit multiplications for speed
	dLsl := deltaLPrime / sl
	dCsc := deltaCPrime / sc
	dHsh := deltaHPrime / sh
	valSq := (dLsl * dLsl) +
		(dCsc * dCsc) +
		(dHsh * dHsh) +
		rt*dCsc*dHsh

	if valSq < 0 {
		return 0
	}
	return math.Sqrt(valSq)
}

// rgbToLab performs a fast, simplified conversion from RGB to CIE Lab space.
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
