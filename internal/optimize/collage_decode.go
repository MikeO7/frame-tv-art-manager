package optimize

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
)

func isPortraitFile(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	imgCfg, format, err := image.DecodeConfig(&contextReader{ctx: ctx, reader: f})
	if err != nil {
		return false, err
	}
	if err := validateInputDimensions(imgCfg.Width, imgCfg.Height); err != nil {
		return false, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return false, err
	}
	orientation, err := readOrientationForExtension(ctx, f, format)
	if err != nil {
		return false, fmt.Errorf("read orientation: %w", err)
	}
	w, h := imgCfg.Width, imgCfg.Height
	if orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return h > w, nil
}

func loadAndRotateImageWithConfig(ctx context.Context, path string, cfg Config, logger *slog.Logger) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	imgCfg, format, err := image.DecodeConfig(&contextReader{ctx: ctx, reader: f})
	if err != nil {
		return nil, err
	}
	if err := validateInputDimensions(imgCfg.Width, imgCfg.Height); err != nil {
		return nil, err
	}
	extension := extJPG
	if format == formatPNG {
		extension = extPNG
	}
	colorMetadata, err := readColorDataWithPolicy(ctx, f, extension, cfg, logger, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	orientation, err := readOrientationForExtension(ctx, f, format)
	if err != nil {
		return nil, fmt.Errorf("read orientation: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(&contextReader{ctx: ctx, reader: f})
	if err != nil {
		return nil, err
	}
	return toRGBA(RotateImage(normalizeColor(img, colorMetadata, cfg, logger, filepath.Base(path)), orientation)), nil
}
