package optimize

import (
	"image"
	"math"
	"sync"
)

//nolint:gochecknoglobals // global read-only lookup table for performance-critical sRGB calculations
var (
	lutSrgb     [16384]uint8
	lutSrgbOnce sync.Once
)

// applyMuseumMode orchestrates a suite of visual filters to simulate physical artwork.
func applyMuseumMode(src *image.RGBA, intensity int) *image.RGBA {
	if intensity > 10 {
		intensity = 10
	}
	if intensity < 0 {
		intensity = 0
	}

	img := unifyCollection(src)

	if intensity > 0 {
		img = applyCanvasTexture(img, intensity)
	}

	img = galleryMasterPolish(img)

	return img
}

func unifyCollection(src *image.RGBA) *image.RGBA {
	_, rms := calculateRMSContrast(src)

	// Target Gallery RMS (Rich Contrast)
	const targetRMS = 58.0
	contrastGamma := 1.0 + (rms-targetRMS)/100.0
	if contrastGamma < 0.85 {
		contrastGamma = 0.85
	}
	if contrastGamma > 1.15 {
		contrastGamma = 1.15
	}

	applyContrastAndGamut(src, contrastGamma)
	return src
}

func calculateRMSContrast(src *image.RGBA) (float64, float64) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var sumSq, sum float64

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers
	sums := make([]float64, workers)
	sumSqs := make([]float64, workers)

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
		go func(workerIdx, sy, ey int) {
			defer wg.Done()
			sums[workerIdx], sumSqs[workerIdx] = accumulateLuminance(src, width, sy, ey)
		}(i, startY, endY)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		sum += sums[i]
		sumSq += sumSqs[i]
	}

	// scale down the integer-inflated totals outside the loop to save massive division overhead
	sum /= 1000.0
	sumSq /= 1000000.0

	mean := sum / float64(width*height)
	valSq := sumSq/float64(width*height) - mean*mean
	if valSq < 0 {
		valSq = 0
	}
	rms := math.Sqrt(valSq)
	return mean, rms
}

func accumulateLuminance(src *image.RGBA, width, sy, ey int) (float64, float64) {
	var localSum, localSumSq uint64
	for y := sy; y < ey; y++ {
		offset := y * src.Stride
		for x := 0; x < width; x++ {
			idx := offset + x*4
			// using pure integer math drastically improves per-pixel execution speed over float multiplication.
			// max uint64 safely handles resolutions up to ~280 Megapixels before localSumSq overflow.
			lumInt := uint64(299*uint32(src.Pix[idx]) + 587*uint32(src.Pix[idx+1]) + 114*uint32(src.Pix[idx+2]))
			localSum += lumInt
			localSumSq += lumInt * lumInt
		}
	}
	return float64(localSum), float64(localSumSq)
}

// processGamutPixel applies the gamma contrast and pigment gamut compression to a pixel.
func processGamutPixel(r, g, b uint8, lutLin *[256]float64) (uint8, uint8, uint8) {
	rLin := lutLin[r]
	gLin := lutLin[g]
	bLin := lutLin[b]

	avg := (rLin + gLin + bLin) * 0.333333333
	rLin = rLin*0.97 + avg*0.03
	gLin = gLin*0.97 + avg*0.03
	bLin = bLin*0.97 + avg*0.03

	fR := rLin * 16383.0
	idxR := 0
	if fR >= 16383.0 {
		idxR = 16383
	} else if fR > 0 {
		idxR = int(fR)
	}

	fG := gLin * 16383.0
	idxG := 0
	if fG >= 16383.0 {
		idxG = 16383
	} else if fG > 0 {
		idxG = int(fG)
	}

	fB := bLin * 16383.0
	idxB := 0
	if fB >= 16383.0 {
		idxB = 16383
	} else if fB > 0 {
		idxB = int(fB)
	}

	return lutSrgb[idxR], lutSrgb[idxG], lutSrgb[idxB]
}

//nolint:gocognit // complexity justified for this domain-specific path
func applyContrastAndGamut(src *image.RGBA, contrastGamma float64) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var lutLin [256]float64
	for i := 0; i < 256; i++ {
		lutLin[i] = math.Pow(float64(i)/255.0, 2.2*contrastGamma)
	}

	lutSrgbOnce.Do(func() {
		for i := 0; i < 16384; i++ {
			val := math.Pow(float64(i)/16383.0, 0.454545454545) * 255.0
			switch {
			case val < 0:
				lutSrgb[i] = 0
			case val > 255:
				lutSrgb[i] = 255
			default:
				lutSrgb[i] = uint8(val)
			}
		}
	})

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := j * chunk
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
			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					outR, outG, outB := processGamutPixel(src.Pix[i], src.Pix[i+1], src.Pix[i+2], &lutLin)
					src.Pix[i] = outR
					src.Pix[i+1] = outG
					src.Pix[i+2] = outB
				}
			}
		}(startY, endY)
	}
	wg.Wait()
}

// polishPixel limits the maximum brightness and adds paper grain noise to a pixel.
