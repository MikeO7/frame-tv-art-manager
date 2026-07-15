package optimize

import (
	"image"
	"math"
)

// generateBMSMap implements Boolean Map Saliency's surroundedness principle
// across the three perceptual Lab channels and both threshold polarities.
//
//nolint:gocognit // the nested channel/threshold/polarity loops are the BMS algorithm's explicit dimensions
func generateBMSMap(src *image.RGBA) []float64 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	bms := make([]float64, w*h)
	if w == 0 || h == 0 {
		return bms
	}

	pix := src.Pix
	channels := [3][]uint8{make([]uint8, w*h), make([]uint8, w*h), make([]uint8, w*h)}
	for i := 0; i < w*h; i++ {
		idx := i * 4
		l, a, b := rgbToLab(pix[idx], pix[idx+1], pix[idx+2])
		channels[0][i] = clampByte(l * 2.55)
		channels[1][i] = clampByte(a + 128)
		channels[2][i] = clampByte(b + 128)
	}

	for _, channel := range channels {
		for threshold := 32; threshold < 256; threshold += 32 {
			for _, inverse := range []bool{false, true} {
				attention := processBMSThresholdDirection(channel, uint8(threshold), w, h, inverse)
				normSquared := 0.0
				for _, value := range attention {
					normSquared += value * value
				}
				if normSquared == 0 {
					continue
				}
				normalizer := 1 / math.Sqrt(normSquared)
				for i, value := range attention {
					bms[i] += value * normalizer
				}
			}
		}
	}
	return normalizeAndBlurBMS(bms, w, h)
}

func clampByte(value float64) uint8 {
	if value <= 0 {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return uint8(math.Round(value))
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
	if s.boolMap[idx] && !s.bg[idx] {
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

func processBMSThreshold(lumMap []uint8, t uint8, w, h int) []float64 {
	return processBMSThresholdDirection(lumMap, t, w, h, false)
}

func processBMSThresholdDirection(values []uint8, threshold uint8, w, h int, inverse bool) []float64 {
	if w <= 0 || h <= 0 {
		return nil
	}
	if len(values) < w*h {
		return nil
	}
	res := make([]float64, w*h)
	boolMap := make([]bool, w*h)
	for i, value := range values {
		if i >= len(boolMap) {
			break
		}
		boolMap[i] = value > threshold
		if inverse {
			boolMap[i] = !boolMap[i]
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

//nolint:gocognit // the fixed 3x3 spatial reduction is clearer kept as direct bounded loops
func normalizeAndBlurBMS(values []float64, w, h int) []float64 {
	blurred := make([]float64, len(values))
	maxValue := 0.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := 0.0
			count := 0.0
			for yy := max(0, y-1); yy <= min(h-1, y+1); yy++ {
				for xx := max(0, x-1); xx <= min(w-1, x+1); xx++ {
					sum += values[yy*w+xx]
					count++
				}
			}
			blurred[y*w+x] = sum / count
			if blurred[y*w+x] > maxValue {
				maxValue = blurred[y*w+x]
			}
		}
	}
	if maxValue > 0 {
		for i := range blurred {
			blurred[i] /= maxValue
		}
	}
	return blurred
}
