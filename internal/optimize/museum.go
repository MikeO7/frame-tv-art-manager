package optimize

import (
	"image"
	"math"
	"sync"
)

// sRGB encode lookup table precision: a 14-bit (16384-entry) table trades a
// small fixed memory cost for a per-pixel gamma encode that avoids math.Pow.
const (
	srgbLUTSize   = 16384
	srgbLUTMaxIdx = srgbLUTSize - 1
)

//nolint:gochecknoglobals // global read-only lookup table for performance-critical sRGB calculations
var (
	lutSrgb     [srgbLUTSize]uint8
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
	workers := pixelWorkers
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
			calculateRMSWorker(rmsWork{
				src:       src,
				width:     width,
				sy:        sy,
				ey:        ey,
				workerIdx: workerIdx,
				sums:      sums,
				sumSqs:    sumSqs,
				wg:        &wg,
			})
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

// rmsWork bundles the inputs for a single goroutine-partitioned slice of the
// RMS-contrast luminance accumulation.
type rmsWork struct {
	src          *image.RGBA
	width        int
	sy, ey       int
	workerIdx    int
	sums, sumSqs []float64
	wg           *sync.WaitGroup
}

func calculateRMSWorker(work rmsWork) {
	src := work.src
	width := work.width
	sy, ey := work.sy, work.ey
	workerIdx := work.workerIdx
	sums, sumSqs := work.sums, work.sumSqs
	wg := work.wg

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

	fR := rLin * srgbLUTMaxIdx
	idxR := 0
	if fR >= srgbLUTMaxIdx {
		idxR = srgbLUTMaxIdx
	} else if fR > 0 {
		idxR = int(fR)
	}

	fG := gLin * srgbLUTMaxIdx
	idxG := 0
	if fG >= srgbLUTMaxIdx {
		idxG = srgbLUTMaxIdx
	} else if fG > 0 {
		idxG = int(fG)
	}

	fB := bLin * srgbLUTMaxIdx
	idxB := 0
	if fB >= srgbLUTMaxIdx {
		idxB = srgbLUTMaxIdx
	} else if fB > 0 {
		idxB = int(fB)
	}

	return lutSrgb[idxR], lutSrgb[idxG], lutSrgb[idxB]
}

func applyContrastAndGamut(src *image.RGBA, contrastGamma float64) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var lutLin [256]float64
	for i := 0; i < 256; i++ {
		lutLin[i] = math.Pow(float64(i)/255.0, 2.2*contrastGamma)
	}

	lutSrgbOnce.Do(func() {
		for i := 0; i < srgbLUTSize; i++ {
			val := math.Pow(float64(i)/srgbLUTMaxIdx, 0.454545454545) * 255.0
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
	workers := pixelWorkers
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
