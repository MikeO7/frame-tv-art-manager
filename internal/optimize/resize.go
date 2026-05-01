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

	// 4. Luminance Normalization (consistent brightness).
	if cfg.NormalizeLuminance {
		rgba = NormalizeLuminance(rgba)
	}

	// 5. Dithering pass.
	rgba = Dither(rgba)

	// 5. Apply Museum Mode aesthetic if enabled.
	if cfg.MuseumModeEnabled {
		rgba = ApplyMuseumMode(rgba, cfg.MuseumModeIntensity)
	}

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

// centerCrop performs a content-aware crop (if enabled) and high-fidelity scale to target dimensions.
// It uses entropy analysis to find the area with the most detail and preserves it in the 16:9 frame.
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
			bestX = findBestCropWindow(src, cropW, srcH, true)
		}
		cropRect = image.Rect(bestX, 0, bestX+cropW, srcH)
	} else {
		// Image is taller than target.
		cropH := int(float64(srcW) / targetAspect)
		bestY := (srcH - cropH) / 2 // Default to center
		if smart {
			bestY = findBestCropWindow(src, srcW, cropH, false)
		}
		cropRect = image.Rect(0, bestY, srcW, bestY+cropH)
	}

	// Single-pass high-fidelity scaling using Catmull-Rom (Bicubic).
	final := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.CatmullRom.Scale(final, final.Bounds(), src, cropRect, draw.Src, nil)
	return final
}

// findBestCropWindow uses entropy (pixel variance) to find the most visually significant window.
func findBestCropWindow(src *image.RGBA, windowW, windowH int, horizontal bool) int {
	maxOffset := 0
	if horizontal {
		maxOffset = src.Bounds().Dx() - windowW
	} else {
		maxOffset = src.Bounds().Dy() - windowH
	}
	if maxOffset <= 0 {
		return 0
	}

	bestOffset := maxOffset / 2
	maxEntropy := -1.0

	// Check 10 samples across the possible range to find the area with the highest detail.
	for i := 0; i <= 10; i++ {
		offset := (maxOffset * i) / 10
		var rect image.Rectangle
		if horizontal {
			rect = image.Rect(offset, 0, offset+windowW, windowH)
		} else {
			rect = image.Rect(0, offset, windowW, offset+windowH)
		}

		entropy := calculateEntropy(src, rect)
		if entropy > maxEntropy {
			maxEntropy = entropy
			bestOffset = offset
		}
	}
	return bestOffset
}

// calculateEntropy measures local pixel variance to find areas of high detail/contrast.
func calculateEntropy(src *image.RGBA, rect image.Rectangle) float64 {
	var totalVariance float64
	// Sample a 15x15 grid for reliable entropy detection.
	for i := 0; i < 15; i++ {
		for j := 0; j < 15; j++ {
			x := rect.Min.X + (rect.Dx()*i)/15
			y := rect.Min.Y + (rect.Dy()*j)/15

			// Stay within bounds
			if x >= src.Bounds().Dx() {
				x = src.Bounds().Dx() - 1
			}
			if y >= src.Bounds().Dy() {
				y = src.Bounds().Dy() - 1
			}

			idx := y*src.Stride + x*4
			r, g, b := int(src.Pix[idx]), int(src.Pix[idx+1]), int(src.Pix[idx+2])

			// Contrast heuristic: measure local differences between channels
			totalVariance += math.Abs(float64(r-g)) + math.Abs(float64(g-b)) + math.Abs(float64(b-r))
		}
	}
	return totalVariance
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

	// 1. Normalize Luminance (unify brightness across the collection).
	img := NormalizeLuminance(src)

	// 2. Colors and Black Lift stay at a fixed, subtle baseline (intensity 1).
	img = LiftBlacks(img, 1)
	img = ApplyWarmth(img, 1)

	// 3. Gallery Lighting (subtle vignette for spotlight effect).
	img = ApplyGalleryLighting(img)

	// 4. Texture look is the only thing impacted by the user-defined intensity setting.
	img = ApplyCanvasTexture(img, intensity)

	// 5. Final polish: Unify the collection, add physical depth, and apply Gallery Master techniques.
	img = UnifyCollection(img)
	img = GalleryMasterPolish(img)
	img = ApplyMuseumSignature(img)
	img = Dither(img)

	return img
}

