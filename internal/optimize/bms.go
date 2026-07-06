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

	// Precompute luminance map once for all thresholds to avoid redundant
	// calculation overhead across concurrent goroutines
	pix := src.Pix
	lumMap := make([]uint8, w*h)
	for i := 0; i < w*h; i++ {
		idx := i * 4
		lum := (int(pix[idx])*299 + int(pix[idx+1])*587 + int(pix[idx+2])*114) / 1000
		lumMap[i] = uint8(lum) //nolint:gosec // weighted luminance of 0-255 channels stays within byte range
	}

	thresholds := []uint8{50, 100, 150, 200, 240}
	results := make([][]float64, len(thresholds))
	var wg sync.WaitGroup

	for i, t := range thresholds {
		wg.Add(1)
		go func(idx int, threshold uint8) {
			defer wg.Done()
			results[idx] = processBMSThreshold(lumMap, threshold, w, h)
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

type bmsState struct {
	queue   []int
	head    int
	tail    int
	boolMap []bool
	bg      []bool
	w       int
	h       int
}

func (s *bmsState) tryEnqueue(idx int) {
	if !s.boolMap[idx] && !s.bg[idx] {
		s.bg[idx] = true
		s.queue[s.tail] = idx
		s.tail++
	}
}

func (s *bmsState) seedBorders() {
	for x := 0; x < s.w; x++ {
		s.tryEnqueue(x)
		s.tryEnqueue((s.h-1)*s.w + x)
	}
	for y := 0; y < s.h; y++ {
		s.tryEnqueue(y * s.w)
		s.tryEnqueue(y*s.w + s.w - 1)
	}
}

func (s *bmsState) floodFill() {
	for s.head < s.tail {
		curr := s.queue[s.head]
		s.head++
		cx, cy := curr%s.w, curr/s.w

		// OPTIMIZATION: Reusing 1D loop variable 'curr' instead of recalculating 2D Cartesian
		// coordinates (cy*s.w + cx) avoids heavy redundant math operations in this tight loop.
		if cx-1 >= 0 {
			s.tryEnqueue(curr - 1)
		}
		if cx+1 < s.w {
			s.tryEnqueue(curr + 1)
		}
		if cy-1 >= 0 {
			s.tryEnqueue(curr - s.w)
		}
		if cy+1 < s.h {
			s.tryEnqueue(curr + s.w)
		}
	}
}

func processBMSThreshold(lumMap []uint8, t uint8, w, h int) []float64 {
	if w <= 0 || h <= 0 {
		return nil
	}
	res := make([]float64, w*h)
	boolMap := make([]bool, w*h)
	for i := 0; i < w*h; i++ {
		if lumMap[i] > t {
			boolMap[i] = true
		}
	}

	state := bmsState{
		queue:   make([]int, w*h),
		boolMap: boolMap,
		bg:      make([]bool, w*h),
		w:       w,
		h:       h,
	}

	state.seedBorders()
	state.floodFill()

	for i := 0; i < w*h; i++ {
		if state.boolMap[i] && !state.bg[i] {
			res[i] = 1.0
		}
	}
	return res
}
