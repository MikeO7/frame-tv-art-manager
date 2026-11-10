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
		cropW := max(int(float64(srcH)*targetAspect), 1)
		bestX := (srcW - cropW) / 2 // Default to center
		if smart {
			bestX = findBestDirectorCropWithProtection(src, cropW, srcH, true, minGain, protect, protectionStrength)
		}
		return image.Rect(bestX, 0, bestX+cropW, srcH)
	}

	// Image is taller than target: crop vertically.
	cropH := max(int(float64(srcW)/targetAspect), 1)
	bestY := (srcH - cropH) / 2 // Default to center
	if smart {
		bestY = findBestDirectorCropWithProtection(src, srcW, cropH, false, minGain, protect, protectionStrength)
	}
	return image.Rect(0, bestY, srcW, bestY+cropH)
}

// sharpen applies contrast-adaptive sharpening (after AMD FidelityFX CAS) to
// luminance. Each pixel's strength adapts to the headroom left by its local
// minimum and maximum, and the result is clamped to that local range, so
// edges gain contrast without halos and flat areas such as mats are untouched.
// amount 0 disables it and values of 1 or more are full strength. threshold
// skips neighborhoods whose 8-bit luminance range is smaller than it, which
// keeps JPEG noise from being amplified. Only opaque pixels change.
func sharpen(src *image.RGBA, amount float64, threshold int) *image.RGBA {
	if amount <= 0 {
		return src
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 3 || height < 3 {
		return src
	}
	luma := lumaPlane(src)
	dst := image.NewRGBA(bounds)
	std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)
	peak := float32(-1 / (8 - 3*min(amount, 1)))
	minimumRange := float32(threshold) / 255
	runPixelRows(1, height-1, func(_, startY, endY int) {
		for y := startY; y < endY; y++ {
			for x := 1; x < width-1; x++ {
				offset := y*src.Stride + x*4
				if src.Pix[offset+3] != 255 {
					continue
				}
				index := y*width + x
				delta := casDelta(luma[index], luma[index-width], luma[index+width], luma[index-1], luma[index+1], peak, minimumRange)
				if delta == 0 {
					continue
				}
				for channel := range 3 {
					dst.Pix[offset+channel] = clampByte(float64(src.Pix[offset+channel]) + delta)
				}
			}
		}
	})
	return dst
}

// lumaPlane returns encoded Rec. 709 luminance in 0..1 for every pixel.
func lumaPlane(src *image.RGBA) []float32 {
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	luma := make([]float32, width*height)
	for y := range height {
		row := src.Pix[y*src.Stride:]
		for x := range width {
			luma[y*width+x] = (0.2126*float32(row[x*4]) + 0.7152*float32(row[x*4+1]) + 0.0722*float32(row[x*4+2])) / 255
		}
	}
	return luma
}

// casDelta returns the 8-bit luminance change contrast-adaptive sharpening
// makes to center given its four neighbors, or zero when the neighborhood's
// range is below minimumRange.
//
//nolint:revive // per-pixel hot path: the cross neighborhood is passed flat
func casDelta(center, north, south, west, east, peak, minimumRange float32) float64 {
	low := min(center, north, south, west, east)
	high := max(center, north, south, west, east)
	if high-low < minimumRange || high <= 0 {
		return 0
	}
	headroom := min(low, 1-high) / high
	weight := float32(math.Sqrt(float64(min(max(headroom, 0), 1)))) * peak
	sharpened := (weight*(north+south+west+east) + center) / (1 + 4*weight)
	return float64(min(max(sharpened, low), high)-center) * 255
}

// scaleAwareSharpenAmount treats the configured amount as the 1:1 baseline.
// Downscales receive a modest bounded lift to restore detail lost in
// resampling. Mild upscales receive less, and upscales beyond 1.5x none:
// they contain no new detail, so sharpening only exaggerates interpolation
// and compression artifacts.
func scaleAwareSharpenAmount(amount float64, sourceWidth, sourceHeight, targetWidth, targetHeight int) float64 {
	if amount <= 0 || sourceWidth <= 0 || sourceHeight <= 0 || targetWidth <= 0 || targetHeight <= 0 {
		return 0
	}
	scale := max(
		float64(targetWidth)/float64(sourceWidth),
		float64(targetHeight)/float64(sourceHeight),
	)
	factor := 1.0
	switch {
	case scale < 1:
		factor = min(1.5, 1+0.25*math.Log2(1/scale))
	case scale > 1.5:
		return 0
	case scale > 1:
		factor = 1 / math.Sqrt(scale)
	}
	return min(amount*factor, 2)
}
