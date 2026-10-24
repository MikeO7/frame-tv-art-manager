// Package optimize provides bounded image transformations for Frame TVs.
//
//nolint:revive // the optimizer's public configuration and transactional entry point intentionally remain colocated
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
	"time"
)

const (
	extJPG                = ".jpg"
	extJPEG               = ".jpeg"
	extPNG                = ".png"
	formatJPEG            = "jpeg"
	formatPNG             = "png"
	profileRejectEmbedded = "reject-embedded"
	profileConvertSRGB    = "convert-srgb"
	profileAssumeSRGB     = "assume-srgb"
	smartCropProviderHTTP = "http"
	jpegICCMetadata       = "JPEG ICC"
	jpegICCSignature      = "ICC_PROFILE\x00"
	pngChunkICC           = "iCCP"
	pngChunkCICP          = "cICP"
	pngChunkGamma         = "gAMA"
	pngChunkChromaticity  = "cHRM"
	pngChunkImageData     = "IDAT"
	pngICCMetadata        = "PNG iCCP"
	pngCICPMetadata       = "PNG cICP"
	portraitModeCollage   = "collage"
	transformRevision     = "frame-image-v7"
)

type Config struct {
	Enabled                        bool
	SmartCropEnabled               bool
	SmartCropMinGain               float64
	SmartCropProtection            bool
	SmartCropProtectionStrength    float64
	SmartCropProvider              string
	SmartCropProviderURL           string
	SmartCropProviderMinConfidence float64
	SmartCropProviderTimeout       time.Duration
	MaxWidth                       int
	MaxHeight                      int
	MaxOutputPixels                int64
	MaxWorkingBytes                int64
	OptimizeJPEGQuality            int
	OptimizePNG                    bool
	LinearLightResize              bool
	SharpenAmount                  float64
	SharpenThreshold               int
	ColorProfilePolicy             string
	HDRToneMap                     bool
	HDRSourcePeakNits              float64
	HDRTargetPeakNits              float64
	MuseumModeEnabled              bool
	MuseumModeIntensity            int
	PortraitMode                   string
}

//nolint:revive // explicit transaction inputs avoid hidden optimizer state
func optimizeFileWithPolicy(
	ctx context.Context,
	path string,
	label string,
	force bool,
	cfg Config,
	logger *slog.Logger,
) (string, bool, error) {
	filename := filepath.Base(path)
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

	inputConfig, err := decodeInputConfig(ctx, f)
	if err != nil {
		return filename, false, err
	}
	if err := validateWorkingSet(workingInputFor(inputConfig), cfg); err != nil {
		return filename, false, err
	}
	colorMetadata, err := readColorDataWithPolicy(ctx, f, ext, cfg, logger, filename)
	if err != nil {
		return filename, false, fmt.Errorf("read embedded color metadata: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return filename, false, fmt.Errorf("seek image orientation: %w", err)
	}
	orientation, err := readOrientationForExtension(ctx, f, ext)
	if err != nil {
		return filename, false, fmt.Errorf("read image orientation: %w", err)
	}
	displayWidth, displayHeight := inputConfig.Width, inputConfig.Height
	if orientation >= 5 && orientation <= 8 {
		displayWidth, displayHeight = displayHeight, displayWidth
	}
	needsColorNormalization := len(colorMetadata.icc) > 0 && cfg.ColorProfilePolicy == profileConvertSRGB
	needsHDRToneMap := colorMetadata.hdr != nil && cfg.HDRToneMap
	needsAdjustment := force || orientation != 1 || displayWidth != cfg.MaxWidth ||
		displayHeight != cfg.MaxHeight || needsColorNormalization || needsHDRToneMap
	if !needsAdjustment && !cfg.MuseumModeEnabled {
		return filename, false, validateOptimizedPixels(ctx, f, cfg.MaxWidth, cfg.MaxHeight)
	}

	if err := rewriteImage(ctx, f, path, orientation, colorMetadata, cfg, logger); err != nil {
		return filename, false, err
	}
	newFilename, err := moveToContentName(ctx, path, contentNameRequest{
		directory: filepath.Dir(path), currentName: filename, label: label, extension: filepath.Ext(filename),
	})
	if err != nil {
		return filename, true, fmt.Errorf("rename optimized output: %w", err)
	}
	if newFilename != filename {
		logger.Info("updated optimized filename", "old", filename, "new", newFilename)
	}
	return newFilename, true, nil
}

func isOptimizableJPEG(extension string) bool {
	return extension == extJPG || extension == extJPEG
}

func isOptimizableImage(extension string, cfg Config) bool {
	return isOptimizableJPEG(extension) || (extension == extPNG && cfg.OptimizePNG)
}

func readOrientationForExtension(ctx context.Context, reader io.Reader, extension string) (int, error) {
	contextual := &contextReader{ctx: ctx, reader: reader}
	if extension == extPNG {
		return ReadPNGOrientation(contextual)
	}
	return ReadOrientation(contextual)
}

func validateOptimizedPixels(ctx context.Context, file *os.File, expectedWidth, expectedHeight int) error {
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek optimized image: %w", err)
	}
	decoded, _, err := image.Decode(&contextReader{ctx: ctx, reader: file})
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
		transformRevision+"|%dx%d|q=%d|png=%t|linear=%t|smart=%t|gain=%.6g|protect=%t/%.6g|"+
			"provider=%s/%s/%.6g/%s|sharp=%.6g/%d|hdr=%t/%.6g/%.6g|museum=%t/%d|portrait=%s|profile=%s",
		cfg.MaxWidth, cfg.MaxHeight, cfg.OptimizeJPEGQuality, cfg.OptimizePNG, cfg.LinearLightResize,
		cfg.SmartCropEnabled, cfg.SmartCropMinGain, cfg.SmartCropProtection, cfg.SmartCropProtectionStrength,
		cfg.SmartCropProvider, cfg.SmartCropProviderURL, cfg.SmartCropProviderMinConfidence, cfg.SmartCropProviderTimeout,
		cfg.SharpenAmount, cfg.SharpenThreshold,
		cfg.HDRToneMap, cfg.HDRSourcePeakNits, cfg.HDRTargetPeakNits,
		cfg.MuseumModeEnabled, cfg.MuseumModeIntensity,
		cfg.PortraitMode, cfg.ColorProfilePolicy,
	)
	digest := sha256.Sum256([]byte(description))
	return hex.EncodeToString(digest[:])
}
