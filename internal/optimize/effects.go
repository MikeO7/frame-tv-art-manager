package optimize

import (
	"image"
	std_draw "image/draw"
	"math"
)

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

// centerCrop performs a content-aware crop and high-fidelity scale to target dimensions.
// It uses the Director's Cut Saliency Engine to identify subjects and optimize composition.
func centerCrop(src *image.RGBA, targetW, targetH int, smart bool) *image.RGBA {
	return centerCropWithOptions(src, targetW, targetH, smart, 0.03, true)
}

//nolint:revive // crop policy is kept explicit at this internal pixel-processing seam
func centerCropWithOptions(
	src *image.RGBA,
	targetW, targetH int,
	smart bool,
	minGain float64,
	linearLight bool,
) *image.RGBA {
	cropRect := cropRectForAspectWithGain(src, float64(targetW)/float64(targetH), smart, minGain)
	return resizeCrop(src, cropRect, targetW, targetH, linearLight)
}

func cropRectForAspectWithGain(src *image.RGBA, targetAspect float64, smart bool, minGain float64) image.Rectangle {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	if float64(srcW)/float64(srcH) > targetAspect {
		// Image is wider than target: crop horizontally.
		cropW := int(float64(srcH) * targetAspect)
		bestX := (srcW - cropW) / 2 // Default to center
		if smart {
			bestX = findBestDirectorCropWithGain(src, cropW, srcH, true, minGain)
		}
		return image.Rect(bestX, 0, bestX+cropW, srcH)
	}

	// Image is taller than target: crop vertically.
	cropH := int(float64(srcW) / targetAspect)
	bestY := (srcH - cropH) / 2 // Default to center
	if smart {
		bestY = findBestDirectorCropWithGain(src, srcW, cropH, false, minGain)
	}
	return image.Rect(0, bestY, srcW, bestY+cropH)
}

// sharpen applies a high-performance 3x3 sharpening kernel to the image.
func sharpen(src *image.RGBA) *image.RGBA {
	return sharpenWithWorkers(src, defaultPixelWorkers())
}

//nolint:gocyclo,gocognit,funlen // performance-critical kernel is manually unrolled across bounded row partitions
func sharpenWithWorkers(src *image.RGBA, workerLimit int) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	width, height := bounds.Dx(), bounds.Dy()

	if width < 3 || height < 3 {
		std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)
		return dst
	}

	chunk := (height - 2) / pixelPartitions
	if chunk == 0 {
		chunk = 1
	}

	srcStride := src.Stride
	srcPix := src.Pix
	dstStride := dst.Stride
	dstPix := dst.Pix

	runPixelTasks(workerLimit, func(i int) {
		startY := 1 + i*chunk
		endY := startY + chunk
		if i == pixelPartitions-1 {
			endY = height - 1
		}
		if startY >= height-1 {
			return
		}

		func(sy, ey int) {
			for y := sy; y < ey; y++ {
				srcOffset := y * srcStride
				dstOffset := y * dstStride
				srcTopOffset := (y - 1) * srcStride
				srcBottomOffset := (y + 1) * srcStride

				for x := 1; x < width-1; x++ {
					iDst := dstOffset + x*4
					iSrc := srcOffset + x*4
					iTop := srcTopOffset + x*4
					iBottom := srcBottomOffset + x*4
					iLeft := iSrc - 4
					iRight := iSrc + 4

					// R
					valR := (int(srcPix[iSrc]) * 5) - int(srcPix[iTop]) - int(srcPix[iBottom]) - int(srcPix[iLeft]) - int(srcPix[iRight])
					if valR < 0 {
						valR = 0
					} else if valR > 255 {
						valR = 255
					}
					dstPix[iDst] = uint8(valR)

					// G
					valG := (int(srcPix[iSrc+1]) * 5) - int(srcPix[iTop+1]) - int(srcPix[iBottom+1]) - int(srcPix[iLeft+1]) - int(srcPix[iRight+1])
					if valG < 0 {
						valG = 0
					} else if valG > 255 {
						valG = 255
					}
					dstPix[iDst+1] = uint8(valG)

					// B
					valB := (int(srcPix[iSrc+2]) * 5) - int(srcPix[iTop+2]) - int(srcPix[iBottom+2]) - int(srcPix[iLeft+2]) - int(srcPix[iRight+2])
					if valB < 0 {
						valB = 0
					} else if valB > 255 {
						valB = 255
					}
					dstPix[iDst+2] = uint8(valB)

					// A
					dstPix[iDst+3] = srcPix[iSrc+3]
				}
			}
		}(startY, endY)
	})

	// Copy borders
	for x := 0; x < width; x++ {
		copy(dst.Pix[0*dst.Stride+x*4:0*dst.Stride+x*4+4], src.Pix[0*src.Stride+x*4:0*src.Stride+x*4+4])
		copy(dst.Pix[(height-1)*dst.Stride+x*4:(height-1)*dst.Stride+x*4+4], src.Pix[(height-1)*src.Stride+x*4:(height-1)*src.Stride+x*4+4])
	}
	for y := 0; y < height; y++ {
		copy(dst.Pix[y*dst.Stride+0*4:y*dst.Stride+0*4+4], src.Pix[y*src.Stride+0*4:y*src.Stride+0*4+4])
		copy(dst.Pix[y*dst.Stride+(width-1)*4:y*dst.Stride+(width-1)*4+4], src.Pix[y*src.Stride+(width-1)*4:y*src.Stride+(width-1)*4+4])
	}

	return dst
}

// sharpenWithOptions applies a bounded luminance unsharp mask. The threshold
// suppresses amplification of low-level JPEG noise and the shared luminance
// delta avoids creating colored edge halos.
//
//nolint:gocognit // bounded row/channel loops keep the hot pixel path allocation-free
func sharpenWithOptions(src *image.RGBA, amount float64, threshold, workerLimit int) *image.RGBA {
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
	chunk := (height - 2 + pixelPartitions - 1) / pixelPartitions
	rgbLuma := func(pix []uint8, offset int) int {
		return int(pix[offset])*299 + int(pix[offset+1])*587 + int(pix[offset+2])*114
	}
	runPixelTasks(workerLimit, func(partition int) {
		startY := 1 + partition*chunk
		endY := min(startY+chunk, height-1)
		if startY >= endY {
			return
		}
		for y := startY; y < endY; y++ {
			for x := 1; x < width-1; x++ {
				center := y*src.Stride + x*4
				neighborLuma := (rgbLuma(src.Pix, center-src.Stride) +
					rgbLuma(src.Pix, center+src.Stride) +
					rgbLuma(src.Pix, center-4) +
					rgbLuma(src.Pix, center+4)) / 4
				delta := float64(rgbLuma(src.Pix, center)-neighborLuma) / 1000
				if math.Abs(delta) < float64(threshold) {
					continue
				}
				for channel := 0; channel < 3; channel++ {
					value := float64(src.Pix[center+channel]) + amount*delta
					dst.Pix[center+channel] = clampByte(value)
				}
			}
		}
	})
	return dst
}
