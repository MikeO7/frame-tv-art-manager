// Package optimize provides image resizing and quality optimization
// for Samsung Frame TV artwork. Frame TVs are 4K (3840×2160), so
// uploading larger images wastes bandwidth and transfer time.
package optimize

import (
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/muesli/smartcrop"
	"github.com/muesli/smartcrop/nfnt"
	"golang.org/x/image/draw"
)

// Config holds image optimization settings.
type Config struct {
	Enabled             bool
	MaxWidth            int
	MaxHeight           int
	OptimizeJPEGQuality int
	SmartCropEnabled    bool
	SmartFillEnabled    bool
	SmartFillTolerance  float64
	ImageMatteMode      string
	AmbientDimming      float64
	AmbientVignette     float64
}

// DefaultConfig returns sensible defaults for Frame TV display.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		MaxWidth:            3840,
		MaxHeight:           2160,
		OptimizeJPEGQuality: 92,
		SmartCropEnabled:    false,
		SmartFillEnabled:    true,
		SmartFillTolerance:  0.12,
		ImageMatteMode:      "extended",
		AmbientDimming:      1.1, // "B3" style: 1.1 (bright). Set to 1.0 for "B2" style.
		AmbientVignette:     0.0, // "B3" style: 0.0 (no vignette). Set to 0.3 for "B2" style.
	}
}

// OptimizeFile checks if an image needs resizing and optimizes it
// in-place. Returns the new width, height, and whether the file was modified.
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (int, int, bool, error) {
	if !cfg.Enabled {
		return 0, 0, false, nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".png" {
		// Skip PNG — may need transparency for matte effects.
		return 0, 0, false, nil
	}

	// Decode the image.
	img, err := decodeImage(path)
	if err != nil {
		return 0, 0, false, err
	}

	bounds := img.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	// Check if resize or crop is needed.
	aspectRatio := float64(cfg.MaxWidth) / float64(cfg.MaxHeight)
	imgRatio := float64(origW) / float64(origH)

	// Tolerance for "Smart Fill": If the image is within 12% of the target 16:9 ratio,
	// we perform a slight center-crop to avoid tiny black slivers.
	const fillTolerance = 0.12
	ratioDiff := (imgRatio - aspectRatio) / aspectRatio
	if ratioDiff < 0 {
		ratioDiff = -ratioDiff
	}
	isCloseEnoughToFill := ratioDiff <= fillTolerance

	// We optimize if:
	// 1. Image is not exactly 4K (either too large or too small)
	// 2. Image aspect ratio doesn't match 4K target exactly
	isExact4K := origW == cfg.MaxWidth && origH == cfg.MaxHeight
	needsRatioFix := (fmt.Sprintf("%.3f", imgRatio) != fmt.Sprintf("%.3f", aspectRatio))

	if isExact4K && !needsRatioFix {
		logger.Debug("image already optimal size and aspect",
			"file", filepath.Base(path),
			"width", origW,
			"height", origH,
		)
		return origW, origH, false, nil
	}

	var dst image.Image
	// Prioritize cropping logic: SmartCrop (Explicit) > SmartFill (Tolerance) > Standard Resize (Blur)
	switch {
	case cfg.SmartCropEnabled:
		dst, err = smartCrop(img, cfg)
	case cfg.SmartFillEnabled && isCloseEnoughToFill:
		logger.Debug("performing subtle smart-fill crop to remove slivers", "file", filepath.Base(path))
		dst, err = smartCrop(img, cfg)
	default:
		dst = fitResize(img, cfg)
	}

	if err != nil {
		return 0, 0, false, err
	}

	ok, err := atomicWriteImage(path, dst, cfg.OptimizeJPEGQuality, logger)
	if err != nil {
		return 0, 0, false, err
	}
	return dst.Bounds().Dx(), dst.Bounds().Dy(), ok, nil
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // Path is verified
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	defer func() { _ = f.Close() }()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

func atomicWriteImage(path string, img image.Image, quality int, logger *slog.Logger) (bool, error) {
	// Get original size for logging.
	origStat, _ := os.Stat(path)

	// Write to temp file then rename.
	tmpPath := path + ".opt.tmp"
	out, err := os.Create(filepath.Clean(tmpPath)) //nolint:gosec // Safe temporary path
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}

	// Always encode as JPEG for the TV.
	err = jpeg.Encode(out, img, &jpeg.Options{Quality: quality})
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("encode jpeg: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename optimized file: %w", err)
	}

	// Ensure inclusive permissions for Mac access.
	_ = os.Chmod(path, 0644) //nolint:gosec // Requires inclusive permissions

	if origStat != nil {
		if newStat, err := os.Stat(path); err == nil {
			logger.Info("image optimized",
				"file", filepath.Base(path),
				"original_bytes", origStat.Size(),
				"optimized_bytes", newStat.Size(),
				"saved_pct", fmt.Sprintf("%.0f%%", (1-float64(newStat.Size())/float64(origStat.Size()))*100),
			)
		}
	}

	return true, nil
}

// ValidateImage checks if a file is a valid image that can be decoded.
func ValidateImage(path string) error {
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, _, err = image.Decode(f)
	return err
}

