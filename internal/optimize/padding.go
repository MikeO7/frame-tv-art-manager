package optimize

import (
	"image"
	std_draw "image/draw"

	"golang.org/x/image/draw"
)

func padPortraitWithOptions(src *image.RGBA, targetW, targetH int, linearLight bool) *image.RGBA {
	// Blur a centered target-aspect crop to fill the background.
	backgroundCrop := cropRectForAspectWithProtection(src, float64(targetW)/float64(targetH), false, 0, false, 0)
	bg := blurImage(resizeCrop(src, backgroundCrop, targetW, targetH, linearLight), 8)

	// Scale the upright portrait to the target height, preserving aspect ratio,
	// and draw it centered over the background.
	srcBounds := src.Bounds()
	newW := min(int(float64(srcBounds.Dx())*(float64(targetH)/float64(srcBounds.Dy()))), targetW)
	scaledPortrait := resizeCrop(src, srcBounds, newW, targetH, linearLight)
	startX := (targetW - newW) / 2
	std_draw.Draw(bg, image.Rect(startX, 0, startX+newW, targetH), scaledPortrait, scaledPortrait.Bounds().Min, std_draw.Over)
	return bg
}

// blurImage applies a heavy blur by downscaling 8x, running three box-blur
// passes, and upscaling back to the original resolution.
func blurImage(src *image.RGBA, radius int) *image.RGBA {
	bounds := src.Bounds()
	small := image.NewRGBA(image.Rect(0, 0, max(bounds.Dx()/8, 1), max(bounds.Dy()/8, 1)))
	draw.BiLinear.Scale(small, small.Bounds(), src, src.Bounds(), draw.Src, nil)

	tmp := image.NewRGBA(small.Bounds())
	dst := image.NewRGBA(small.Bounds())
	currentSrc := small
	for range 3 {
		boxBlurPass(currentSrc, tmp, radius, true)
		boxBlurPass(tmp, dst, radius, false)
		currentSrc = dst
	}

	blurred := image.NewRGBA(bounds)
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
