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
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

// Supported image extensions and the marker embedded in optimized filenames.
const (
	extJPG          = ".jpg"
	extJPEG         = ".jpeg"
	extPNG          = ".png"
	optimizedMarker = "_opt.h_"
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
	PortraitMode        string
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
		PortraitMode:        "crop",
	}
}

// OptimizeFile resizes and enhances a JPEG in-place to the configured Frame TV
// dimensions, writing an optimized copy adjacent to the original and applying
// the hash-based "_opt.h_" naming convention.
//
// Parameters:
//   - path: The absolute path to the local image file.
//   - cfg: Configuration specifying target dimensions and processing rules.
//   - logger: The structured logger for recording optimization events.
//
// Returns:
//   - string: The resulting filename (the renamed optimized file, or the original when no work was needed).
//   - bool: Indicates whether the file was modified.
//   - error: Any I/O or decode error encountered. Non-JPEG inputs and already-optimized files are skipped.
//
// Example:
//
//	optName, modified, err := OptimizeFile("/data/artwork/photo.jpg", cfg, logger)
//	if err != nil {
//	    return err
//	}
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (string, bool, error) {
	filename := filepath.Base(path)
	dir := filepath.Dir(path)
	if !cfg.Enabled {
		return filename, false, nil
	}

	// Only optimize JPEGs (Frame TV primary format).
	ext := strings.ToLower(filepath.Ext(path))
	if ext != extJPG && ext != extJPEG {
		return filename, false, nil
	}

	if checkFastPath(filename, cfg, logger) {
		return filename, false, nil
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
		finalW, finalH, err = rewriteImage(rewriteParams{
			f: f, path: path, filename: filename, width: width,
			height: height, needsAdjustment: needsAdjustment, cfg: cfg, logger: logger,
		})
		if err != nil {
			return filename, false, err
		}
		modified = true
	}

	return handleRename(renameRequest{
		path: path, filename: filename, dir: dir, modified: modified,
		finalW: finalW, finalH: finalH, logger: logger,
	})
}

// checkFastPath returns true if the file is already optimized with matching dimensions.
func checkFastPath(filename string, cfg Config, logger *slog.Logger) bool {
	if !strings.Contains(filename, optimizedMarker) {
		return false
	}
	w, h, ok := artwork.ParseDimensions(filename)
	if ok && w == cfg.MaxWidth && h == cfg.MaxHeight {
		logger.Debug("skipping already optimized file", "file", filename, "dims", fmt.Sprintf("%dx%d", w, h))
		return true
	}
	return false
}

// renameRequest bundles the inputs needed to decide whether an optimized file
// should be renamed to reflect its final dimensions.
type renameRequest struct {
	path, filename, dir string
	modified            bool
	finalW, finalH      int
	logger              *slog.Logger
}

// handleRename renames the file according to optimized names if needed.
func handleRename(req renameRequest) (string, bool, error) {
	path := req.path
	filename := req.filename
	dir := req.dir
	modified := req.modified
	finalW, finalH := req.finalW, req.finalH
	logger := req.logger

	currentW, currentH, _ := artwork.ParseDimensions(filename)
	isOpt := strings.Contains(filename, optimizedMarker)

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

// ValidateImage performs a low-cost corruption check by decoding only the
// image's configuration header, without loading pixel data into memory.
func ValidateImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, _, err = image.DecodeConfig(f)
	return err
}

// rewriteParams bundles the inputs needed to decode, transform, and re-encode
// an image to its optimized form on disk.
type rewriteParams struct {
	f               *os.File
	path, filename  string
	width, height   int
	needsAdjustment bool
	cfg             Config
	logger          *slog.Logger
}

// rewriteImage handles the actual decoding, pixel modifications (cropping, sharpening, dithering),
// and re-encoding of the image back to disk.
func rewriteImage(params rewriteParams) (int, int, error) {
	f := params.f
	cfg := params.cfg
	_ = params.needsAdjustment // recalculated after rotation

	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}
	orientation, _ := ReadOrientation(f)

	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image: %w", err)
	}

	params.logger.Info("optimizing image",
		"file", params.filename,
		"original_dims", fmt.Sprintf("%dx%d", params.width, params.height))

	img = RotateImage(img, orientation)
	rgba := toRGBA(img)

	newW, newH := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if newW != cfg.MaxWidth || newH != cfg.MaxHeight {
		if newH > newW && cfg.PortraitMode != "crop" {
			rgba = padPortrait(rgba, cfg.MaxWidth, cfg.MaxHeight)
		} else {
			rgba = centerCrop(rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled)
		}
	}

	rgba = sharpen(rgba)
	if cfg.MuseumModeEnabled {
		rgba = applyMuseumMode(rgba, cfg.MuseumModeIntensity)
	} else {
		rgba = dither(rgba)
	}

	// Close original file so we can overwrite or rename it.
	_ = f.Close()

	// Save back to disk.
	// 0o644 is intentional — artwork files must be world-readable so they
	// can be accessed over SMB/NFS network shares. Do NOT tighten to 0o600.
	out, err := os.OpenFile(params.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("create optimized file: %w", err)
	}
	defer func() { _ = out.Close() }()

	err = jpeg.Encode(out, rgba, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	if err != nil {
		return 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}
	_ = out.Close()
	// Explicit chmod to 0o644 is required to override restrictive system umasks (e.g. 0077)
	// so files are readable over SMB/NFS network shares. Do NOT tighten to 0o600.
	_ = os.Chmod(params.path, 0o644)

	newBounds := rgba.Bounds()
	return newBounds.Dx(), newBounds.Dy(), nil
}
