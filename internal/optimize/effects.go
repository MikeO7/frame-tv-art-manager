package optimize

import (
	"image"
	std_draw "image/draw"
	"sync"

	"golang.org/x/image/draw"
)

// toRGBA converts any image type to a standard RGBA image for processing.
// This also serves as a color normalization step, flattening different
// color profiles into a consistent sRGB-like space for the TV.
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
	cropRect := cropRectForAspect(src, float64(targetW)/float64(targetH), smart)

	// Single-pass high-fidelity scaling using Catmull-Rom (Bicubic).
	final := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.CatmullRom.Scale(final, final.Bounds(), src, cropRect, draw.Src, nil)
	return final
}

// cropRectForAspect selects the source rectangle that matches targetAspect,
// centered by default or saliency-aligned when smart cropping is enabled.
func cropRectForAspect(src *image.RGBA, targetAspect float64, smart bool) image.Rectangle {
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	if float64(srcW)/float64(srcH) > targetAspect {
		// Image is wider than target: crop horizontally.
		cropW := int(float64(srcH) * targetAspect)
		bestX := (srcW - cropW) / 2 // Default to center
		if smart {
			bestX = findBestDirectorCrop(src, cropW, srcH, true)
		}
		return image.Rect(bestX, 0, bestX+cropW, srcH)
	}

	// Image is taller than target: crop vertically.
	cropH := int(float64(srcW) / targetAspect)
	bestY := (srcH - cropH) / 2 // Default to center
	if smart {
		bestY = findBestDirectorCrop(src, srcW, cropH, false)
	}
	return image.Rect(0, bestY, srcW, bestY+cropH)
}

// dither applies a subtle random jitter to pixel values to break up banding in gradients.
//
//nolint:gocognit,funlen // per-pixel xorshift jitter + channel clamps kept inline across goroutine-partitioned rows to avoid call overhead
func dither(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers
	stride := src.Stride
	pix := src.Pix

	for i := 0; i < workers; i++ {
		startY := i * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()

			// Fast thread-local PRNG (Xorshift32)
			state := uint32(sy + 1) //nolint:gosec // Seed based on row

			for y := sy; y < ey; y++ {
				offset := y * stride
				for x := 0; x < width; x++ {
					i := offset + x*4

					// xorshift32
					state ^= state << 13
					state ^= state >> 17
					state ^= state << 5

					// Generate -1, 0, or 1
					jitter := int((state % 3)) - 1

					// R
					valR := int(pix[i]) + jitter
					if valR < 0 {
						valR = 0
					} else if valR > 255 {
						valR = 255
					}
					pix[i] = uint8(valR)

					// G
					valG := int(pix[i+1]) + jitter
					if valG < 0 {
						valG = 0
					} else if valG > 255 {
						valG = 255
					}
					pix[i+1] = uint8(valG)

					// B
					valB := int(pix[i+2]) + jitter
					if valB < 0 {
						valB = 0
					} else if valB > 255 {
						valB = 255
					}
					pix[i+2] = uint8(valB)
				}
			}
		}(startY, endY)
	}
	wg.Wait()
	return src
}

// sharpen applies a high-performance 3x3 sharpening kernel to the image.
//
//nolint:gocyclo,gocognit,funlen // Highly optimized, performance-critical loops are manually unrolled
func sharpen(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	width, height := bounds.Dx(), bounds.Dy()

	if width < 3 || height < 3 {
		std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)
		return dst
	}

	var wg sync.WaitGroup
	workers := 8 // Target 8 routines to map well to multi-core CPUs
	chunk := (height - 2) / workers
	if chunk == 0 {
		chunk = 1
	}

	srcStride := src.Stride
	srcPix := src.Pix
	dstStride := dst.Stride
	dstPix := dst.Pix

	for i := 0; i < workers; i++ {
		startY := 1 + i*chunk
		endY := startY + chunk
		if i == workers-1 {
			endY = height - 1
		}
		if startY >= height-1 {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()
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
	}
	wg.Wait()

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
