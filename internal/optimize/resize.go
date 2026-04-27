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
	"github.com/muesli/smartcrop"
	"github.com/nfnt/resize"
)

// Config holds image optimization settings.
type Config struct {
	Enabled     bool
	MaxWidth    int
	MaxHeight   int
	OptimizeJPEGQuality int
	SmartCropEnabled   bool
}

// DefaultConfig returns sensible defaults for Frame TV display.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		MaxWidth:            3840,
		MaxHeight:           2160,
		OptimizeJPEGQuality: 92,
		SmartCropEnabled:    true,
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

	// Check if resize or crop is needed.
	aspectRatio := float64(cfg.MaxWidth) / float64(cfg.MaxHeight)
	imgRatio := float64(origW) / float64(origH)
	
	// We optimize if:
	// 1. Image is oversized
	// 2. Image has wrong aspect ratio and SmartCropEnabled is enabled
	needsResize := origW > cfg.MaxWidth || origH > cfg.MaxHeight
	needsCrop := cfg.SmartCropEnabled && (fmt.Sprintf("%.3f", imgRatio) != fmt.Sprintf("%.3f", aspectRatio))

	if !needsResize && !needsCrop {
		logger.Debug("image already optimal size and aspect",
			"file", filepath.Base(path),
			"width", origW,
			"height", origH,
		)
		return false, nil
	}

	var dst image.Image
	if cfg.SmartCropEnabled {
		logger.Info("smart cropping image",
			"file", filepath.Base(path),
			"from", fmt.Sprintf("%dx%d", origW, origH),
			"target_aspect", "16:9",
		)
		
		analyzer := smartcrop.NewAnalyzer(resize.NewDefaultResizer())
		topCrop, err := analyzer.FindBestCrop(img, cfg.MaxWidth, cfg.MaxHeight)
		if err != nil {
			return false, fmt.Errorf("find best crop: %w", err)
		}

		type subImager interface {
			SubImage(r image.Rectangle) image.Image
		}
		
		// Helper to handle both NRGBA/RGBA and other image types
		var croppedImg image.Image
		if si, ok := img.(subImager); ok {
			croppedImg = si.SubImage(topCrop)
		} else {
			// Fallback: draw to a new RGBA then crop
			tempDst := image.NewRGBA(img.Bounds())
			draw.Draw(tempDst, tempDst.Bounds(), img, bounds.Min, draw.Src)
			croppedImg = tempDst.SubImage(topCrop)
		}

		// Resize the crop to final 4K dimensions
		finalDst := image.NewRGBA(image.Rect(0, 0, cfg.MaxWidth, cfg.MaxHeight))
		draw.CatmullRom.Scale(finalDst, finalDst.Bounds(), croppedImg, croppedImg.Bounds(), draw.Over, nil)
		dst = finalDst
	} else {
		// Calculate new dimensions preserving aspect ratio (Fit).
		newW, newH := fitDimensions(origW, origH, cfg.MaxWidth, cfg.MaxHeight)

		logger.Info("resizing image (fit)",
			"file", filepath.Base(path),
			"from", fmt.Sprintf("%dx%d", origW, origH),
			"to", fmt.Sprintf("%dx%d", newW, newH),
		)

		// Resize using CatmullRom.
		finalDst := image.NewRGBA(image.Rect(0, 0, newW, newH))
		draw.CatmullRom.Scale(finalDst, finalDst.Bounds(), img, bounds, draw.Over, nil)
		dst = finalDst
	}

	// Write to temp file then rename.
	tmpPath := path + ".opt.tmp"
	out, err := os.Create(filepath.Clean(tmpPath)) //nolint:gosec // Safe temporary path
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}

	switch ext {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(out, dst, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	case ".png":
		err = png.Encode(out, dst)
	default:
		err = jpeg.Encode(out, dst, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
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
	_ = os.Chmod(path, 0644) //nolint:gosec // Requires inclusive permissions

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

// fitDimensions calculates new width and height...
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
