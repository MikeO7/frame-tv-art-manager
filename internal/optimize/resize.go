// Package optimize provides image resizing and quality optimization
// for Samsung Frame TV artwork. Frame TVs are 4K (3840×2160), so
// uploading larger images wastes bandwidth and transfer time.
package optimize

import (
	"fmt"
	"image"
	std_draw "image/draw"
	"image/jpeg"
	_ "image/png" // Needed for decoding PNG images
	"log/slog"
	"math"

	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/draw"
)

type Config struct {
	Enabled             bool
	SmartCropEnabled    bool
	MaxWidth            int
	MaxHeight           int
	OptimizeJPEGQuality int
	NormalizeLuminance  bool
	MuseumModeEnabled   bool
	MuseumModeIntensity int
}

var (
	lutSrgb         [16384]uint8
	lutSrgbOnce     sync.Once
	lutRgbToLab     [256]float64
	lutRgbToLabOnce sync.Once
	lutWeave        [400]float64
	lutVarnishPool  [400]float64
	lutWeaveOnce    sync.Once
)

// DefaultConfig returns sensible defaults for Frame TV display.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		SmartCropEnabled:    false,
		MaxWidth:            3840,
		MaxHeight:           2160,
		OptimizeJPEGQuality: 95,
		NormalizeLuminance:  true,
		MuseumModeEnabled:   false,
		MuseumModeIntensity: 1,
	}
}

// OptimizeFile checks if an image needs resizing and optimizes it
// in-place. Returns the new width, height, and whether the file was modified.
//
//nolint:revive // the package structure makes the OptimizeFile name acceptable and backward compatible
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (int, int, bool, error) {
	if !cfg.Enabled {
		return 0, 0, false, nil
	}

	// Only optimize JPEGs (Frame TV primary format).
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" {
		return 0, 0, false, nil
	}

	//nolint:gosec // Path is internally controlled
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false, fmt.Errorf("open image: %w", err)
	}
	defer func() { _ = f.Close() }()

	// 1. Fast path: check dimensions without full decode.
	imgCfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, false, fmt.Errorf("decode image config: %w", err)
	}

	width, height := imgCfg.Width, imgCfg.Height

	// Only optimize if dimensions don't match target exactly or museum mode requires it.
	needsAdjustment := width != cfg.MaxWidth || height != cfg.MaxHeight
	if !needsAdjustment && !cfg.MuseumModeEnabled {
		return width, height, false, nil
	}

	// 2. Full decode required.
	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, false, fmt.Errorf("seek to start: %w", err)
	}

	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, false, fmt.Errorf("decode image: %w", err)
	}

	logger.Info("optimizing image", "file", filepath.Base(path), "original_dims", fmt.Sprintf("%dx%d", width, height))

	// 1. Convert to RGBA for fast processing.
	rgba := toRGBA(img)

	// 2. Progressive Resize/Fill to match target dimensions.
	if needsAdjustment {
		rgba = centerCrop(rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled)
	}

	// 3. Sharpening pass.
	rgba = sharpen(rgba)

	// 4. Apply Museum Mode aesthetic if enabled.
	if cfg.MuseumModeEnabled {
		rgba = applyMuseumMode(rgba, cfg.MuseumModeIntensity)
	}

	// 5. Final Dithering pass (always last to prevent banding).
	rgba = dither(rgba)

	// 6. Save back to disk.
	//nolint:gosec // Path is internally controlled
	out, err := os.Create(path)
	if err != nil {
		return 0, 0, false, fmt.Errorf("create optimized file: %w", err)
	}
	defer func() { _ = out.Close() }()

	err = jpeg.Encode(out, rgba, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	if err != nil {
		return 0, 0, false, fmt.Errorf("encode jpeg: %w", err)
	}

	newBounds := rgba.Bounds()
	return newBounds.Dx(), newBounds.Dy(), true, nil
}

// toRGBA converts any image type to a standard RGBA image for processing.
// This also serves as a color normalization step, flattening different
// color profiles into a consistent sRGB-like space for the TV.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	std_draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, std_draw.Src)
	return rgba
}

