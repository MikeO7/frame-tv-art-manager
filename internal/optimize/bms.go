package optimize

import (
	"sync"
)

// generateBMSMap implements Boolean Map Saliency's surroundedness principle.
// It finds regions that are topologically isolated from the image borders.
// v4.0 is fully parallelized across all threshold channels.
// generateBMSMapFromLum implements Boolean Map Saliency's surroundedness principle.
// It finds regions that are topologically isolated from the image borders.
// v4.0 is fully parallelized across all threshold channels.
func generateBMSMapFromLum(lumMapInt []int, w, h int) []float64 {
	bms := make([]float64, w*h)

	lumMap := make([]uint8, w*h)
	for i := 0; i < w*h; i++ {
		lumMap[i] = uint8(lumMapInt[i] / 1000)
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
	q := s.queue
	bMap := s.boolMap
	bg := s.bg
	w := s.w

	head := s.head
	tail := s.tail

	for head < tail {
		curr := q[head]
		head++

		cx := curr % w

		if cx > 0 {
			idx := curr - 1
			if !bMap[idx] && !bg[idx] {
				bg[idx] = true
				q[tail] = idx
				tail++
			}
		}
		if cx+1 < w {
			idx := curr + 1
			if !bMap[idx] && !bg[idx] {
				bg[idx] = true
				q[tail] = idx
				tail++
			}
		}
		if curr >= w {
			idx := curr - w
			if !bMap[idx] && !bg[idx] {
				bg[idx] = true
				q[tail] = idx
				tail++
			}
		}
		if curr+w < len(bMap) {
			idx := curr + w
			if !bMap[idx] && !bg[idx] {
				bg[idx] = true
				q[tail] = idx
				tail++
			}
		}
	}
	s.head = head
	s.tail = tail
}

//nolint:gosec // uint8 conversion is safe here as max luminance is 255
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
