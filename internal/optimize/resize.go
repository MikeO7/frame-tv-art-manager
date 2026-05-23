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

// parseDimensions extracts width and height from a filename like "..._3840x2160_opt.h_...".
func parseDimensions(filename string) (int, int, bool) {
	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		identity = strings.Join(parts[:len(parts)-1], "__")
	}

	parts := strings.Split(identity, "_")
	for _, p := range parts {
		if strings.Contains(p, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(p, "%dx%d", &w, &h); n == 2 {
				return w, h, true
			}
		}
	}
	return 0, 0, false
}

// OptimizeFile checks if an image needs resizing and optimizes it
// in-place. It encapsulates the naming convention and returns the
// final path/filename and whether the file was modified.
//
//nolint:gocyclo,nestif,funlen // the package structure makes the OptimizeFile name acceptable and backward compatible
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
		w, h, ok := parseDimensions(filename)
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
		// Full decode required.
		if _, err := f.Seek(0, 0); err != nil {
			return filename, false, fmt.Errorf("seek to start: %w", err)
		}

		img, _, err := image.Decode(f)
		if err != nil {
			return filename, false, fmt.Errorf("decode image: %w", err)
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

		out, err := os.Create(path)
		if err != nil {
			return filename, false, fmt.Errorf("create optimized file: %w", err)
		}
		defer func() { _ = out.Close() }()

		err = jpeg.Encode(out, rgba, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
		if err != nil {
			return filename, false, fmt.Errorf("encode jpeg: %w", err)
		}
		_ = out.Close()

		newBounds := rgba.Bounds()
		finalW, finalH = newBounds.Dx(), newBounds.Dy()
		modified = true
	}

	// Rename the file using the naming policy if needed.
	currentW, currentH, _ := parseDimensions(filename)
	isOpt := strings.Contains(filename, "_opt.h_")

	if !modified && isOpt && currentW == finalW && currentH == finalH {
		return filename, false, nil
	}

	identity := strings.TrimSuffix(filename, ext)
	var hash string

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
		hash = parts[1]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		hash = parts[len(parts)-1]
		identity = strings.Join(parts[:len(parts)-1], "__")
	} else {
		hash = "local"
	}

	if lastUnderscore := strings.LastIndex(identity, "_"); lastUnderscore != -1 {
		suffix := identity[lastUnderscore+1:]
		if strings.Contains(suffix, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(suffix, "%dx%d", &w, &h); n == 2 {
				identity = identity[:lastUnderscore]
			}
		}
	}
	identity = strings.Split(identity, "_opt")[0]

	newFilename := fmt.Sprintf("%s_%dx%d_opt.h_%s%s", identity, finalW, finalH, hash, ext)
	if newFilename == filename {
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