// centerCrop performs a content-aware crop and high-fidelity scale to target dimensions.
// It uses the Director's Cut Saliency Engine to identify subjects and optimize composition.
func centerCrop(src *image.RGBA, targetW, targetH int, smart bool) *image.RGBA {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	targetAspect := float64(targetW) / float64(targetH)
	srcAspect := float64(srcW) / float64(srcH)

	var cropRect image.Rectangle
	if srcAspect > targetAspect {
		// Image is wider than target.
		cropW := int(float64(srcH) * targetAspect)
		bestX := (srcW - cropW) / 2 // Default to center
		if smart {
			bestX = findBestDirectorCrop(src, cropW, srcH, true)
		}
		cropRect = image.Rect(bestX, 0, bestX+cropW, srcH)
	} else {
		// Image is taller than target.
		cropH := int(float64(srcW) / targetAspect)
		bestY := (srcH - cropH) / 2 // Default to center
		if smart {
			bestY = findBestDirectorCrop(src, srcW, cropH, false)
		}
		cropRect = image.Rect(0, bestY, srcW, bestY+cropH)
	}

	// Single-pass high-fidelity scaling using Catmull-Rom (Bicubic).
	final := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.CatmullRom.Scale(final, final.Bounds(), src, cropRect, draw.Src, nil)
	return final
}

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

		// Clamp to bounds
		if horizontal {
			if offset < 0 {
				offset = 0
			}
			if offset+windowW > srcW {
				offset = srcW - windowW
			}
		} else {
			if offset < 0 {
				offset = 0
			}
			if offset+windowH > srcH {
				offset = srcH - windowH
			}
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

	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*src.Stride + x*4
			r, g, b := src.Pix[idx], src.Pix[idx+1], src.Pix[idx+2]

			// 2. Structural Saliency (Edge Detection via 3x3 Sobel)
			edge := calculateSobelEdge(src, x, y)

			// 3. Skin Tone Saliency (Heuristic)
			skin := calculateSkinProbability(r, g, b)

			// 4. Object Saliency (BMS)
			object := bmsMap[y*w+x]

			// 5. Perceptual Lab Saliency (Color Contrast in Lab Space)
			// Lab space is uniform and captures true human color perception.
			_, aLab, bLab := rgbToLab(r, g, b)
			colorWeight := math.Sqrt(aLab*aLab+bLab*bLab) / 128.0

			// 6. Aesthetic/Compositional Weight (Rule of Thirds + Balance)
			aesthetic := calculateAestheticScore(x, y, w, h)

			// Weighted Fusion v4.0
			// BMS remains the core, but we now incorporate perceptual Lab color weight.
			fusion := (object * 0.40) + (edge * 0.20) + (skin * 0.25) + (colorWeight * 0.15)
			mapData[y*w+x] = fusion * (1.0 + aesthetic)
		}
	}
	return mapData
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
	// We penalize saliency if it's too far to one side unless balanced by something else.
	// This is a subtle bias toward overall frame harmony.
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

// applyMuseumMode orchestrates a suite of visual filters to simulate physical artwork.
func applyMuseumMode(src *image.RGBA, intensity int) *image.RGBA {
	// Clamp intensity to 0-10 (used only for texture).
	if intensity > 10 {
		intensity = 10
	}
	if intensity < 0 {
		intensity = 0
	}

	// 1. Unify the collection (Luminance and Color DNA)
	img := unifyCollection(src)

	// 2. Apply Physical Texture (Weave, Impasto, Craquelure, Varnish)
	// If intensity is 0, skip the physical texture to only keep color unification.
	if intensity > 0 {
		img = applyCanvasTexture(img, intensity)
	}

	// 3. Final Museum Polish (Peak Clamping)
	img = galleryMasterPolish(img)

	return img
}

