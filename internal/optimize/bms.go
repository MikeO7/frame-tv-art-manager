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

func processBMSThreshold(src *image.RGBA, t uint8, w, h int) []float64 {
	res := make([]float64, w*h)
	boolMap := make([]bool, w*h)
	for i := 0; i < w*h; i++ {
		idx := i * 4
		lum := 0.299*float64(src.Pix[idx]) + 0.587*float64(src.Pix[idx+1]) + 0.114*float64(src.Pix[idx+2])
		if uint8(lum) > t {
			boolMap[i] = true
		}
	}

	bg := make([]bool, w*h)
	queue := make([]int, 0, w*h)
	for x := 0; x < w; x++ {
		queue = checkAndPush(boolMap, bg, queue, x, 0, w, h)
		queue = checkAndPush(boolMap, bg, queue, x, h-1, w, h)
	}
	for y := 0; y < h; y++ {
		queue = checkAndPush(boolMap, bg, queue, 0, y, w, h)
		queue = checkAndPush(boolMap, bg, queue, w-1, y, w, h)
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		cx, cy := curr%w, curr/w
		queue = checkAndPush(boolMap, bg, queue, cx-1, cy, w, h)
		queue = checkAndPush(boolMap, bg, queue, cx+1, cy, w, h)
		queue = checkAndPush(boolMap, bg, queue, cx, cy-1, w, h)
		queue = checkAndPush(boolMap, bg, queue, cx, cy+1, w, h)
	}

	for i := 0; i < w*h; i++ {
		if boolMap[i] && !bg[i] {
			res[i] = 1.0
		}
	}
	return res
}

func checkAndPush(boolMap, bg []bool, queue []int, x, y, w, h int) []int {
	if x < 0 || x >= w || y < 0 || y >= h {
		return queue
	}
	idx := y*w + x
	if !boolMap[idx] && !bg[idx] {
		bg[idx] = true
		return append(queue, idx)
	}
	return queue
}
