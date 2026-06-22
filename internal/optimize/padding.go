package optimize

import (
	"image"
	std_draw "image/draw"

	"golang.org/x/image/draw"
)

// padPortrait takes an upright portrait image and fits it centered inside a 16:9 canvas (e.g. 3840x2160)
// by scaling the photo to fill the background, heavily blurring it, and drawing the scaled upright
// photo on top in the center.
func padPortrait(src *image.RGBA, targetW, targetH int) *image.RGBA {
	// 1. Create blurred background filling the target size
	bg := centerCrop(src, targetW, targetH, false)
	bg = blurImage(bg, 8) // scale-down + box blur + scale-up method

	// 2. Scale the upright portrait image so its height matches targetH exactly, preserving aspect ratio
	srcBounds := src.Bounds()
	scaleFactor := float64(targetH) / float64(srcBounds.Dy())
	newW := int(float64(srcBounds.Dx()) * scaleFactor)
	if newW > targetW {
		newW = targetW
	}

	scaledPortrait := image.NewRGBA(image.Rect(0, 0, newW, targetH))
	draw.CatmullRom.Scale(scaledPortrait, scaledPortrait.Bounds(), src, srcBounds, draw.Src, nil)

	// 3. Draw the scaled portrait image in the center of the blurred background
	startX := (targetW - newW) / 2
	std_draw.Draw(bg, image.Rect(startX, 0, startX+newW, targetH), scaledPortrait, scaledPortrait.Bounds().Min, std_draw.Over)

	return bg
}

// blurImage applies a heavy blur by downscaling, applying multiple passes of box blur, and upscaling.
func blurImage(src *image.RGBA, radius int) *image.RGBA {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Downscale by 8x for fast blur processing and extra smoothness
	downW, downH := w/8, h/8
	if downW < 1 {
		downW = 1
	}
	if downH < 1 {
		downH = 1
	}

	small := image.NewRGBA(image.Rect(0, 0, downW, downH))
	draw.BiLinear.Scale(small, small.Bounds(), src, src.Bounds(), draw.Src, nil)

	// Perform 3 passes of box blur
	tmp := image.NewRGBA(small.Bounds())
	dst := image.NewRGBA(small.Bounds())

	currentSrc := small
	for pass := 0; pass < 3; pass++ {
		boxBlurH(currentSrc, tmp, radius)
		boxBlurV(tmp, dst, radius)
		currentSrc = dst
	}

	// Upscale back to original resolution
	blurred := image.NewRGBA(bounds)
	draw.CatmullRom.Scale(blurred, blurred.Bounds(), dst, dst.Bounds(), draw.Src, nil)

	return blurred
}

// avgChannel returns the box-blur window mean as a byte. Each summed sample is
// a 0-255 channel value, so sum/windowSize is inherently within byte range and
// the conversion cannot overflow.
func avgChannel(sum, windowSize int) uint8 {
	return uint8(sum / windowSize) //nolint:gosec // mean of 0-255 samples stays in byte range
}

//nolint:dupl // horizontal and vertical passes are structurally similar but iterate differently
func boxBlurH(src, dst *image.RGBA, radius int) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	windowSize := 2*radius + 1
	for y := 0; y < h; y++ {
		var rSum, gSum, bSum int
		// Initialize window
		for x := -radius; x <= radius; x++ {
			nx := x
			if nx < 0 {
				nx = 0
			} else if nx >= w {
				nx = w - 1
			}
			off := y*src.Stride + nx*4
			rSum += int(src.Pix[off])
			gSum += int(src.Pix[off+1])
			bSum += int(src.Pix[off+2])
		}

		for x := 0; x < w; x++ {
			offDst := y*dst.Stride + x*4
			dst.Pix[offDst] = avgChannel(rSum, windowSize)
			dst.Pix[offDst+1] = avgChannel(gSum, windowSize)
			dst.Pix[offDst+2] = avgChannel(bSum, windowSize)
			dst.Pix[offDst+3] = 255

			// Slide window
			leftX := x - radius
			if leftX < 0 {
				leftX = 0
			}
			rightX := x + radius + 1
			if rightX >= w {
				rightX = w - 1
			}

			offLeft := y*src.Stride + leftX*4
			offRight := y*src.Stride + rightX*4

			rSum = rSum - int(src.Pix[offLeft]) + int(src.Pix[offRight])
			gSum = gSum - int(src.Pix[offLeft+1]) + int(src.Pix[offRight+1])
			bSum = bSum - int(src.Pix[offLeft+2]) + int(src.Pix[offRight+2])
		}
	}
}

//nolint:dupl // horizontal and vertical passes are structurally similar but iterate differently
func boxBlurV(src, dst *image.RGBA, radius int) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	windowSize := 2*radius + 1
	for x := 0; x < w; x++ {
		var rSum, gSum, bSum int
		// Initialize window
		for y := -radius; y <= radius; y++ {
			ny := y
			if ny < 0 {
				ny = 0
			} else if ny >= h {
				ny = h - 1
			}
			off := ny*src.Stride + x*4
			rSum += int(src.Pix[off])
			gSum += int(src.Pix[off+1])
			bSum += int(src.Pix[off+2])
		}

		for y := 0; y < h; y++ {
			offDst := y*dst.Stride + x*4
			dst.Pix[offDst] = avgChannel(rSum, windowSize)
			dst.Pix[offDst+1] = avgChannel(gSum, windowSize)
			dst.Pix[offDst+2] = avgChannel(bSum, windowSize)
			dst.Pix[offDst+3] = 255

			// Slide window
			topY := y - radius
			if topY < 0 {
				topY = 0
			}
			bottomY := y + radius + 1
			if bottomY >= h {
				bottomY = h - 1
			}

			offTop := topY*src.Stride + x*4
			offBottom := bottomY*src.Stride + x*4

			rSum = rSum - int(src.Pix[offTop]) + int(src.Pix[offBottom])
			gSum = gSum - int(src.Pix[offTop+1]) + int(src.Pix[offBottom+1])
			bSum = bSum - int(src.Pix[offTop+2]) + int(src.Pix[offBottom+2])
		}
	}
}
