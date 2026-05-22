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