// UnifyCollection ensures that diverse images share a consistent "visual DNA".
// Uses RMS Contrast scaling and Pigment Gamut compression.
func UnifyCollection(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// 1. Calculate RMS Contrast (Visual Energy)
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

	// Target Gallery RMS (subtle but present contrast)
	const targetRMS = 45.0
	contrastFactor := targetRMS / (rms + 1.0)
	// Dampen the factor to avoid extreme distortion
	contrastFactor = 1.0 + (contrastFactor-1.0)*0.5

	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4

			// Physics-Based Linear Processing
			rLin := math.Pow(float64(src.Pix[i])/255.0, 2.2)
			gLin := math.Pow(float64(src.Pix[i+1])/255.0, 2.2)
			bLin := math.Pow(float64(src.Pix[i+2])/255.0, 2.2)

			// 2. Apply Collection-Wide Contrast Scaling
			rLin = ((rLin - 0.5) * contrastFactor) + 0.5
			gLin = ((gLin - 0.5) * contrastFactor) + 0.5
			bLin = ((bLin - 0.5) * contrastFactor) + 0.5

			// 3. Pigment Gamut Compression (Mineral Palette)
			// Move colors slightly toward the average to harmonize the palette
			avg := (rLin + gLin + bLin) / 3
			rLin = rLin*0.95 + avg*0.05
			gLin = gLin*0.95 + avg*0.05
			bLin = bLin*0.95 + avg*0.05

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
			const maxBright = 215.0
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

// ApplyMuseumSignature adds the final "Archive" touches: Bevel, Varnish, and Grain.
func ApplyMuseumSignature(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			r, g, b := float64(src.Pix[i]), float64(src.Pix[i+1]), float64(src.Pix[i+2])

			// 1. Aged Archive Varnish (Berns 1994)
			// Simulates blue-light absorption in organic resins over time
			r *= 1.02
			g *= 1.01
			b *= 0.96

			// 2. Matte Bevel Simulation (1px highlight/shadow at edges)
			// This simulates the physical cut in the cardboard matte
			if x == 0 || y == 0 {
				r *= 1.15 // Top/Left Highlight
				g *= 1.15
				b *= 1.15
			} else if x == width-1 || y == height-1 {
				r *= 0.85 // Bottom/Right Shadow
				g *= 0.85
				b *= 0.85
			}

			src.Pix[i] = uint8(math.Min(255, r))
			src.Pix[i+1] = uint8(math.Min(255, g))
			src.Pix[i+2] = uint8(math.Min(255, b))
		}
	}
	return src
}

// ApplyGalleryLighting adds a soft vignette to simulate gallery spotlighting.
func ApplyGalleryLighting(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	centerX, centerY := float64(width)/2, float64(height)/2
	maxDist := math.Sqrt(centerX*centerX + centerY*centerY)

	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			dx, dy := float64(x)-centerX, float64(y)-centerY
			dist := math.Sqrt(dx*dx + dy*dy)

			// Soft falloff starting from the center
			factor := 1.0 - (dist/maxDist)*0.15 // Max 15% darkening at corners

			for c := 0; c < 3; c++ {
				val := float64(src.Pix[i+c]) * factor
				src.Pix[i+c] = uint8(val)
			}
		}
	}
	return src
}

// LiftBlacks raises the black point to simulate ink-on-canvas rather than digital pixels.
func LiftBlacks(src *image.RGBA, intensity int) *image.RGBA {
	// Re-mapped: The old 6 is the new 10. Multiplier 5 (10*5=50).
	val := 5 * intensity
	if val > 100 {
		val = 100 // Cap to prevent washing out the image too much
	}
	lift := uint8(val)

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			for c := 0; c < 3; c++ {
				val := int(src.Pix[i+c])
				if val < int(lift) {
					// Soft ramp up to the lift point
					val = int(lift) + (val * (255 - int(lift)) / 255)
				} else {
					// Compress the remaining range
					val = int(lift) + (val-int(lift))*(255-int(lift))/(255-int(lift))
				}
				if val > 255 {
					val = 255
				}
				src.Pix[i+c] = uint8(val)
			}
		}
	}
	return src
}

