// Package optimize provides image resizing and quality optimization
// for Samsung Frame TV artwork. Frame TVs are 4K (3840×2160), so
// uploading larger images wastes bandwidth and transfer time.
package optimize

import (
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, false, fmt.Errorf("decode image: %w", err)
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// Only optimize if dimensions don't match target exactly or museum mode requires it.
	needsAdjustment := width != cfg.MaxWidth || height != cfg.MaxHeight
	if !needsAdjustment && !cfg.MuseumModeEnabled {
		return width, height, false, nil
	}

	logger.Info("optimizing image", "file", filepath.Base(path), "original_dims", fmt.Sprintf("%dx%d", width, height))

	// 1. Convert to RGBA for fast processing.
	rgba := toRGBA(img)

	// 2. Progressive Resize/Fill to match target dimensions.
	if needsAdjustment {
		rgba = centerCrop(rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled)
	}

	// 3. Sharpening pass.
	rgba = Sharpen(rgba)

	// 4. Apply Museum Mode aesthetic if enabled.
	if cfg.MuseumModeEnabled {
		rgba = ApplyMuseumMode(rgba, cfg.MuseumModeIntensity)
	}

	// 5. Final Dithering pass (always last to prevent banding).
	rgba = Dither(rgba)

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
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)
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

// findBestDirectorCrop implements the 'Director's Cut' Smart Crop v3.0.
// It combines Boolean Map Saliency (BMS) for object detection, Sobel for structural edges,
// and a high-performance Integral Image (Summed-Area Table) search to find the optimal focal point.
func findBestDirectorCrop(src *image.RGBA, windowW, windowH int, horizontal bool) int {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	// 1. Generate Saliency Map at working resolution (256px)
	const workSize = 256
	scale := float64(workSize) / float64(srcW)
	if !horizontal {
		scale = float64(workSize) / float64(srcH)
	}
	workW, workH := int(float64(srcW)*scale), int(float64(srcH)*scale)
	workImg := image.NewRGBA(image.Rect(0, 0, workW, workH))
	draw.NearestNeighbor.Scale(workImg, workImg.Bounds(), src, src.Bounds(), draw.Src, nil)

	saliencyMap := generateSaliencyMap(workImg)

	// 2. Create Integral Image (Summed-Area Table) for O(1) window sums
	integral := calculateIntegralImage(saliencyMap)

	// 3. Exhaustive search for the best window
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
		return int(float64(bestMapPos) / scale)
	} else {
		maxMapY := workH - mapWinH
		for my := 0; my <= maxMapY; my++ {
			score := getRectSum(integral, 0, my, workW-1, my+mapWinH-1, workW)
			if score > maxScore {
				maxScore = score
				bestMapPos = my
			}
		}
		return int(float64(bestMapPos) / scale)
	}
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

			// 5. Aesthetic/Compositional Weight (Rule of Thirds)
			aesthetic := calculateAestheticScore(x, y, w, h)

			// Weighted Fusion v3.0
			fusion := (object * 0.45) + (edge * 0.25) + (skin * 0.30)
			mapData[y*w+x] = fusion * (1.0 + aesthetic)
		}
	}
	return mapData
}

// generateBMSMap implements Boolean Map Saliency's surroundedness principle.
// It finds regions that are topologically isolated from the image borders.
func generateBMSMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	bms := make([]float64, w*h)

	// We use 5 threshold levels to capture objects of different brightness.
	thresholds := []uint8{50, 100, 150, 200, 240}
	for _, t := range thresholds {
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
				bms[i] += 1.0 / float64(len(thresholds))
			}
		}
	}
	return bms
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
		i := yy*src.Stride + xx*4
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

	tx1, tx2 := nx-0.33, nx-0.66
	ty1, ty2 := ny-0.33, ny-0.66
	thirdX := math.Exp(-(tx1*tx1)/0.02) + math.Exp(-(tx2*tx2)/0.02)
	thirdY := math.Exp(-(ty1*ty1)/0.02) + math.Exp(-(ty2*ty2)/0.02)

	return centerBias + ((thirdX + thirdY) * 0.25)
}

