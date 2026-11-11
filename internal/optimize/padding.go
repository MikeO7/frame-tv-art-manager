package optimize

import (
	"image"
	std_draw "image/draw"

	"golang.org/x/image/draw"
)

// padOnBlur centers the whole image over a blurred, muted fill of itself.
// The image is never upscaled beyond cfg.MaxUpscale.
func padOnBlur(src *image.RGBA, cfg Config) *image.RGBA {
	targetW, targetH := cfg.MaxWidth, cfg.MaxHeight
	backgroundCrop := cropRectForAspectWithProtection(src, float64(targetW)/float64(targetH), false, 0, false, 0)
	// The blur works on a background an eighth of the frame, so the crop is
	// resized straight to that size instead of to the whole frame first.
	reduced := resizeCrop(
		src, backgroundCrop, max(targetW/blurReduction, 1), max(targetH/blurReduction, 1), cfg.LinearLightResize,
	)
	bg := blurImage(reduced, 8, targetW, targetH)
	muteBackground(bg)

	srcBounds := src.Bounds()
	width, height := containedSize(srcBounds.Dx(), srcBounds.Dy(), targetW, targetH, cfg.MaxUpscale)
	scaled := resizeCrop(src, srcBounds, width, height, cfg.LinearLightResize)
	scaled = sharpenResampled(scaled, cfg, float64(height)/float64(srcBounds.Dy()))
	x, y := (targetW-width)/2, (targetH-height)/2
	std_draw.Draw(bg, image.Rect(x, y, x+width, y+height), scaled, image.Point{}, std_draw.Over)
	return bg
}

// muteBackground halves saturation and darkens by a quarter so the blurred
// fill recedes behind the sharp image instead of competing with it.
func muteBackground(img *image.RGBA) {
	pix := img.Pix
	for offset := 0; offset+3 < len(pix); offset += 4 {
		red, green, blue := float64(pix[offset]), float64(pix[offset+1]), float64(pix[offset+2])
		gray := 0.2126*red + 0.7152*green + 0.0722*blue
		pix[offset] = clampByte(0.75 * (gray + 0.5*(red-gray)))
		pix[offset+1] = clampByte(0.75 * (gray + 0.5*(green-gray)))
		pix[offset+2] = clampByte(0.75 * (gray + 0.5*(blue-gray)))
	}
}

// blurReduction is how much smaller than the frame the blurred background is
// computed: a heavy blur keeps no detail a full-size pass would add.
const blurReduction = 8

// blurImage applies a heavy blur to small, a background already reduced by
// blurReduction, with three box-blur passes and enlarges the result to
// width x height.
func blurImage(small *image.RGBA, radius, width, height int) *image.RGBA {
	tmp := image.NewRGBA(small.Bounds())
	dst := image.NewRGBA(small.Bounds())
	currentSrc := small
	for range 3 {
		boxBlurPass(currentSrc, tmp, radius, true)
		boxBlurPass(tmp, dst, radius, false)
		currentSrc = dst
	}

	blurred := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(blurred, blurred.Bounds(), dst, dst.Bounds(), draw.Src, nil)
	return blurred
}

// avgChannel returns the box-blur window mean rounded to the nearest byte.
// Each summed sample is a 0-255 channel value, so the rounded mean is
// inherently within byte range and the conversion cannot overflow.
func avgChannel(sum, windowSize int) uint8 {
	return uint8((sum + windowSize/2) / windowSize) //nolint:gosec // rounded mean of 0-255 samples stays in byte range
}

// boxBlurPass blurs every row (horizontal) or column (vertical) of src into
// dst, which has the same bounds, with an edge-clamped sliding window.
func boxBlurPass(src, dst *image.RGBA, radius int, horizontal bool) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	lines, length, lineStep, sampleStep := h, w, src.Stride, 4
	if !horizontal {
		lines, length, lineStep, sampleStep = w, h, 4, src.Stride
	}
	windowSize := 2*radius + 1
	for line := range lines {
		base := line * lineStep
		var rSum, gSum, bSum int
		for i := -radius; i <= radius; i++ {
			off := base + min(max(i, 0), length-1)*sampleStep
			rSum += int(src.Pix[off])
			gSum += int(src.Pix[off+1])
			bSum += int(src.Pix[off+2])
		}
		for i := range length {
			off := base + i*sampleStep
			dst.Pix[off] = avgChannel(rSum, windowSize)
			dst.Pix[off+1] = avgChannel(gSum, windowSize)
			dst.Pix[off+2] = avgChannel(bSum, windowSize)
			dst.Pix[off+3] = 255

			leaving := base + max(i-radius, 0)*sampleStep
			entering := base + min(i+radius+1, length-1)*sampleStep
			rSum += int(src.Pix[entering]) - int(src.Pix[leaving])
			gSum += int(src.Pix[entering+1]) - int(src.Pix[leaving+1])
			bSum += int(src.Pix[entering+2]) - int(src.Pix[leaving+2])
		}
	}
}
