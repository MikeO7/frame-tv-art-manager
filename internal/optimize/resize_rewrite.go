package optimize

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

type rewriteParams struct {
	f               *os.File
	path, filename  string
	width, height   int
	needsAdjustment bool
	cfg             Config
	logger          *slog.Logger
	ctx             context.Context
	pixelWorkers    int
}

//nolint:funlen,gocognit,gocyclo // ordered decode, transform, validation, and durable publication pipeline
func rewriteImage(params rewriteParams) (int, int, error) {
	f := params.f
	cfg := params.cfg
	if params.ctx == nil {
		params.ctx = context.Background()
	}
	if params.pixelWorkers <= 0 {
		params.pixelWorkers = defaultPixelWorkers()
	}
	if err := params.ctx.Err(); err != nil {
		return 0, 0, err
	}

	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}
	extension := strings.ToLower(filepath.Ext(params.path))
	orientation, orientationErr := readOrientationForExtension(f, extension)
	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image: %w", err)
	}
	if orientationErr != nil {
		return 0, 0, fmt.Errorf("read image orientation: %w", orientationErr)
	}
	if err := params.ctx.Err(); err != nil {
		return 0, 0, err
	}

	params.logger.Info("optimizing image", "file", params.filename, "original_dims", fmt.Sprintf("%dx%d", params.width, params.height))
	rgba := toRGBA(RotateImage(img, orientation))
	newW, newH := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if newW != cfg.MaxWidth || newH != cfg.MaxHeight {
		if newH > newW && cfg.PortraitMode != "crop" {
			rgba = padPortraitWithOptions(rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.LinearLightResize)
		} else {
			rgba = centerCropWithOptions(
				rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled, cfg.SmartCropMinGain, cfg.LinearLightResize,
			)
		}
	}
	if err := params.ctx.Err(); err != nil {
		return 0, 0, err
	}

	if params.needsAdjustment {
		rgba = sharpenWithOptions(rgba, cfg.SharpenAmount, cfg.SharpenThreshold, params.pixelWorkers)
	}
	if cfg.MuseumModeEnabled {
		rgba = applyMuseumModeWithWorkers(rgba, cfg.MuseumModeIntensity, params.pixelWorkers)
	} else if cfg.DitherEnabled {
		rgba = ditherWithWorkers(rgba, params.pixelWorkers)
	}
	if err := params.ctx.Err(); err != nil {
		return 0, 0, err
	}
	if err := f.Close(); err != nil {
		return 0, 0, fmt.Errorf("close source image: %w", err)
	}

	tmpPath, err := encodeOptimizedTemporary(params.path, rgba, cfg.OptimizeJPEGQuality)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = os.Remove(tmpPath) }()
	if err := ValidateImage(tmpPath); err != nil {
		return 0, 0, fmt.Errorf("validate optimized image: %w", err)
	}
	if err := durablefs.Rename(params.ctx, tmpPath, params.path); err != nil {
		return 0, 0, fmt.Errorf("replace optimized image: %w", err)
	}
	return rgba.Bounds().Dx(), rgba.Bounds().Dy(), nil
}

func encodeOptimizedTemporary(path string, imageData image.Image, quality int) (string, error) {
	out, err := os.CreateTemp(filepath.Dir(path), ".optimize-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create optimized temporary file: %w", err)
	}
	tmpPath := out.Name()
	var encodeErr error
	if strings.EqualFold(filepath.Ext(path), extPNG) {
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		encodeErr = encoder.Encode(out, imageData)
	} else {
		encodeErr = jpeg.Encode(out, imageData, &jpeg.Options{Quality: quality})
	}
	if encodeErr != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("encode optimized image: %w", encodeErr)
	}
	if err := errors.Join(out.Chmod(0o644), out.Sync(), out.Close()); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("finalize optimized image: %w", err)
	}
	return tmpPath, nil
}