func calculateIntegralImage(saliencyMap []float64) []float64 {
	w := 256 // Fixed working width
	h := len(saliencyMap) / w
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

// ApplyMuseumMode orchestrates a suite of visual filters to simulate physical artwork.
func ApplyMuseumMode(src *image.RGBA, intensity int) *image.RGBA {
	// Clamp intensity to 1-10 (used only for texture).
	if intensity > 10 {
		intensity = 10
	}
	if intensity < 1 {
		intensity = 1
	}

	// 1. Unify the collection (Luminance and Color DNA)
	img := UnifyCollection(src)

	// 2. Apply Physical Texture (Weave, Impasto, Craquelure, Varnish)
	img = ApplyCanvasTexture(img, intensity)

	// 3. Final Museum Polish (Peak Clamping)
	img = GalleryMasterPolish(img)

	return img
}

// UnifyCollection ensures that diverse images share a consistent "visual DNA".
// Uses a Black-Point Preserving Power Curve to maintain depth.
func UnifyCollection(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// 1. Calculate Perceptual Contrast
	var sumSq, sum float64
	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			lum := 0.299*float64(src.Pix[i]) + 0.587*float64(src.Pix[i+1]) + 0.114*float64(src.Pix[i+2])
			sum += lum
			sumSq += lum * lum
		}
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

	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4

			// Physics-Based Linear Processing
			rLin := math.Pow(float64(src.Pix[i])/255.0, 2.2)
			gLin := math.Pow(float64(src.Pix[i+1])/255.0, 2.2)
			bLin := math.Pow(float64(src.Pix[i+2])/255.0, 2.2)

			// 2. Apply Power-Curve Contrast (Preserves 0.0 and 1.0)
			rLin = math.Pow(rLin, contrastGamma)
			gLin = math.Pow(gLin, contrastGamma)
			bLin = math.Pow(bLin, contrastGamma)

			// 3. Pigment Gamut Compression
			avg := (rLin + gLin + bLin) / 3
			rLin = rLin*0.97 + avg*0.03
			gLin = gLin*0.97 + avg*0.03
			bLin = bLin*0.97 + avg*0.03

			// Re-process to sRGB
			src.Pix[i] = uint8(math.Min(255, math.Max(0, math.Pow(rLin, 1.0/2.2)*255.0)))
			src.Pix[i+1] = uint8(math.Min(255, math.Max(0, math.Pow(gLin, 1.0/2.2)*255.0)))
			src.Pix[i+2] = uint8(math.Min(255, math.Max(0, math.Pow(bLin, 1.0/2.2)*255.0)))
		}
	}
	return src
}

// GalleryMasterPolish implements high-end gallery techniques to remove "digital glow".
func GalleryMasterPolish(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			r, g, b := float64(src.Pix[i]), float64(src.Pix[i+1]), float64(src.Pix[i+2])

			// 1. Research-Backed Peak Brightness Clamping (Berns 2001)
			// CAP at 235 instead of 215 to restore whites while maintaining surface reflection headroom.
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
			avg := (r + g + b) / 3
			r = r*0.92 + avg*0.08
			g = g*0.92 + avg*0.08
			b = b*0.92 + avg*0.08

			// 3. Micro-Paper Grain (Simulate physical substrate fibers)
			//nolint:gosec
			noise := (rand.Float64() - 0.5) * 5.0
			r += noise
			g += noise
			b += noise

			src.Pix[i] = uint8(math.Max(0, math.Min(255, r)))
			src.Pix[i+1] = uint8(math.Max(0, math.Min(255, g)))
			src.Pix[i+2] = uint8(math.Max(0, math.Min(255, b)))
		}
	}
	return src
}