// ApplyWarmth adds a subtle amber tint to simulate gallery spotlighting.
func ApplyWarmth(src *image.RGBA, intensity int) *image.RGBA {
	// Re-mapped: The old 6 is the new 10. Multiplier 1 (10*1=10).
	val := 1 * intensity
	if val > 50 {
		val = 50 // Cap to prevent turning the image deep orange
	}
	warmth := uint8(val)

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			// Red up
			r := int(src.Pix[i]) + int(warmth)
			if r > 255 {
				r = 255
			}
			src.Pix[i] = uint8(r)

			// Blue down
			b := int(src.Pix[i+2]) - int(warmth)
			if b < 0 {
				b = 0
			}
			src.Pix[i+2] = uint8(b)
		}
	}
	return src
}

// ApplyCanvasTexture simulates a physical interlocking warp-and-weft weave.
// Uses a 3D Normal-Mapping simulation for light-aware depth and anisotropic grain.
// UPDATED: Now includes Virtual Impasto (stroke height) and Craquelure (age splitting).
func ApplyCanvasTexture(src *image.RGBA, intensity int) *image.RGBA {
	opacity := 0.04 * math.Pow(1.28, float64(intensity-1))
	if opacity > 0.50 {
		opacity = 0.50
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	rng := rand.New(rand.NewSource(42)) //nolint:gosec

	for y := 1; y < height-1; y++ {
		offset := y * src.Stride
		for x := 1; x < width-1; x++ {
			i := offset + x*4

			// 1. Virtual Impasto (Luminance-Ridge Mapping)
			impasto := calculateImpasto(src, i)

			// 2. 3D Interlocking Weave (10px frequency)
			weave, varnishPool := calculateWeave(x, y, rng)

			// 3. Procedural Craquelure (Microscopic Age-Splitting)
			if rng.Float64() > 0.9995 {
				weave -= 0.4 // A deep, sharp crack
			}

			weave += impasto

			// 4. Blending & Archive Varnish
			for c := 0; c < 3; c++ {
				a := float64(src.Pix[i+c]) / 255.0

				// Apply thickness-dependent varnish shift
				if c == 0 {
					a *= 1.02
				} // Red
				if c == 1 {
					a *= 1.01
				} // Green
				if c == 2 {
					a *= varnishPool
				} // Blue (Pooled)

				src.Pix[i+c] = applySoftLight(a, weave, opacity)
			}
		}
	}
	return src
}

func calculateImpasto(src *image.RGBA, i int) float64 {
	center := 0.299*float64(src.Pix[i]) + 0.587*float64(src.Pix[i+1]) + 0.114*float64(src.Pix[i+2])
	right := 0.299*float64(src.Pix[i+4]) + 0.587*float64(src.Pix[i+5]) + 0.114*float64(src.Pix[i+6])
	bottom := 0.299*float64(src.Pix[i+src.Stride]) + 0.587*float64(src.Pix[i+src.Stride+1]) + 0.114*float64(src.Pix[i+src.Stride+2])

	ridge := math.Abs(center-right) + math.Abs(center-bottom)
	return (ridge / 255.0) * 0.15
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

// NormalizeLuminance balances the overall brightness of an image to a consistent gallery target.
func NormalizeLuminance(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return src
	}

	// 1. Calculate current average luminance.
	var totalLum float64
	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			r, g, b := float64(src.Pix[i]), float64(src.Pix[i+1]), float64(src.Pix[i+2])
			// Rec. 601 luminance formula.
			lum := 0.299*r + 0.587*g + 0.114*b
			totalLum += lum
		}
	}
	avgLum := totalLum / float64(width*height)

	// Target luminance 115 (subtle, rich look).
	const targetLum = 115.0

	// Guard against extreme images that would produce artifacts.
	if avgLum < 10 || avgLum > 240 {
		return src
	}

	scale := targetLum / avgLum
	// Clamp scale to avoid extreme shifts (max 1.8x up, 0.6x down).
	if scale > 1.8 {
		scale = 1.8
	}
	if scale < 0.6 {
		scale = 0.6
	}

	// 2. Apply scale to RGB channels.
	for y := 0; y < height; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			i := offset + x*4
			for c := 0; c < 3; c++ {
				val := float64(src.Pix[i+c]) * scale
				if val > 255 {
					val = 255
				}
				src.Pix[i+c] = uint8(val)
			}
		}
	}
	return src
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