// fitDimensions calculates new width and height that fit within
// maxW×maxH while preserving the aspect ratio.
func smartCrop(img image.Image, cfg Config) (image.Image, error) {
	analyzer := smartcrop.NewAnalyzer(nfnt.NewDefaultResizer())
	topCrop, err := analyzer.FindBestCrop(img, cfg.MaxWidth, cfg.MaxHeight)
	if err != nil {
		return nil, fmt.Errorf("find best crop: %w", err)
	}

	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}

	var croppedImg image.Image
	if si, ok := img.(subImager); ok {
		croppedImg = si.SubImage(topCrop)
	} else {
		tempDst := image.NewRGBA(img.Bounds())
		draw.Draw(tempDst, tempDst.Bounds(), img, img.Bounds().Min, draw.Src)
		croppedImg = tempDst.SubImage(topCrop)
	}

	finalDst := image.NewRGBA(image.Rect(0, 0, cfg.MaxWidth, cfg.MaxHeight))
	draw.CatmullRom.Scale(finalDst, finalDst.Bounds(), croppedImg, croppedImg.Bounds(), draw.Over, nil)
	return finalDst, nil
}

func fitResize(img image.Image, cfg Config) image.Image {
	origW := img.Bounds().Dx()
	origH := img.Bounds().Dy()
	newW, newH := fitDimensions(origW, origH, cfg.MaxWidth, cfg.MaxHeight)

	// Create a new image scaled to fit.
	scaledImg := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(scaledImg, scaledImg.Bounds(), img, img.Bounds(), draw.Over, nil)

	// Create the background based on the configured mode.
	var ambientBg *image.RGBA
	if cfg.ImageMatteMode == "black" {
		// Classic Black Bars
		ambientBg = image.NewRGBA(image.Rect(0, 0, cfg.MaxWidth, cfg.MaxHeight))
		// Default is already transparent/black (0,0,0,0), but we ensure it's opaque black.
		for i := 0; i < len(ambientBg.Pix); i += 4 {
			ambientBg.Pix[i+3] = 255 // Set Alpha to 255
		}
	} else {
		// Premium "Extended Look" (Blurred + Jazzed-up)
		bgW, bgH := 640, 360
		lowRes := image.NewRGBA(image.Rect(0, 0, bgW, bgH))
		draw.BiLinear.Scale(lowRes, lowRes.Bounds(), img, img.Bounds(), draw.Over, nil)

		blurredLowRes := GaussianBlur(lowRes, 8.0)

		ambientBg = image.NewRGBA(image.Rect(0, 0, cfg.MaxWidth, cfg.MaxHeight))
		draw.BiLinear.Scale(ambientBg, ambientBg.Bounds(), blurredLowRes, blurredLowRes.Bounds(), draw.Over, nil)

		// Apply "Jazz-up" effects (Dimming + Vignette)
		applyAmbientEffects(ambientBg, cfg.AmbientDimming, cfg.AmbientVignette)
	}

	// Create the final destination canvas.
	finalDst := ambientBg

	// Calculate center offset.
	offsetX := (cfg.MaxWidth - newW) / 2
	offsetY := (cfg.MaxHeight - newH) / 2

	// Draw the sharp scaled image onto the center of the canvas.
	draw.Draw(finalDst, image.Rect(offsetX, offsetY, offsetX+newW, offsetY+newH), scaledImg, scaledImg.Bounds().Min, draw.Over)

	return finalDst
}

// applyAmbientEffects adjusts brightness and adds a vignette to the background.
func applyAmbientEffects(img *image.RGBA, dimFactor, vignetteStrength float64) {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	centerX, centerY := float64(w)/2, float64(h)/2
	maxDist := math.Sqrt(centerX*centerX + centerY*centerY)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			vignette := 1.0
			if vignetteStrength > 0 {
				dx := float64(x) - centerX
				dy := float64(y) - centerY
				dist := math.Sqrt(dx*dx + dy*dy)

				// Vignette factor: 1.0 at center, drops off toward corners.
				if dist > maxDist*0.3 {
					vignette = 1.0 - (dist-maxDist*0.3)/(maxDist*0.7)*vignetteStrength
				}
				if vignette < 1.0-vignetteStrength {
					vignette = 1.0 - vignetteStrength
				}
			}

			finalDim := dimFactor * vignette

			i := y*img.Stride + x*4
			// Use a helper to clamp values to 0-255
			img.Pix[i] = clamp(float64(img.Pix[i]) * finalDim)
			img.Pix[i+1] = clamp(float64(img.Pix[i+1]) * finalDim)
			img.Pix[i+2] = clamp(float64(img.Pix[i+2]) * finalDim)
		}
	}
}

func clamp(v float64) uint8 {
	if v > 255 {
		return 255
	}
	if v < 0 {
		return 0
	}
	return uint8(v)
}

func fitDimensions(origW, origH, maxW, maxH int) (int, int) {
	ratioW := float64(maxW) / float64(origW)
	ratioH := float64(maxH) / float64(origH)

	ratio := ratioW
	if ratioH < ratioW {
		ratio = ratioH
	}

	// Use the ratio to scale. We allow upscaling to ensure the image
	// fills the 4K canvas as much as possible while maintaining aspect ratio.
	newW := int(float64(origW) * ratio)
	newH := int(float64(origH) * ratio)

	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	return newW, newH
}