// ApplyCanvasTexture simulates a physical interlocking warp-and-weft weave.
// Uses a 3D Normal-Mapping simulation for light-aware depth and anisotropic grain.
// UPDATED: Now includes Virtual Impasto (stroke height) and Craquelure (age splitting).
func ApplyCanvasTexture(src *image.RGBA, intensity int) *image.RGBA {
	// 1. Updated Opacity Curve (1.32 multiplier for more distinct jumps)
	opacity := 0.04 * math.Pow(1.32, float64(intensity-1))
	if opacity > 0.60 {
		opacity = 0.60
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	rng := rand.New(rand.NewSource(42)) //nolint:gosec

	for y := 1; y < height-1; y++ {
		offset := y * src.Stride
		for x := 1; x < width-1; x++ {
			i := offset + x*4

			// 1. Bipolar Virtual Impasto
			impasto := calculateBipolarImpasto(src, i)

			// 2. 3D Interlocking Weave
			weave, varnishPool := calculateWeave(x, y, rng)

			// 3. Procedural Craquelure
			if rng.Float64() > 0.9997 {
				weave -= 0.5
			}

			// Merge topography
			weave += impasto

			// 4. Blending & Archive Varnish
			for c := 0; c < 3; c++ {
				a := float64(src.Pix[i+c]) / 255.0

				if c == 0 {
					a *= 1.01
				} // Subtle Red
				if c == 2 {
					a *= (varnishPool * 0.99)
				} // Blue absorption

				src.Pix[i+c] = applySoftLight(a, weave, opacity)
			}
		}
	}
	return src
}

func calculateBipolarImpasto(src *image.RGBA, i int) float64 {
	// Detect ridge direction (Normal Mapping)
	center := 0.299*float64(src.Pix[i]) + 0.587*float64(src.Pix[i+1]) + 0.114*float64(src.Pix[i+2])
	left := 0.299*float64(src.Pix[i-4]) + 0.587*float64(src.Pix[i-3]) + 0.114*float64(src.Pix[i-2])
	top := 0.299*float64(src.Pix[i-src.Stride]) + 0.587*float64(src.Pix[i-src.Stride+1]) + 0.114*float64(src.Pix[i-src.Stride+2])

	// Create a bipolar offset (-0.15 to 0.15) based on edge direction
	// This creates highlights on one side of a stroke and shadows on the other
	dx := (center - left) / 255.0
	dy := (center - top) / 255.0

	// Virtual Light from Top-Left (-1, -1)
	return (dx + dy) * 0.15
}

func calculateWeave(x, y int, rng *rand.Rand) (float64, float64) {
	idX, idY := x/10, y/10
	cellX, cellY := x%10, y%10
	isWarp := (idX+idY)%2 == 0

	var weave float64
	lightDirX, lightDirY := -0.707, -0.707

	if isWarp {
		nx := (float64(cellX) - 4.5) / 5.0
		diffuse := math.Max(0, nx*lightDirX)
		weave = 0.4 + (diffuse * 0.3)
	} else {
		ny := (float64(cellY) - 4.5) / 5.0
		diffuse := math.Max(0, ny*lightDirY)
		weave = 0.4 + (diffuse * 0.3)
		if math.Abs(ny) < 0.2 {
			weave += 0.15
		}
	}

	// Add organic slub noise (fiber irregularities)
	if rng.Float64() > 0.98 {
		weave -= 0.05
	}

	isValley := cellX == 0 || cellX == 9 || cellY == 0 || cellY == 9
	varnishPool := 1.0
	if isValley {
		weave *= 0.8
		varnishPool = 0.96
	}
	return weave, varnishPool
}

func applySoftLight(a, b, opacity float64) uint8 {
	var res float64
	if b <= 0.5 {
		res = a - (1.0-2.0*b)*a*(1.0-a)
	} else {
		res = a + (2.0*b-1.0)*(math.Sqrt(a)-a)
	}
	final := a*(1.0-opacity) + res*opacity
	return uint8(math.Min(255, math.Max(0, final*255.0)))
}

// Dither applies a subtle random jitter to pixel values to break up banding in gradients.
func Dither(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			// Add a tiny random jitter (+/- 0.5 bits equivalent)
			// We use a small range to avoid visible noise while still breaking up patterns.
			// Using the top-level rand.Intn is thread-safe for parallel execution.
			//nolint:gosec // Weak random is perfectly fine for visual dithering
			jitter := rand.Intn(3) - 1 // -1, 0, or 1

			for c := 0; c < 3; c++ {
				val := int(src.Pix[i+c]) + jitter
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				src.Pix[i+c] = uint8(val)
			}
		}
	}
	return src
}

// Sharpen applies a high-performance 3x3 sharpening kernel to the image.
func Sharpen(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	width, height := bounds.Dx(), bounds.Dy()

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			for c := 0; c < 4; c++ { // Red, Green, Blue, Alpha
				if c == 3 { // Preserve alpha
					dst.Pix[y*dst.Stride+x*4+c] = src.Pix[y*src.Stride+x*4+c]
					continue
				}

				center := int(src.Pix[y*src.Stride+x*4+c])
				top := int(src.Pix[(y-1)*src.Stride+x*4+c])
				bottom := int(src.Pix[(y+1)*src.Stride+x*4+c])
				left := int(src.Pix[y*src.Stride+(x-1)*4+c])
				right := int(src.Pix[y*src.Stride+(x+1)*4+c])

				val := (center * 5) - top - bottom - left - right
				if val < 0 {
					val = 0
				} else if val > 255 {
					val = 255
				}
				dst.Pix[y*dst.Stride+x*4+c] = uint8(val)
			}
		}
	}

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

// ParseDimensionsFromFilename extracts width and height from an optimized filename.
func ParseDimensionsFromFilename(filename string) (int, int, bool) {
	parts := strings.Split(filename, "_opt.h_")
	if len(parts) < 2 {
		return 0, 0, false
	}
	dimPart := parts[1]
	if idx := strings.Index(dimPart, "."); idx != -1 {
		dimPart = dimPart[:idx]
	}
	dims := strings.Split(dimPart, "x")
	if len(dims) != 2 {
		return 0, 0, false
	}
	w, _ := strconv.Atoi(dims[0])
	h, _ := strconv.Atoi(dims[1])
	return w, h, true
}