// unifyCollection ensures that diverse images share a consistent "visual DNA".
// Uses a Black-Point Preserving Power Curve to maintain depth.
//
//nolint:gocyclo // Highly optimized, performance-critical loops are manually unrolled
func unifyCollection(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// 1. Calculate Perceptual Contrast
	var sumSq, sum float64

	var wgLum sync.WaitGroup
	workersLum := 8
	chunkLum := (height + workersLum - 1) / workersLum
	sums := make([]float64, workersLum)
	sumSqs := make([]float64, workersLum)

	for i := 0; i < workersLum; i++ {
		startY := i * chunkLum
		endY := startY + chunkLum
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wgLum.Add(1)
		go func(workerIdx, sy, ey int) {
			defer wgLum.Done()
			var localSum, localSumSq float64
			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					lum := 0.299*float64(src.Pix[i]) + 0.587*float64(src.Pix[i+1]) + 0.114*float64(src.Pix[i+2])
					localSum += lum
					localSumSq += lum * lum
				}
			}
			sums[workerIdx] = localSum
			sumSqs[workerIdx] = localSumSq
		}(i, startY, endY)
	}
	wgLum.Wait()

	for i := 0; i < workersLum; i++ {
		sum += sums[i]
		sumSq += sumSqs[i]
	}
	mean := sum / float64(width*height)
	rms := math.Sqrt(sumSq/float64(width*height) - mean*mean)

	// Target Gallery RMS (Rich Contrast)
	const targetRMS = 58.0
	// Calculate a Gamma-based contrast shift instead of linear
	contrastGamma := 1.0 + (rms-targetRMS)/100.0
	// Clamp gamma to a safe range
	if contrastGamma < 0.85 {
		contrastGamma = 0.85
	}
	if contrastGamma > 1.15 {
		contrastGamma = 1.15
	}

	var lutLin [256]float64
	for i := 0; i < 256; i++ {
		lutLin[i] = math.Pow(float64(i)/255.0, 2.2*contrastGamma)
	}

	// Precompute the combination of pigment gamut compression + sRGB mapping
	// mapping from rLin/gLin/bLin directly to final sRGB to avoid math per pixel
	// Since pigment compression mixes RGB channels, we can't fully precompute without a 3D LUT,
	// but we can optimize the math.

	lutSrgbOnce.Do(func() {
		for i := 0; i < 16384; i++ {
			val := math.Pow(float64(i)/16383.0, 0.454545454545) * 255.0
			switch {
			case val < 0:
				lutSrgb[i] = 0
			case val > 255:
				lutSrgb[i] = 255
			default:
				lutSrgb[i] = uint8(val)
			}
		}
	})

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := j * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()
			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4

					// Physics-Based Linear Processing
					rLin := lutLin[src.Pix[i]]
					gLin := lutLin[src.Pix[i+1]]
					bLin := lutLin[src.Pix[i+2]]

					// 3. Pigment Gamut Compression
					avg := (rLin + gLin + bLin) * 0.333333333
					rLin = rLin*0.97 + avg*0.03
					gLin = gLin*0.97 + avg*0.03
					bLin = bLin*0.97 + avg*0.03

					// Re-process to sRGB
					fR := rLin * 16383.0
					idxR := 0
					if fR >= 16383.0 {
						idxR = 16383
					} else if fR > 0 {
						idxR = int(fR)
					}

					fG := gLin * 16383.0
					idxG := 0
					if fG >= 16383.0 {
						idxG = 16383
					} else if fG > 0 {
						idxG = int(fG)
					}

					fB := bLin * 16383.0
					idxB := 0
					if fB >= 16383.0 {
						idxB = 16383
					} else if fB > 0 {
						idxB = int(fB)
					}

					src.Pix[i] = lutSrgb[idxR]
					src.Pix[i+1] = lutSrgb[idxG]
					src.Pix[i+2] = lutSrgb[idxB]
				}
			}
		}(startY, endY)
	}
	wg.Wait()

	return src
}

// galleryMasterPolish implements high-end gallery techniques to remove "digital glow".
//
//nolint:gocyclo // Highly optimized, performance-critical loops are manually unrolled
func galleryMasterPolish(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := j * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()

			// Fast thread-local PRNG (Xorshift32)
			state := uint32(sy + 1) //nolint:gosec // Seed based on row

			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4

					r := float32(src.Pix[i])
					g := float32(src.Pix[i+1])
					b := float32(src.Pix[i+2])

					// 1. Research-Backed Peak Brightness Clamping (Berns 2001)
					const maxBright = 235.0
					if r > maxBright {
						r = maxBright
					}
					if g > maxBright {
						g = maxBright
					}
					if b > maxBright {
						b = maxBright
					}

					// 2. Pigment Saturation Limiter (Earth tones)
					avg := (r + g + b) * 0.33333333
					r = r*0.92 + avg*0.08
					g = g*0.92 + avg*0.08
					b = b*0.92 + avg*0.08

					// 3. Micro-Paper Grain (Simulate physical substrate fibers)
					// xorshift32
					state ^= state << 13
					state ^= state >> 17
					state ^= state << 5

					// Convert state to float noise between -2.5 and 2.5
					noise := (float32(state)/float32(0xFFFFFFFF) - 0.5) * 5.0
					r += noise
					g += noise
					b += noise
					// Faster clamp and cast without math.Max/Min
					switch {
					case r < 0:
						src.Pix[i] = 0
					case r > 255:
						src.Pix[i] = 255
					default:
						src.Pix[i] = uint8(r)
					}

					switch {
					case g < 0:
						src.Pix[i+1] = 0
					case g > 255:
						src.Pix[i+1] = 255
					default:
						src.Pix[i+1] = uint8(g)
					}

					switch {
					case b < 0:
						src.Pix[i+2] = 0
					case b > 255:
						src.Pix[i+2] = 255
					default:
						src.Pix[i+2] = uint8(b)
					}
				}
			}
		}(startY, endY)
	}
	wg.Wait()
	return src
}

