package optimize

import (
	"context"
	"image"
	std_draw "image/draw"
	"log/slog"
	"math"
)

//nolint:revive // rendering context, source, target, policy, and logger are orthogonal inputs
func centerCropConfigured(ctx context.Context, src *image.RGBA, targetW, targetH int, cfg Config, logger *slog.Logger) *image.RGBA {
	cropRect := cropRectWithPolicy(ctx, src, targetW, targetH, cfg, logger)
	return resizeCrop(src, cropRect, targetW, targetH, cfg.LinearLightResize)
}

// toRGBA converts decoded samples to a standard RGBA buffer. Go's standard
// decoders do not apply embedded ICC transforms, so callers must enforce the
// configured profile policy before treating samples as sRGB.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	std_draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, std_draw.Src)
	return rgba
}

//nolint:revive // explicit crop policy values keep this allocation-free scoring seam small
func cropRectForAspectWithProtection(
	src *image.RGBA,
	targetAspect float64,
	smart bool,
	minGain float64,
	protect bool,
	protectionStrength float64,
) image.Rectangle {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	if float64(srcW)/float64(srcH) > targetAspect {
		// Image is wider than target: crop horizontally.
		cropW := int(float64(srcH) * targetAspect)
		bestX := (srcW - cropW) / 2 // Default to center
		if smart {
			bestX = findBestDirectorCropWithProtection(src, cropW, srcH, true, minGain, protect, protectionStrength)
		}
		return image.Rect(bestX, 0, bestX+cropW, srcH)
	}

	// Image is taller than target: crop vertically.
	cropH := int(float64(srcW) / targetAspect)
	bestY := (srcH - cropH) / 2 // Default to center
	if smart {
		bestY = findBestDirectorCropWithProtection(src, srcW, cropH, false, minGain, protect, protectionStrength)
	}
	return image.Rect(0, bestY, srcW, bestY+cropH)
}

// sharpen applies a bounded luminance unsharp mask. The threshold suppresses
// amplification of low-level JPEG noise and the shared luminance delta avoids
// creating colored edge halos.
func sharpen(src *image.RGBA, amount float64, threshold int) *image.RGBA {
	if amount <= 0 {
		return src
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 3 || height < 3 {
		return src
	}
	dst := image.NewRGBA(bounds)
	std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)
	rgbLuma := func(offset int) int {
		return int(src.Pix[offset])*299 + int(src.Pix[offset+1])*587 + int(src.Pix[offset+2])*114
	}
	runPixelRows(1, height-1, func(_, startY, endY int) {
		for y := startY; y < endY; y++ {
			for x := 1; x < width-1; x++ {
				center := y*src.Stride + x*4
				neighborLuma := (rgbLuma(center-src.Stride) + rgbLuma(center+src.Stride) +
					rgbLuma(center-4) + rgbLuma(center+4)) / 4
				delta := float64(rgbLuma(center)-neighborLuma) / 1000
				if math.Abs(delta) < float64(threshold) {
					continue
				}
				for channel := range 3 {
					dst.Pix[center+channel] = clampByte(float64(src.Pix[center+channel]) + amount*delta)
				}
			}
		}
	})
	return dst
}

// scaleAwareSharpenAmount treats the configured amount as the 1:1 baseline.
// Downscales receive a modest bounded lift to restore detail lost in
// resampling; upscales receive less sharpening because they contain no new
// source detail and are more prone to halos.
func scaleAwareSharpenAmount(amount float64, sourceWidth, sourceHeight, targetWidth, targetHeight int) float64 {
	if amount <= 0 || sourceWidth <= 0 || sourceHeight <= 0 || targetWidth <= 0 || targetHeight <= 0 {
		return 0
	}
	scale := max(
		float64(targetWidth)/float64(sourceWidth),
		float64(targetHeight)/float64(sourceHeight),
	)
	factor := 1.0
	if scale < 1 {
		factor = min(1.5, 1+0.25*math.Log2(1/scale))
	} else if scale > 1 {
		factor = max(0.5, 1/math.Sqrt(scale))
	}
	return min(amount*factor, 2)
}
