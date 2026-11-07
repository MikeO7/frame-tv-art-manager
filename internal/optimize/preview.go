package optimize

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// FitMode names how artwork is placed in the frame.
type FitMode string

const (
	// FitExact means the artwork already matches the frame exactly.
	FitExact FitMode = "exact"
	// FitCrop fills the frame by cropping to its aspect ratio.
	FitCrop FitMode = "crop"
	// FitMat shows the whole artwork on a gallery mat.
	FitMat FitMode = "mat"
	// FitBlur shows the whole artwork on a blurred copy of itself.
	FitBlur FitMode = "blur"
	// FitCollage pairs a portrait with another portrait when one is available.
	FitCollage FitMode = "collage"
	// FitUnchanged means optimization is off and the TV receives the file as is.
	FitUnchanged FitMode = "unchanged"
)

// Fit describes how an image of a given size will be placed in the frame.
type Fit struct {
	Mode FitMode
	// Scale is the factor applied to the artwork's pixels; above 1 is upscaling.
	Scale float64
	// CropPercent is the share of the picture area cut away by a crop.
	CropPercent float64
}

// DescribeFit predicts the frame placement for a width x height image under
// cfg without decoding pixels. It mirrors planFit and the pad renderers.
func DescribeFit(width, height int, cfg Config) Fit {
	if width <= 0 || height <= 0 || cfg.MaxWidth <= 0 || cfg.MaxHeight <= 0 {
		return Fit{Mode: FitUnchanged, Scale: 1}
	}
	if !cfg.Enabled {
		return Fit{Mode: FitUnchanged, Scale: 1}
	}
	if width == cfg.MaxWidth && height == cfg.MaxHeight {
		return Fit{Mode: FitExact, Scale: 1}
	}
	if planFit(width, height, cfg) == fitCrop {
		return Fit{
			Mode:        FitCrop,
			Scale:       fillScale(width, height, cfg.MaxWidth, cfg.MaxHeight),
			CropPercent: croppedFraction(width, height, cfg.MaxWidth, cfg.MaxHeight) * 100,
		}
	}
	if cfg.PadStyle == padStyleBlur {
		containedWidth, _ := containedSize(width, height, cfg.MaxWidth, cfg.MaxHeight, cfg.MaxUpscale)
		return Fit{Mode: FitBlur, Scale: float64(containedWidth) / float64(width)}
	}
	mode := FitMat
	if height > width && cfg.PortraitMode == portraitModeCollage {
		mode = FitCollage
	}
	boxWidth, boxHeight := matBox(cfg, 1)
	_, containedHeight := containedSize(width, height, boxWidth, boxHeight, cfg.MaxUpscale)
	return Fit{Mode: mode, Scale: float64(containedHeight) / float64(height)}
}

// PreviewInput identifies one artwork file to render as the TV will show it.
type PreviewInput struct {
	Path string
	// Optimized reports that the file is already a current derivative, so the
	// TV receives it unchanged and effects must not be applied twice.
	Optimized bool
}

// RenderPreview renders the artwork at Path in memory exactly as the
// optimization pipeline would publish it, without writing any file. It
// enforces the working-memory limit and honors cancellation. A portrait
// destined for a collage is rendered alone, as it appears when unpaired.
func RenderPreview(ctx context.Context, input PreviewInput, cfg Config, logger *slog.Logger) (*image.RGBA, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Clean(input.Path))
	if err != nil {
		return nil, fmt.Errorf("open artwork for preview: %w", err)
	}
	defer func() { _ = file.Close() }()
	inputConfig, err := decodeInputConfig(ctx, file)
	if err != nil {
		return nil, err
	}
	if err := validateWorkingSet(workingInputFor(inputConfig), cfg); err != nil {
		return nil, err
	}
	extension := strings.ToLower(filepath.Ext(input.Path))
	if input.Optimized || !cfg.Enabled || !isOptimizableImage(extension, cfg) {
		return decodeForPreview(ctx, file)
	}
	if err := validateOutputDimensions(cfg.MaxWidth, cfg.MaxHeight, cfg.MaxOutputPixels); err != nil {
		return nil, err
	}
	filename := filepath.Base(input.Path)
	colorData, err := readColorDataWithPolicy(ctx, file, extension, cfg, logger, filename)
	if err != nil {
		return nil, fmt.Errorf("read embedded color metadata: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek image orientation: %w", err)
	}
	orientation, err := readOrientationForExtension(ctx, file, extension)
	if err != nil {
		return nil, fmt.Errorf("read image orientation: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek to start: %w", err)
	}
	decoded, _, err := image.Decode(&contextReader{ctx: ctx, reader: file})
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return transformDecoded(ctx, decoded, orientation, colorData, cfg, logger, filename)
}

func decodeForPreview(ctx context.Context, file *os.File) (*image.RGBA, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek to start: %w", err)
	}
	decoded, _, err := image.Decode(&contextReader{ctx: ctx, reader: file})
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return toRGBA(decoded), nil
}