// applyCanvasTexture simulates a physical interlocking warp-and-weft weave.
// Uses a 3D Normal-Mapping simulation for light-aware depth and anisotropic grain.
// UPDATED: Now includes Virtual Impasto (stroke height) and Craquelure (age splitting).
func applyCanvasTexture(src *image.RGBA, intensity int) *image.RGBA {
	// 1. Updated Opacity Curve (1.32 multiplier for more distinct jumps)
	opacity := 0.04 * math.Pow(1.32, float64(intensity-1))
	if opacity > 0.60 {
		opacity = 0.60
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// To prevent data races, calculateBipolarImpasto reads top and left pixels,
	// meaning concurrent writes to `src.Pix` by different rows will cause a race.
	// We will create a destination image and copy edge pixels.
	dst := image.NewRGBA(bounds)
	std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)

	var wg sync.WaitGroup
	workers := 8
	chunk := (height - 2 + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := 1 + j*chunk
		endY := startY + chunk
		if endY > height-1 {
			endY = height - 1
		}
		if startY >= height-1 {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()

			// Fast thread-local PRNG (Xorshift32)
			state := uint32(sy + 42) //nolint:gosec // Seed based on row

			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 1; x < width-1; x++ {
					processCanvasPixel(src, dst, offset+x*4, x, y, &state, opacity)
				}
			}
		}(startY, endY)
	}

	wg.Wait()
	return dst
}

func processCanvasPixel(src, dst *image.RGBA, i, x, y int, state *uint32, opacity float64) {
	// 1. Bipolar Virtual Impasto
	impasto := calculateBipolarImpasto(src, i)

	// 2. 3D Interlocking Weave
	weave, varnishPool := calculateWeave(x, y)

	// Add organic slub noise (fiber irregularities)
	*state ^= *state << 13
	*state ^= *state >> 17
	*state ^= *state << 5
	if float32(*state)/float32(0xFFFFFFFF) > 0.98 {
		weave -= 0.05
	}

	// 3. Procedural Craquelure
	*state ^= *state << 13
	*state ^= *state >> 17
	*state ^= *state << 5
	if float32(*state)/float32(0xFFFFFFFF) > 0.9997 {
		weave -= 0.5
	}

	// Merge topography
	weave += impasto

	// 4. Blending & Archive Varnish
	// To prevent race conditions with calculateBipolarImpasto reading adjacent pixels,
	// calculate new values into temporary variables before modifying the source pixel.
	aR := float64(src.Pix[i]) / 255.0 * 1.01 // Subtle Red
	r := applySoftLight(aR, weave, opacity)

	aG := float64(src.Pix[i+1]) / 255.0
	g := applySoftLight(aG, weave, opacity)

	aB := float64(src.Pix[i+2]) / 255.0 * (varnishPool * 0.99) // Blue absorption
	b := applySoftLight(aB, weave, opacity)

	dst.Pix[i] = r
	dst.Pix[i+1] = g
	dst.Pix[i+2] = b
}

