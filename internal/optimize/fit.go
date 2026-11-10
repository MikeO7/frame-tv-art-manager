package optimize

import (
	"context"
	"image"
	"log/slog"
	"math"
)

const (
	padStyleMat  = "mat"
	padStyleBlur = "blur"
	portraitCrop = "crop"
)

// fitMode is how a source image fills the target frame.
type fitMode int

const (
	// fitCrop fills the frame by cropping to the target aspect ratio.
	fitCrop fitMode = iota
	// fitPad shows the whole image on a mat or blurred background.
	fitPad
)

// planFit chooses between cropping and padding. Portraits follow
// PORTRAIT_MODE. Other images are cropped only when the crop keeps enough of
// the picture and filling the frame does not need more upscaling than
// MaxUpscale; otherwise they are padded so nothing important is cut off and
// small images are not blown up into blur.
func planFit(sourceWidth, sourceHeight int, cfg Config) fitMode {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return fitCrop
	}
	if sourceHeight > sourceWidth {
		if cfg.PortraitMode == portraitCrop {
			return fitCrop
		}
		return fitPad
	}
	if croppedFraction(sourceWidth, sourceHeight, cfg.MaxWidth, cfg.MaxHeight) > float64(cfg.MaxCropPercent)/100+1e-9 {
		return fitPad
	}
	if fill := fillScale(sourceWidth, sourceHeight, cfg.MaxWidth, cfg.MaxHeight); cfg.MaxUpscale > 0 && fill > cfg.MaxUpscale+1e-9 {
		return fitPad
	}
	return fitCrop
}

// croppedFraction is the share of the source area a fill crop discards.
func croppedFraction(sourceWidth, sourceHeight, targetWidth, targetHeight int) float64 {
	sourceAspect := float64(sourceWidth) / float64(sourceHeight)
	targetAspect := float64(targetWidth) / float64(targetHeight)
	return 1 - min(sourceAspect/targetAspect, targetAspect/sourceAspect)
}

// fillScale is the scale factor at which the source covers the whole target.
func fillScale(sourceWidth, sourceHeight, targetWidth, targetHeight int) float64 {
	return max(float64(targetWidth)/float64(sourceWidth), float64(targetHeight)/float64(sourceHeight))
}

// fitImage renders src into a cfg.MaxWidth x cfg.MaxHeight frame according to
// the fit plan.
func fitImage(ctx context.Context, src *image.RGBA, cfg Config, logger *slog.Logger) *image.RGBA {
	if planFit(src.Bounds().Dx(), src.Bounds().Dy(), cfg) == fitCrop {
		cropped := centerCropConfigured(ctx, src, cfg.MaxWidth, cfg.MaxHeight, cfg, logger)
		return sharpenResampled(cropped, cfg, fillScale(src.Bounds().Dx(), src.Bounds().Dy(), cfg.MaxWidth, cfg.MaxHeight))
	}
	if cfg.PadStyle == padStyleBlur {
		return padOnBlur(src, cfg)
	}
	return renderMat([]*image.RGBA{src}, cfg)
}

// containedSize scales a source to fit inside a box without exceeding
// maxUpscale (zero means unlimited). Each side is at least one pixel.
func containedSize(sourceWidth, sourceHeight, boxWidth, boxHeight int, maxUpscale float64) (int, int) {
	scale := min(float64(boxWidth)/float64(sourceWidth), float64(boxHeight)/float64(sourceHeight))
	if maxUpscale > 0 {
		scale = min(scale, maxUpscale)
	}
	width := min(max(int(math.Round(float64(sourceWidth)*scale)), 1), boxWidth)
	height := min(max(int(math.Round(float64(sourceHeight)*scale)), 1), boxHeight)
	return width, height
}
