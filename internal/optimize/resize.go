// Package optimize provides bounded image transformations for Frame TVs.
package optimize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/png" // Needed for decoding PNG images
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const (
	extJPG                = ".jpg"
	extJPEG               = ".jpeg"
	extPNG                = ".png"
	formatJPEG            = "jpeg"
	formatPNG             = "png"
	profileRejectEmbedded = "reject-embedded"
	portraitModeCollage   = "collage"
)

type Config struct {
	Enabled             bool
	SmartCropEnabled    bool
	SmartCropMinGain    float64
	MaxWidth            int
	MaxHeight           int
	MaxOutputPixels     int64
	MaxWorkingBytes     int64
	OptimizeJPEGQuality int
	OptimizePNG         bool
	LinearLightResize   bool
	SharpenAmount       float64
	SharpenThreshold    int
	ColorProfilePolicy  string
	MuseumModeEnabled   bool
	MuseumModeIntensity int
	PortraitMode        string
}

// DefaultConfig returns sensible defaults for Frame TV display.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		SmartCropEnabled:    false,
		SmartCropMinGain:    0.03,
		MaxWidth:            3840,
		MaxHeight:           2160,
		MaxOutputPixels:     defaultMaxOutputPixels,
		MaxWorkingBytes:     512 * 1024 * 1024,
		OptimizeJPEGQuality: 95,
		OptimizePNG:         true,
		LinearLightResize:   true,
		SharpenAmount:       0.25,
		SharpenThreshold:    4,
		ColorProfilePolicy:  "assume-srgb",
		MuseumModeEnabled:   false,
		MuseumModeIntensity: 5,
		PortraitMode:        "crop",
	}
}

func optimizeFile(ctx context.Context, path string, cfg Config, logger *slog.Logger, pixelWorkerLimit int) (string, bool, error) {
	return optimizeFileWithPolicy(ctx, path, filepath.Base(path), false, cfg, logger, pixelWorkerLimit)
}

