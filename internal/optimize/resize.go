// Package optimize provides bounded image transformations for Frame TVs.
package optimize

import (
	"context"
	"fmt"
	"image"
	_ "image/png" // Needed for decoding PNG images
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const (
	extJPG              = ".jpg"
	extJPEG             = ".jpeg"
	extPNG              = ".png"
	optimizedMarker     = "_opt.h_"
	portraitModeCollage = "collage"
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

func optimizeFile(ctx context.Context, path string, cfg Config, logger *slog.Logger, pixelWorkerLimit int) (string, bool, error) {
	filename := filepath.Base(path)
	dir := filepath.Dir(path)
	if err := ctx.Err(); err != nil {
		return filename, false, err
	}
	if !cfg.Enabled {
		return filename, false, nil
	}

	// Only optimize JPEGs (Frame TV primary format).
	ext := strings.ToLower(filepath.Ext(path))
	if !isOptimizableJPEG(ext) {
		return filename, false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return filename, false, fmt.Errorf("open image: %w", err)
	}
	defer func() { _ = f.Close() }()

	width, height, err := decodeInputConfig(f)
	if err != nil {
		return filename, false, err
	}
	if checkFastPath(filename, cfg, logger) {
		if err := validateOptimizedPixels(f); err != nil {
			return filename, false, err
		}
		return filename, false, nil
	}
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
			ctx: ctx, pixelWorkers: pixelWorkerLimit,
		})
		if err != nil {
			return filename, false, err
		}
		modified = true
	}

	//nolint:contextcheck // the request carries this exact context into the durable rename
	return handleRename(renameRequest{
		path: path, filename: filename, dir: dir, modified: modified,
		finalW: finalW, finalH: finalH, logger: logger, ctx: ctx,
	})
}

func isOptimizableJPEG(extension string) bool {
	return extension == extJPG || extension == extJPEG
}

func validateOptimizedPixels(file *os.File) error {
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek optimized image: %w", err)
	}
	if _, _, err := image.Decode(file); err != nil {
		return fmt.Errorf("validate optimized pixels: %w", err)
	}
	return nil
}

// checkFastPath returns true if the file is already optimized with matching dimensions.
func checkFastPath(filename string, cfg Config, logger *slog.Logger) bool {
	if isConfiguredOptimizedName(filename, cfg) {
		w, h, _ := artwork.ParseDimensions(filename)
		logger.Debug("skipping already optimized file", "file", filename, "dims", fmt.Sprintf("%dx%d", w, h))
		return true
	}
	return false
}

func isConfiguredOptimizedName(filename string, cfg Config) bool {
	if !strings.Contains(filename, optimizedMarker) {
		return false
	}
	w, h, ok := artwork.ParseDimensions(filename)
	return ok && w == cfg.MaxWidth && h == cfg.MaxHeight
}

// renameRequest bundles the inputs needed to decide whether an optimized file
// should be renamed to reflect its final dimensions.
type renameRequest struct {
	path, filename, dir string
	modified            bool
	finalW, finalH      int
	logger              *slog.Logger
	ctx                 context.Context
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
	ctx := req.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := durablefs.MoveExclusive(ctx, path, newPath); err != nil {
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
