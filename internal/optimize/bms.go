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

	// OPTIMIZATION: Precompute luminance map once instead of recalculating 5 times for each threshold.
	lumMap := make([]uint8, w*h)
	pix := src.Pix
	for i := 0; i < w*h; i++ {
		idx := i * 4
		// Extract src.Pix to a local variable to prevent pointer indirection in hot path
		lum := (int(pix[idx])*299 + int(pix[idx+1])*587 + int(pix[idx+2])*114) / 1000
		lumMap[i] = uint8(lum)
	}

	thresholds := []uint8{50, 100, 150, 200, 240}
	results := make([][]bool, len(thresholds))
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
	increment := 1.0 / float64(len(thresholds))
	for i, t := range thresholds {
		bg := results[i]
		if bg == nil {
			continue
		}
		for j := 0; j < w*h; j++ {
			if lumMap[j] > t && !bg[j] {
				bms[j] += increment
			}
		}
	}
	return bms
}

type bmsState struct {
	queue  []int
	head   int
	tail   int
	lumMap []uint8
	t      uint8
	bg     []bool
	w      int
	h      int
}

func (s *bmsState) tryEnqueue(idx int) {
	if s.lumMap[idx] <= s.t && !s.bg[idx] {
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

		if cx-1 >= 0 {
			s.tryEnqueue(cy*s.w + cx - 1)
		}
		if cx+1 < s.w {
			s.tryEnqueue(cy*s.w + cx + 1)
		}
		if cy-1 >= 0 {
			s.tryEnqueue((cy-1)*s.w + cx)
		}
		if cy+1 < s.h {
			s.tryEnqueue((cy+1)*s.w + cx)
		}
	}
}

//nolint:gosec // uint8 conversion is safe here as max luminance is 255
func processBMSThreshold(lumMap []uint8, t uint8, w, h int) []bool {
	if w <= 0 || h <= 0 {
		return nil
	}

	state := bmsState{
		queue:  make([]int, w*h),
		lumMap: lumMap,
		t:      t,
		bg:     make([]bool, w*h),
		w:      w,
		h:      h,
	}

	state.seedBorders()
	state.floodFill()

	return state.bg
}
