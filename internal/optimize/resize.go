// Package optimize provides image resizing and quality optimization
// for Samsung Frame TV artwork. Frame TVs are 4K (3840×2160), so
// uploading larger images wastes bandwidth and transfer time.
package optimize

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
)

// Config holds image optimization settings.
type Config struct {
	Enabled     bool
	MaxWidth    int
	MaxHeight   int
	JPEGQuality int
}

// DefaultConfig returns sensible defaults for Frame TV display.
func DefaultConfig() Config {
	return Config{
		Enabled:     true,
		MaxWidth:    3840,
		MaxHeight:   2160,
		JPEGQuality: 92,
	}
}

// OptimizeFile checks if an image needs resizing and optimizes it
// in-place. Returns true if the file was modified.
//
// The function:
//  1. Decodes the image to get dimensions
//  2. If larger than MaxWidth×MaxHeight, resizes using Lanczos resampling
//  3. Re-encodes at the target JPEG quality
//  4. Writes atomically (temp file + rename)
//  5. Skips PNG files (may need transparency for matte effects)
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (bool, error) {
	if !cfg.Enabled {
		return false, nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".png" {
		// Skip PNG — may need transparency for matte effects.
		return false, nil
	}

	// Decode the image.
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // Path is verified
	if err != nil {
		return false, fmt.Errorf("open image: %w", err)
	}

	img, format, err := image.Decode(f)
	_ = f.Close()
	if err != nil {
		return false, fmt.Errorf("decode image: %w", err)
	}

	bounds := img.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	// Check if resize is needed.
	if origW <= cfg.MaxWidth && origH <= cfg.MaxHeight {
		logger.Debug("image already optimal size",
			"file", filepath.Base(path),
			"width", origW,
			"height", origH,
		)
		return false, nil
	}

	// Calculate new dimensions preserving aspect ratio.
	newW, newH := fitDimensions(origW, origH, cfg.MaxWidth, cfg.MaxHeight)

	logger.Info("resizing image",
		"file", filepath.Base(path),
		"from", fmt.Sprintf("%dx%d", origW, origH),
		"to", fmt.Sprintf("%dx%d", newW, newH),
		"format", format,
	)

	// Resize using CatmullRom (high quality, similar to Lanczos).
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	// Write to temp file then rename.
	tmpPath := path + ".opt.tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}

	switch ext {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(out, dst, &jpeg.Options{Quality: cfg.JPEGQuality})
	case ".png":
		err = png.Encode(out, dst)
	default:
		err = jpeg.Encode(out, dst, &jpeg.Options{Quality: cfg.JPEGQuality})
	}

	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("encode resized image: %w", err)
	}

	// Get size comparison.
	origStat, _ := os.Stat(path)
	newStat, _ := os.Stat(tmpPath)

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename optimized file: %w", err)
	}

	// Ensure inclusive permissions for Mac access.
	_ = os.Chmod(path, 0644); //nosec G302

	if origStat != nil && newStat != nil {
		logger.Info("image optimized",
			"file", filepath.Base(path),
			"original_bytes", origStat.Size(),
			"optimized_bytes", newStat.Size(),
			"saved_pct", fmt.Sprintf("%.0f%%", (1-float64(newStat.Size())/float64(origStat.Size()))*100),
		)
	}

	return true, nil
}

// fitDimensions calculates new width and height that fit within
// maxW×maxH while preserving the aspect ratio.
func fitDimensions(origW, origH, maxW, maxH int) (int, int) {
	ratioW := float64(maxW) / float64(origW)
	ratioH := float64(maxH) / float64(origH)

	ratio := ratioW
	if ratioH < ratioW {
		ratio = ratioH
	}

	// Never upscale.
	if ratio > 1.0 {
		ratio = 1.0
	}

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
