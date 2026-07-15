package optimize

import (
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
)

func isPortraitFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	imgCfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return false, err
	}
	if err := validateInputDimensions(imgCfg.Width, imgCfg.Height); err != nil {
		return false, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return false, err
	}
	orientation, err := readOrientationForExtension(f, format)
	if err != nil {
		return false, fmt.Errorf("read orientation: %w", err)
	}
	w, h := imgCfg.Width, imgCfg.Height
	if orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return h > w, nil
}

func loadAndRotateImage(path string) (*image.RGBA, error) {
	return loadAndRotateImageWithPolicy(path, "assume-srgb", slog.New(slog.DiscardHandler))
}

func loadAndRotateImageWithPolicy(path, profilePolicy string, logger *slog.Logger) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	imgCfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if err := validateInputDimensions(imgCfg.Width, imgCfg.Height); err != nil {
		return nil, err
	}
	extension := extJPG
	if format == "png" {
		extension = extPNG
	}
	colorMetadata, err := enforceColorProfilePolicy(f, extension, profilePolicy)
	if err != nil {
		return nil, err
	}
	if colorMetadata != "" {
		logger.Warn(
			"embedded color metadata is not transformed; treating decoded samples as sRGB",
			"file", filepath.Base(path), "metadata", colorMetadata,
		)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	orientation, err := readOrientationForExtension(f, format)
	if err != nil {
		return nil, fmt.Errorf("read orientation: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return toRGBA(RotateImage(img, orientation)), nil
}
