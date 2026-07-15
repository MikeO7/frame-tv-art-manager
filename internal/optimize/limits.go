package optimize

import (
	"context"
	"fmt"
	"image"
	"io"
)

const (
	maxInputPixels         = int64(40_000_000)
	defaultMaxOutputPixels = int64(12_000_000)
)

func decodeInputConfig(ctx context.Context, reader io.Reader) (int, int, error) {
	config, _, err := image.DecodeConfig(&contextReader{ctx: ctx, reader: reader})
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	if err := validateInputDimensions(config.Width, config.Height); err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
}

func validateOutputDimensions(width, height int, maxPixels int64) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid target dimensions %dx%d", width, height)
	}
	if maxPixels <= 0 {
		maxPixels = defaultMaxOutputPixels
	}
	if pixels := int64(width) * int64(height); pixels > maxPixels {
		return fmt.Errorf("target dimensions %dx%d exceed %d-pixel output limit", width, height, maxPixels)
	}
	return nil
}

func validateWorkingSet(inputWidth, inputHeight int, cfg Config) error {
	return validateWorkingPixels(int64(inputWidth)*int64(inputHeight), cfg)
}

func validateWorkingPixels(inputPixels int64, cfg Config) error {
	targetPixels := int64(cfg.MaxWidth) * int64(cfg.MaxHeight)
	estimatedBytes := inputPixels * 4 // decoded RGBA
	if cfg.LinearLightResize {
		estimatedBytes += inputPixels*8 + targetPixels*8
	}
	estimatedBytes += targetPixels * 4 // final RGBA
	if cfg.SharpenAmount > 0 {
		estimatedBytes += targetPixels * 4
	}
	if cfg.MuseumModeEnabled {
		estimatedBytes += targetPixels * 4
	}
	estimatedBytes = estimatedBytes * 5 / 4 // decoder, allocator, and encoder overhead
	maxBytes := cfg.MaxWorkingBytes
	if maxBytes <= 0 {
		maxBytes = 512 * 1024 * 1024
	}
	if estimatedBytes > maxBytes {
		return fmt.Errorf(
			"estimated image working set %d MiB exceeds %d MiB limit",
			(estimatedBytes+(1024*1024-1))/(1024*1024), maxBytes/(1024*1024),
		)
	}
	return nil
}

func validateInputDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid image dimensions %dx%d", width, height)
	}
	if pixels := int64(width) * int64(height); pixels > maxInputPixels {
		return fmt.Errorf("image dimensions %dx%d exceed %d-pixel limit", width, height, maxInputPixels)
	}
	return nil
}