//nolint:funlen,gocognit,gocyclo,revive // explicit transaction inputs avoid hidden optimizer state
func optimizeFileWithPolicy(
	ctx context.Context,
	path string,
	label string,
	force bool,
	cfg Config,
	logger *slog.Logger,
	pixelWorkerLimit int,
) (string, bool, error) {
	filename := filepath.Base(path)
	dir := filepath.Dir(path)
	if err := ctx.Err(); err != nil {
		return filename, false, err
	}
	if !cfg.Enabled {
		return filename, false, nil
	}
	if err := validateOutputDimensions(cfg.MaxWidth, cfg.MaxHeight, cfg.MaxOutputPixels); err != nil {
		return filename, false, err
	}

	ext := strings.ToLower(filepath.Ext(path))
	if !isOptimizableImage(ext, cfg) {
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
	if err := validateWorkingSet(width, height, cfg); err != nil {
		return filename, false, err
	}
	colorMetadata, err := enforceColorProfilePolicy(ctx, f, ext, cfg.ColorProfilePolicy)
	if err != nil {
		return filename, false, fmt.Errorf("enforce color profile policy: %w", err)
	}
	if colorMetadata != "" {
		logger.Warn("embedded color metadata is not transformed; treating decoded samples as sRGB", "file", filename, "metadata", colorMetadata)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return filename, false, fmt.Errorf("seek image orientation: %w", err)
	}
	orientation, err := readOrientationForExtension(f, ext)
	if err != nil {
		return filename, false, fmt.Errorf("read image orientation: %w", err)
	}
	displayWidth, displayHeight := width, height
	if orientation >= 5 && orientation <= 8 {
		displayWidth, displayHeight = height, width
	}
	needsAdjustment := force || orientation != 1 || displayWidth != cfg.MaxWidth || displayHeight != cfg.MaxHeight
	if !needsAdjustment && !cfg.MuseumModeEnabled {
		if err := validateOptimizedPixels(f, cfg.MaxWidth, cfg.MaxHeight); err != nil {
			return filename, false, err
		}
	}

	var finalW, finalH int
	var modified bool

	if !needsAdjustment && !cfg.MuseumModeEnabled {
		finalW, finalH = width, height
		modified = false
	} else {
		finalW, finalH, err = rewriteImage(rewriteParams{
			f: f, path: path, filename: filename, width: width,
			height: height, cfg: cfg, logger: logger,
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
		finalW: finalW, finalH: finalH, logger: logger, ctx: ctx, cfg: cfg, label: label,
	})
}

func isOptimizableJPEG(extension string) bool {
	return extension == extJPG || extension == extJPEG
}

func isOptimizableImage(extension string, cfg Config) bool {
	return isOptimizableJPEG(extension) || (extension == extPNG && cfg.OptimizePNG)
}

func readOrientationForExtension(reader io.Reader, extension string) (int, error) {
	if extension == extPNG || extension == formatPNG {
		return ReadPNGOrientation(reader)
	}
	return ReadOrientation(reader)
}

func validateOptimizedPixels(file *os.File, expectedWidth, expectedHeight int) error {
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek optimized image: %w", err)
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return fmt.Errorf("validate optimized pixels: %w", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != expectedWidth || bounds.Dy() != expectedHeight {
		return fmt.Errorf(
			"validate optimized dimensions: got %dx%d, want %dx%d",
			bounds.Dx(), bounds.Dy(), expectedWidth, expectedHeight,
		)
	}
	return nil
}

// TransformKey identifies every setting and algorithm revision that can alter
// encoded output bytes. It is durable manifest metadata, never filename state.
func TransformKey(cfg Config) string {
	description := fmt.Sprintf(
		"frame-image-v4|%dx%d|q=%d|png=%t|linear=%t|smart=%t|gain=%.6g|sharp=%.6g/%d|museum=%t/%d|portrait=%s|profile=%s",
		cfg.MaxWidth, cfg.MaxHeight, cfg.OptimizeJPEGQuality, cfg.OptimizePNG, cfg.LinearLightResize,
		cfg.SmartCropEnabled, cfg.SmartCropMinGain, cfg.SharpenAmount, cfg.SharpenThreshold,
		cfg.MuseumModeEnabled, cfg.MuseumModeIntensity,
		cfg.PortraitMode, cfg.ColorProfilePolicy,
	)
	digest := sha256.Sum256([]byte(description))
	return hex.EncodeToString(digest[:])
}

// renameRequest bundles the inputs needed to decide whether an optimized file
// should be renamed to reflect its final dimensions.
type renameRequest struct {
	path, filename, dir string
	label               string
	modified            bool
	finalW, finalH      int
	logger              *slog.Logger
	ctx                 context.Context
	cfg                 Config
}

// handleRename renames the file according to optimized names if needed.
func handleRename(req renameRequest) (string, bool, error) {
	path := req.path
	filename := req.filename
	dir := req.dir
	modified := req.modified
	logger := req.logger
	ctx := req.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if !modified {
		return filename, false, nil
	}
	digest, err := fileDigest(ctx, path)
	if err != nil {
		return filename, modified, fmt.Errorf("hash optimized output: %w", err)
	}
	label := req.label
	if label == "" {
		label = filename
	}
	newFilename, err := availableContentName(ctx, contentNameRequest{
		directory: dir, currentName: filename, label: label, digest: digest, extension: filepath.Ext(filename),
	})
	if err != nil {
		return filename, modified, fmt.Errorf("choose optimized output name: %w", err)
	}
	if newFilename == filename {
		return filename, true, nil
	}

	newPath := filepath.Join(dir, newFilename)
	if err := durablefs.MoveExclusive(ctx, path, newPath); err != nil {
		return filename, modified, fmt.Errorf("rename to optimized: %w", err)
	}

	logger.Info("updated optimized filename", "old", filename, "new", newFilename)
	return newFilename, true, nil
}
