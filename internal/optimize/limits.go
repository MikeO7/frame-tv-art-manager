package optimize

import (
	"fmt"
	"image"
	"io"
)

const maxInputPixels = int64(40_000_000)

func decodeInputConfig(reader io.Reader) (int, int, error) {
	config, _, err := image.DecodeConfig(reader)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	if err := validateInputDimensions(config.Width, config.Height); err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
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