func calculateBipolarImpasto(src *image.RGBA, i int) float64 {
	// Detect ridge direction (Normal Mapping)
	// Factor out constants and 255.0 division to avoid multiple float64 multiplications per channel
	// 0.299 * (0.15 / 255.0) = 0.00017588235294117646
	// 0.587 * (0.15 / 255.0) = 0.00034529411764705883
	// 0.114 * (0.15 / 255.0) = 0.00006705882352941177

	// Create a bipolar offset (-0.15 to 0.15) based on edge direction
	// This creates highlights on one side of a stroke and shadows on the other
	// Virtual Light from Top-Left (-1, -1)
	dR := float64(int(src.Pix[i])<<1 - int(src.Pix[i-4]) - int(src.Pix[i-src.Stride]))
	dG := float64(int(src.Pix[i+1])<<1 - int(src.Pix[i-3]) - int(src.Pix[i-src.Stride+1]))
	dB := float64(int(src.Pix[i+2])<<1 - int(src.Pix[i-2]) - int(src.Pix[i-src.Stride+2]))

	return dR*0.00017588235294117646 + dG*0.00034529411764705883 + dB*0.00006705882352941177
}

// calculateWeave computes the interlocking warp-and-weft canvas weave.
// ⚡ Bolt: Uses a precomputed 20x20 LUT, eliminating math in the tight per-pixel loop.
func calculateWeave(x, y int) (float64, float64) {
	lutWeaveOnce.Do(func() {
		for wy := 0; wy < 20; wy++ {
			for wx := 0; wx < 20; wx++ {
				idX, idY := wx/10, wy/10
				cellX, cellY := wx%10, wy%10
				isWarp := (idX+idY)%2 == 0

				var weave float64
				lightDirX, lightDirY := -0.707, -0.707

				if isWarp {
					nx := (float64(cellX) - 4.5) / 5.0
					diffuse := nx * lightDirX
					if diffuse < 0 {
						diffuse = 0
					}
					weave = 0.4 + (diffuse * 0.3)
				} else {
					ny := (float64(cellY) - 4.5) / 5.0
					diffuse := ny * lightDirY
					if diffuse < 0 {
						diffuse = 0
					}
					weave = 0.4 + (diffuse * 0.3)

					absNy := ny
					if absNy < 0 {
						absNy = -absNy
					}
					if absNy < 0.2 {
						weave += 0.15
					}
				}

				isValley := cellX == 0 || cellX == 9 || cellY == 0 || cellY == 9
				varnishPool := 1.0
				if isValley {
					weave *= 0.8
					varnishPool = 0.96
				}

				idx := wy*20 + wx
				lutWeave[idx] = weave
				lutVarnishPool[idx] = varnishPool
			}
		}
	})

	idx := (y%20)*20 + (x % 20)
	return lutWeave[idx], lutVarnishPool[idx]
}

func applySoftLight(a, b, opacity float64) uint8 {
	var res float64
	if b <= 0.5 {
		res = a - (1.0-2.0*b)*a*(1.0-a)
	} else {
		res = a + (2.0*b-1.0)*(math.Sqrt(a)-a)
	}
	final := a*(1.0-opacity) + res*opacity
	v := final * 255.0
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// dither applies a subtle random jitter to pixel values to break up banding in gradients.
//
//nolint:gocyclo // Highly optimized, performance-critical loops are manually unrolled
func dither(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers

	for i := 0; i < workers; i++ {
		startY := i * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()

			// Fast thread-local PRNG (Xorshift32)
			state := uint32(sy + 1) //nolint:gosec // Seed based on row

			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4

					// xorshift32
					state ^= state << 13
					state ^= state >> 17
					state ^= state << 5

					// Generate -1, 0, or 1
					jitter := int((state % 3)) - 1

					// R
					valR := int(src.Pix[i]) + jitter
					if valR < 0 {
						valR = 0
					} else if valR > 255 {
						valR = 255
					}
					//nolint:gosec // Int to uint8 bounded
					src.Pix[i] = uint8(valR)

					// G
					valG := int(src.Pix[i+1]) + jitter
					if valG < 0 {
						valG = 0
					} else if valG > 255 {
						valG = 255
					}
					//nolint:gosec // Int to uint8 bounded
					src.Pix[i+1] = uint8(valG)

					// B
					valB := int(src.Pix[i+2]) + jitter
					if valB < 0 {
						valB = 0
					} else if valB > 255 {
						valB = 255
					}
					//nolint:gosec // Int to uint8 bounded
					src.Pix[i+2] = uint8(valB)
				}
			}
		}(startY, endY)
	}
	wg.Wait()
	return src
}

