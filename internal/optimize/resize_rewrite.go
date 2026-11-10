package optimize

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
)

// rewriteImage decodes f, applies the configured transforms for its EXIF
// orientation and color metadata, and durably replaces path with the result.
//
//nolint:revive // ordered decode, transform, validation, and durable publication inputs stay explicit
func rewriteImage(
	ctx context.Context,
	f *os.File,
	path string,
	orientation int,
	colorData embeddedColorData,
	cfg Config,
	logger *slog.Logger,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek to start: %w", err)
	}
	img, _, err := image.Decode(&contextReader{ctx: ctx, reader: f})
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	filename := filepath.Base(path)
	logger.Info("optimizing image", "file", filename, "original_dims", fmt.Sprintf("%dx%d", img.Bounds().Dx(), img.Bounds().Dy()))
	rgba, err := transformDecoded(ctx, img, orientation, colorData, cfg, logger, filename)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close source image: %w", err)
	}

	out, err := os.CreateTemp(filepath.Dir(path), ".optimize-*.tmp")
	if err != nil {
		return fmt.Errorf("create optimized temporary file: %w", err)
	}
	defer func() { _ = os.Remove(out.Name()) }()
	if err := writeValidatedOutput(ctx, out, filepath.Ext(path), rgba, cfg); err != nil {
		return fmt.Errorf("write optimized image: %w", err)
	}
	if err := durablefs.Rename(ctx, out.Name(), path); err != nil {
		return fmt.Errorf("replace optimized image: %w", err)
	}
	return nil
}

// transformDecoded applies color normalization, orientation, the frame fit
// with its sharpening, and museum effects to a decoded image. It is the pixel half of
// rewriteImage and is shared with in-memory previews.
//
//nolint:revive // the transform inputs mirror rewriteImage exactly
func transformDecoded(
	ctx context.Context,
	img image.Image,
	orientation int,
	colorData embeddedColorData,
	cfg Config,
	logger *slog.Logger,
	filename string,
) (*image.RGBA, error) {
	rgba := toRGBA(RotateImage(normalizeColor(img, colorData, cfg, logger, filename), orientation))
	sourceW, sourceH := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if sourceW != cfg.MaxWidth || sourceH != cfg.MaxHeight {
		rgba = fitImage(ctx, rgba, cfg, logger)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if cfg.MuseumModeEnabled {
		rgba = applyMuseumMode(rgba, cfg.MuseumModeIntensity)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rgba, nil
}

// writeValidatedOutput encodes img into out as PNG for a .png extension and
// JPEG otherwise, closes it, and verifies that it decodes to the configured
// output format and dimensions. The 0o644 mode is intentional: artwork must
// stay readable over SMB/NFS shares.
func writeValidatedOutput(ctx context.Context, out *os.File, extension string, img image.Image, cfg Config) error {
	writer := &contextWriter{ctx: ctx, writer: out}
	format := formatJPEG
	var err error
	if strings.EqualFold(extension, extPNG) {
		format = formatPNG
		err = png.Encode(writer, img)
	} else {
		err = jpegenc.Encode(writer, img, &jpegenc.Options{Quality: cfg.OptimizeJPEGQuality, Subsampling: cfg.JPEGSubsampling})
	}
	if err != nil {
		_ = out.Close()
		return fmt.Errorf("encode: %w", err)
	}
	if err := errors.Join(out.Chmod(0o644), out.Sync(), out.Close()); err != nil {
		return fmt.Errorf("finalize: %w", err)
	}
	if err := validateImageOutput(ctx, out.Name(), format, cfg.MaxWidth, cfg.MaxHeight); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return nil
}
