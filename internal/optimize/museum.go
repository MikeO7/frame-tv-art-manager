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

// applyMuseumMode orchestrates a suite of visual filters to simulate physical
// artwork. Contrast is pulled toward a rich gallery RMS target in place.
func applyMuseumMode(src *image.RGBA, intensity int) *image.RGBA {
	const targetRMS = 58.0
	applyContrastAndGamut(src, min(max(1.0+(targetRMS-calculateRMSContrast(src))/100.0, 0.85), 1.15))
	if intensity > 0 {
		src = applyCanvasTexture(src, min(intensity, 10))
	}
	return galleryMasterPolish(src)
}

func calculateRMSContrast(src *image.RGBA) float64 {
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	var sums, sumSqs [pixelPartitions]float64
	runPixelRows(0, height, func(partition, startY, endY int) {
		pix, stride := src.Pix, src.Stride
		var localSum, localSumSq uint64
		for y := startY; y < endY; y++ {
			offset := y * stride
			for x := range width {
				idx := offset + x*4
				// Integer luminance keeps the hot loop free of floating point.
				lumInt := uint64(pix[idx])*299 + uint64(pix[idx+1])*587 + uint64(pix[idx+2])*114
				localSum += lumInt
				localSumSq += lumInt * lumInt
			}
		}
		sums[partition] = float64(localSum) / 1000.0
		sumSqs[partition] = float64(localSumSq) / 1000000.0
	})
	var sum, sumSq float64
	for i := range pixelPartitions {
		sum += sums[i]
		sumSq += sumSqs[i]
	}
	mean := sum / float64(width*height)
	return math.Sqrt(sumSq/float64(width*height) - mean*mean)
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

	return lutSrgb[museumLUTIndex(rLin)], lutSrgb[museumLUTIndex(gLin)], lutSrgb[museumLUTIndex(bLin)]
}

// museumLUTIndex rounds a linear value to the nearest lutSrgb entry.
func museumLUTIndex(linear float64) int {
	scaled := linear * srgbLUTMaxIdx
	switch {
	case scaled >= srgbLUTMaxIdx:
		return srgbLUTMaxIdx
	case scaled > 0:
		return int(scaled + 0.5)
	default:
		return 0
	}
}

func applyContrastAndGamut(src *image.RGBA, contrastGamma float64) {
	width, height := src.Bounds().Dx(), src.Bounds().Dy()

	decode := &srgbLookup().decode
	var lutLin [256]float64
	for i := range lutLin {
		lutLin[i] = math.Pow(decode[i], contrastGamma)
	}
	lutSrgbOnce.Do(func() {
		for i := range lutSrgb {
			lutSrgb[i] = roundToByte(srgbEncode(float64(i)/srgbLUTMaxIdx) * 255.0)
		}
	})

	runPixelRows(0, height, func(_, startY, endY int) {
		pix, stride := src.Pix, src.Stride
		for y := startY; y < endY; y++ {
			offset := y * stride
			for x := range width {
				i := offset + x*4
				pix[i], pix[i+1], pix[i+2] = processGamutPixel(pix[i], pix[i+1], pix[i+2], &lutLin)
			}
		}
	})
}

// roundToByte rounds half up in the value's own precision and saturates to
// the byte range.
func roundToByte[F float32 | float64](value F) uint8 {
	switch {
	case value < 0:
		return 0
	case value > 255:
		return 255
	default:
		return uint8(value + 0.5)
	}
}