// sharpen applies a high-performance 3x3 sharpening kernel to the image.
//
//nolint:gocyclo // Highly optimized, performance-critical loops are manually unrolled
func sharpen(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	width, height := bounds.Dx(), bounds.Dy()

	if width < 3 || height < 3 {
		std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)
		return dst
	}

	var wg sync.WaitGroup
	workers := 8 // Target 8 routines to map well to multi-core CPUs
	chunk := (height - 2) / workers
	if chunk == 0 {
		chunk = 1
	}

	for i := 0; i < workers; i++ {
		startY := 1 + i*chunk
		endY := startY + chunk
		if i == workers-1 {
			endY = height - 1
		}
		if startY >= height-1 {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()
			for y := sy; y < ey; y++ {
				srcOffset := y * src.Stride
				dstOffset := y * dst.Stride
				srcTopOffset := (y - 1) * src.Stride
				srcBottomOffset := (y + 1) * src.Stride

				for x := 1; x < width-1; x++ {
					iDst := dstOffset + x*4
					iSrc := srcOffset + x*4
					iTop := srcTopOffset + x*4
					iBottom := srcBottomOffset + x*4
					iLeft := iSrc - 4
					iRight := iSrc + 4

					// R
					valR := (int(src.Pix[iSrc]) * 5) - int(src.Pix[iTop]) - int(src.Pix[iBottom]) - int(src.Pix[iLeft]) - int(src.Pix[iRight])
					if valR < 0 {
						valR = 0
					} else if valR > 255 {
						valR = 255
					}
					//nolint:gosec // Int to uint8 bounded
					dst.Pix[iDst] = uint8(valR)

					// G
					valG := (int(src.Pix[iSrc+1]) * 5) - int(src.Pix[iTop+1]) - int(src.Pix[iBottom+1]) - int(src.Pix[iLeft+1]) - int(src.Pix[iRight+1])
					if valG < 0 {
						valG = 0
					} else if valG > 255 {
						valG = 255
					}
					//nolint:gosec // Int to uint8 bounded
					dst.Pix[iDst+1] = uint8(valG)

					// B
					valB := (int(src.Pix[iSrc+2]) * 5) - int(src.Pix[iTop+2]) - int(src.Pix[iBottom+2]) - int(src.Pix[iLeft+2]) - int(src.Pix[iRight+2])
					if valB < 0 {
						valB = 0
					} else if valB > 255 {
						valB = 255
					}
					//nolint:gosec // Int to uint8 bounded
					dst.Pix[iDst+2] = uint8(valB)

					// A
					dst.Pix[iDst+3] = src.Pix[iSrc+3]
				}
			}
		}(startY, endY)
	}
	wg.Wait()

	// Copy borders
	for x := 0; x < width; x++ {
		copy(dst.Pix[0*dst.Stride+x*4:0*dst.Stride+x*4+4], src.Pix[0*src.Stride+x*4:0*src.Stride+x*4+4])
		copy(dst.Pix[(height-1)*dst.Stride+x*4:(height-1)*dst.Stride+x*4+4], src.Pix[(height-1)*src.Stride+x*4:(height-1)*src.Stride+x*4+4])
	}
	for y := 0; y < height; y++ {
		copy(dst.Pix[y*dst.Stride+0*4:y*dst.Stride+0*4+4], src.Pix[y*src.Stride+0*4:y*src.Stride+0*4+4])
		copy(dst.Pix[y*dst.Stride+(width-1)*4:y*dst.Stride+(width-1)*4+4], src.Pix[y*src.Stride+(width-1)*4:y*src.Stride+(width-1)*4+4])
	}

	return dst
}

// ValidateImage performs a low-cost check to ensure an image file is not corrupt.
func ValidateImage(path string) error {
	//nolint:gosec // Path is internally controlled
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, _, err = image.DecodeConfig(f)
	return err
}

// fitDimensions calculates the best width/height to fit within max bounds while preserving aspect ratio.
func fitDimensions(w, h, maxW, maxH int) (int, int) {
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	// Always upscale to at least fill the 4K canvas (3840x2160)
	if scale < 1.0 {
		return int(float64(w) * scale), int(float64(h) * scale)
	}
	// For Frame TV, we actually want to fill the native 4K resolution
	scale = math.Max(float64(maxW)/float64(w), float64(maxH)/float64(h))
	return int(float64(w) * scale), int(float64(h) * scale)
}
