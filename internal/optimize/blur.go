package optimize

import (
	"image"
	"math"
	"sync"
)

// GaussianBlur applies a high-quality Gaussian blur to the image with the given sigma.
// This implementation uses direct pixel slice access and parallelization for maximum performance.
func GaussianBlur(src *image.RGBA, sigma float64) *image.RGBA {
	if sigma <= 0 {
		return src
	}

	radius := int(math.Ceil(sigma * 3.0))
	kernel := make([]float64, radius+1)
	for i := 0; i <= radius; i++ {
		kernel[i] = math.Exp(-(float64(i * i)) / (2 * sigma * sigma))
	}

	// Normalize kernel weights
	var sum float64
	for i := -radius; i <= radius; i++ {
		sum += kernel[abs(i)]
	}
	for i := range kernel {
		kernel[i] /= sum
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// 1. Horizontal pass
	tmp := image.NewRGBA(bounds)
	applyHorizontalPass(src, tmp, kernel, radius, width, height)

	// 2. Vertical pass
	dst := image.NewRGBA(bounds)
	applyVerticalPass(tmp, dst, kernel, radius, width, height)

	return dst
}

//nolint:dupl // Separable horizontal pass
func applyHorizontalPass(src, dst *image.RGBA, kernel []float64, radius, width, height int) {
	var wg sync.WaitGroup
	numWorkers := 8
	rowsPerWorker := height / numWorkers
	if rowsPerWorker == 0 {
		rowsPerWorker = 1
	}
	for w := 0; w < numWorkers; w++ {
		startY, endY := w*rowsPerWorker, (w+1)*rowsPerWorker
		if w == numWorkers-1 {
			endY = height
		}
		if startY >= height {
			break
		}
		wg.Add(1)
		go func(sY, eY int) {
			defer wg.Done()
			for y := sY; y < eY; y++ {
				for x := 0; x < width; x++ {
					var r, g, b, a float64
					for i := -radius; i <= radius; i++ {
						ix := x + i
						if ix < 0 {
							ix = 0
						} else if ix >= width {
							ix = width - 1
						}
						weight := kernel[abs(i)]
						idx := y*src.Stride + ix*4
						r += float64(src.Pix[idx]) * weight
						g += float64(src.Pix[idx+1]) * weight
						b += float64(src.Pix[idx+2]) * weight
						a += float64(src.Pix[idx+3]) * weight
					}
					idx := y*dst.Stride + x*4
					dst.Pix[idx], dst.Pix[idx+1], dst.Pix[idx+2], dst.Pix[idx+3] = uint8(r), uint8(g), uint8(b), uint8(a)
				}
			}
		}(startY, endY)
	}
	wg.Wait()
}

//nolint:dupl // Separable vertical pass
func applyVerticalPass(src, dst *image.RGBA, kernel []float64, radius, width, height int) {
	var wg sync.WaitGroup
	numWorkers := 8
	colsPerWorker := width / numWorkers
	if colsPerWorker == 0 {
		colsPerWorker = 1
	}
	for w := 0; w < numWorkers; w++ {
		startX, endX := w*colsPerWorker, (w+1)*colsPerWorker
		if w == numWorkers-1 {
			endX = width
		}
		if startX >= width {
			break
		}
		wg.Add(1)
		go func(sX, eX int) {
			defer wg.Done()
			for x := sX; x < eX; x++ {
				for y := 0; y < height; y++ {
					var r, g, b, a float64
					for i := -radius; i <= radius; i++ {
						iy := y + i
						if iy < 0 {
							iy = 0
						} else if iy >= height {
							iy = height - 1
						}
						weight := kernel[abs(i)]
						idx := iy*src.Stride + x*4
						r += float64(src.Pix[idx]) * weight
						g += float64(src.Pix[idx+1]) * weight
						b += float64(src.Pix[idx+2]) * weight
						a += float64(src.Pix[idx+3]) * weight
					}
					idx := y*dst.Stride + x*4
					dst.Pix[idx], dst.Pix[idx+1], dst.Pix[idx+2], dst.Pix[idx+3] = uint8(r), uint8(g), uint8(b), uint8(a)
				}
			}
		}(startX, endX)
	}
	wg.Wait()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
