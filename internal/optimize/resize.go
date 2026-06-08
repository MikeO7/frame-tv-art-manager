// Package optimize provides image resizing and quality optimization
// for Samsung Frame TV artwork. Frame TVs are 4K (3840×2160), so
// uploading larger images wastes bandwidth and transfer time.
package optimize

import (
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // Needed for decoding PNG images
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
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
// in-place. It encapsulates the naming convention and returns the
// final path/filename and whether the file was modified.
//
// Parameters:
//   - path: The absolute or relative file path to the original image (must be a JPEG).
//   - cfg:  The configuration containing MaxWidth, MaxHeight, SmartCrop, and Quality preferences.
//   - logger: A structured logger for emitting processing stages and skipped file notifications.
//
// Returns:
//   - string: The final filename of the optimized image (e.g., "monet_opt.h_1a2b.jpg").
//   - bool:   True if the file was actively modified, false if skipped due to dimensions or config.
//   - error:  Any file I/O or decoding error encountered during the operation.
//
// Example:
//
//	finalName, modified, err := optimize.OptimizeFile("/data/artwork/monet.jpg", optimize.DefaultConfig(), logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if modified {
//	    fmt.Printf("Optimized file into %s\n", finalName)
//	}
//
//nolint:funlen // image optimization pipeline
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (string, bool, error) {
	filename := filepath.Base(path)
	dir := filepath.Dir(path)
	if !cfg.Enabled {
		return filename, false, nil
	}

	// Only optimize JPEGs (Frame TV primary format).
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" {
		return filename, false, nil
	}

	// Fast path check: if filename is already optimized with matching dimensions, skip!
	if strings.Contains(filename, "_opt.h_") {
		w, h, ok := artwork.ParseDimensions(filename)
		if ok && w <= cfg.MaxWidth && h <= cfg.MaxHeight {
			logger.Debug("skipping already optimized file", "file", filename, "dims", fmt.Sprintf("%dx%d", w, h))
			return filename, false, nil
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return filename, false, fmt.Errorf("open image: %w", err)
	}
	defer func() { _ = f.Close() }()

	imgCfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return filename, false, fmt.Errorf("decode image config: %w", err)
	}

	width, height := imgCfg.Width, imgCfg.Height
	needsAdjustment := width != cfg.MaxWidth || height != cfg.MaxHeight

	var finalW, finalH int
	var modified bool

	if !needsAdjustment && !cfg.MuseumModeEnabled {
		finalW, finalH = width, height
		modified = false
	} else {
		finalW, finalH, err = rewriteImage(f, path, filename, width, height, needsAdjustment, cfg, logger)
		if err != nil {
			return filename, false, err
		}
		modified = true
	}

	// Rename the file using the naming policy if needed.
	currentW, currentH, _ := artwork.ParseDimensions(filename)
	isOpt := strings.Contains(filename, "_opt.h_")

	if !modified && isOpt && currentW == finalW && currentH == finalH {
		return filename, false, nil
	}

	newFilename, changed := artwork.BuildOptimizedNameFromFile(filename, finalW, finalH)
	if !changed {
		return filename, modified, nil
	}

	newPath := filepath.Join(dir, newFilename)
	if err := os.Rename(path, newPath); err != nil {
		return filename, modified, fmt.Errorf("rename to optimized: %w", err)
	}

	logger.Info("updated optimized filename", "old", filename, "new", newFilename)
	return newFilename, true, nil
}

// ValidateImage performs a low-cost check to ensure an image file is not corrupt.
// It opens the file and attempts to decode its configuration header without fully loading
// the pixel data into memory.
//
// Parameters:
//   - path: The absolute or relative file path to the image to validate.
//
// Returns:
//   - error: Returns an error if the file cannot be opened or if the image header is corrupt.
//
// Example:
//
//	if err := optimize.ValidateImage("/data/artwork/download.jpg"); err != nil {
//	    log.Printf("Invalid image format: %v", err)
//	    os.Remove("/data/artwork/download.jpg")
//	}
func ValidateImage(path string) error {
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

// rewriteImage handles the actual decoding, pixel modifications (cropping, sharpening, dithering),
// and re-encoding of the image back to disk.
func rewriteImage(f *os.File, path, filename string, width, height int, needsAdjustment bool, cfg Config, logger *slog.Logger) (int, int, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}

	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image: %w", err)
	}

	logger.Info("optimizing image", "file", filename, "original_dims", fmt.Sprintf("%dx%d", width, height))

	rgba := toRGBA(img)
	if needsAdjustment {
		rgba = centerCrop(rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled)
	}
	rgba = sharpen(rgba)
	if cfg.MuseumModeEnabled {
		rgba = applyMuseumMode(rgba, cfg.MuseumModeIntensity)
	}
	rgba = dither(rgba)

	// Close original file so we can overwrite or rename it.
	_ = f.Close()

	// Save back to disk.
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, fmt.Errorf("create optimized file: %w", err)
	}
	defer func() { _ = out.Close() }()

	err = jpeg.Encode(out, rgba, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	if err != nil {
		return 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}
	_ = out.Close()

	newBounds := rgba.Bounds()
	return newBounds.Dx(), newBounds.Dy(), nil
}
