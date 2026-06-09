package optimize

import (
	"image"
	"sync"
)

// generateBMSMap implements Boolean Map Saliency's surroundedness principle.
// It finds regions that are topologically isolated from the image borders.
// v4.0 is fully parallelized across all threshold channels.
func generateBMSMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	bms := make([]float64, w*h)

	thresholds := []uint8{50, 100, 150, 200, 240}
	results := make([][]float64, len(thresholds))
	var wg sync.WaitGroup

	for i, t := range thresholds {
		wg.Add(1)
		go func(idx int, threshold uint8) {
			defer wg.Done()
			results[idx] = processBMSThreshold(src, threshold, w, h)
		}(i, t)
	}
	wg.Wait()

	// Aggregate parallel results
	for _, res := range results {
		for i := 0; i < w*h; i++ {
			bms[i] += res[i] / float64(len(thresholds))
		}
	}
	return bms
}

func enqueueBMS(idx int, boolMap, bg []bool, queue []int, tail *int) {
	if !boolMap[idx] && !bg[idx] {
		bg[idx] = true
		queue[*tail] = idx
		*tail++
	}
}

//nolint:gocognit,gocyclo,gosec,funlen // complexity justified for this domain-specific path; uint8 conversion is safe here as max luminance is 255
func processBMSThreshold(src *image.RGBA, t uint8, w, h int) []float64 {
	if w <= 0 || h <= 0 {
		return nil
	}
	res := make([]float64, w*h)
	boolMap := make([]bool, w*h)
	pix := src.Pix
	for i := 0; i < w*h; i++ {
		idx := i * 4
		// OPTIMIZATION: Replacing floating point multiplication with integer math for much faster luminance calculation
		// Extract src.Pix to a local variable to prevent pointer indirection in hot path
		lum := (int(pix[idx])*299 + int(pix[idx+1])*587 + int(pix[idx+2])*114) / 1000
		if uint8(lum) > t {
			boolMap[i] = true
		}
	}

	bg := make([]bool, w*h)
	// OPTIMIZATION: Fixed array avoids allocs and inlined pushes
	queue := make([]int, w*h)
	head := 0
	tail := 0

	for x := 0; x < w; x++ {
		enqueueBMS(x, boolMap, bg, queue, &tail)
		enqueueBMS((h-1)*w+x, boolMap, bg, queue, &tail)
	}
	for y := 0; y < h; y++ {
		enqueueBMS(y*w, boolMap, bg, queue, &tail)
		enqueueBMS(y*w+w-1, boolMap, bg, queue, &tail)
	}

	for head < tail {
		curr := queue[head]
		head++
		cx, cy := curr%w, curr/w

		if cx-1 >= 0 {
			enqueueBMS(cy*w+cx-1, boolMap, bg, queue, &tail)
		}
		if cx+1 < w {
			enqueueBMS(cy*w+cx+1, boolMap, bg, queue, &tail)
		}
		if cy-1 >= 0 {
			enqueueBMS((cy-1)*w+cx, boolMap, bg, queue, &tail)
		}
		if cy+1 < h {
			enqueueBMS((cy+1)*w+cx, boolMap, bg, queue, &tail)
		}
	}

	for i := 0; i < w*h; i++ {
		if boolMap[i] && !bg[i] {
			res[i] = 1.0
		}
	}
	return res
}
