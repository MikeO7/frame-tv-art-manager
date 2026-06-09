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
			var localSum, localSumSq uint64

			// OPTIMIZATION: Extracting pointer fields to local variables prevents continuous pointer indirection overhead in tight loops
			pix := src.Pix
			stride := src.Stride
			for y := sy; y < ey; y++ {
				offset := y * stride
				for x := 0; x < width; x++ {
					idx := offset + x*4
					// OPTIMIZATION: Use integer math to eliminate floating-point overhead in hot path
					lumInt := uint64(pix[idx])*299 + uint64(pix[idx+1])*587 + uint64(pix[idx+2])*114
					localSum += lumInt
					localSumSq += lumInt * lumInt
				}
			}
			sums[workerIdx] = float64(localSum) / 1000.0
			sumSqs[workerIdx] = float64(localSumSq) / 1000000.0
		}(i, startY, endY)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		sum += sums[i]
		sumSq += sumSqs[i]
	}
	mean := sum / float64(width*height)
	rms := math.Sqrt(sumSq/float64(width*height) - mean*mean)
	return mean, rms
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

			// OPTIMIZATION: Extracting pointer fields to local variables prevents continuous pointer indirection overhead in tight loops
			pix := src.Pix
			stride := src.Stride
			for y := sy; y < ey; y++ {
				offset := y * stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					outR, outG, outB := processGamutPixel(pix[i], pix[i+1], pix[i+2], &lutLin)
					pix[i] = outR
					pix[i+1] = outG
					pix[i+2] = outB
				}
			}
		}(startY, endY)
	}
	wg.Wait()
}

// polishPixel limits the maximum brightness and adds paper grain noise to a pixel.
//
//nolint:funlen // complexity justified for this domain-specific path
func polishPixel(r, g, b float32, state *uint32) (uint8, uint8, uint8) {
	const maxBright = 235.0
	if r > maxBright {
		r = maxBright
	}
	if g > maxBright {
		g = maxBright
	}
	if b > maxBright {
		b = maxBright
	}

	avg := (r + g + b) * 0.33333333
	r = r*0.92 + avg*0.08
	g = g*0.92 + avg*0.08
	b = b*0.92 + avg*0.08

	*state ^= *state << 13
	*state ^= *state >> 17
	*state ^= *state << 5

	noise := (float32(*state)/float32(0xFFFFFFFF) - 0.5) * 5.0
	r += noise
	g += noise
	b += noise

	var outR, outG, outB uint8
	switch {
	case r < 0:
		outR = 0
	case r > 255:
		outR = 255
	default:
		outR = uint8(r)
	}

	switch {
	case g < 0:
		outG = 0
	case g > 255:
		outG = 255
	default:
		outG = uint8(g)
	}

	switch {
	case b < 0:
		outB = 0
	case b > 255:
		outB = 255
	default:
		outB = uint8(b)
	}

	return outR, outG, outB
}

func galleryMasterPolish(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

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

			//nolint:gosec // sy is a positive chunk offset, conversion is mathematically safe
			state := uint32(sy + 1) // Seed based on row

			// OPTIMIZATION: Extracting pointer fields to local variables prevents continuous pointer indirection overhead in tight loops
			pix := src.Pix
			stride := src.Stride

			for y := sy; y < ey; y++ {
				offset := y * stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					outR, outG, outB := polishPixel(float32(pix[i]), float32(pix[i+1]), float32(pix[i+2]), &state)
					pix[i] = outR
					pix[i+1] = outG
					pix[i+2] = outB
				}
			}
		}(startY, endY)
	}
	wg.Wait()
	return src
}
