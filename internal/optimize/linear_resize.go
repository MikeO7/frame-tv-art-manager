package optimize

import (
	"image"
	"math"
	"sync"
	"sync/atomic"

	"golang.org/x/image/draw"
)

// resampleBandBudget bounds each worker's horizontally filtered intermediate
// rows. Output rows are processed in bands sized to this budget, so the
// linear-light working set no longer scales with the source height.
const resampleBandBudget = 8 << 20

// resampleChannels is the float32 premultiplied linear RGBA sample width.
const resampleChannels = 4

func resizeCrop(src *image.RGBA, crop image.Rectangle, targetWidth, targetHeight int, linearLight bool) *image.RGBA {
	if !linearLight {
		output := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		draw.CatmullRom.Scale(output, output.Bounds(), src, crop, draw.Src, nil)
		return output
	}
	return resampleLinear(src, crop, targetWidth, targetHeight, defaultPixelWorkers())
}

// linearResample is one separable Catmull-Rom resize performed in
// premultiplied linear light with float32 accumulation.
type linearResample struct {
	src         *image.RGBA
	crop        image.Rectangle
	horizontal  resampleAxis
	vertical    resampleAxis
	destination *image.RGBA
	tables      *srgbTables
	bandRows    int
}

// resampleLinear resizes crop to targetWidth x targetHeight. Bands of output
// rows are distributed over at most workers goroutines; every output row is
// computed from the same taps in the same order, so the result does not depend
// on the worker count.
func resampleLinear(src *image.RGBA, crop image.Rectangle, targetWidth, targetHeight, workers int) *image.RGBA {
	job := linearResample{
		src:         src,
		crop:        crop,
		horizontal:  newResampleAxis(crop.Dx(), targetWidth),
		vertical:    newResampleAxis(crop.Dy(), targetHeight),
		destination: image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight)),
		tables:      srgbLookup(),
	}
	job.bandRows = job.outputRowsPerBand()
	bands := (targetHeight + job.bandRows - 1) / job.bandRows
	var next atomic.Int64
	var group sync.WaitGroup
	for range min(max(workers, 1), bands) {
		group.Go(func() {
			var scratch resampleScratch
			for {
				band := int(next.Add(1) - 1)
				if band >= bands {
					return
				}
				firstRow := band * job.bandRows
				job.renderBand(firstRow, min(firstRow+job.bandRows, targetHeight), &scratch)
			}
		})
	}
	group.Wait()
	return job.destination
}

type resampleScratch struct {
	rows        []float32
	accumulator []float32
}

// outputRowsPerBand sizes a band so its intermediate rows fit the budget. A
// band always holds at least one output row, whose taps are bounded by the
// vertical kernel support.
func (job *linearResample) outputRowsPerBand() int {
	rowBytes := job.destination.Rect.Dx() * resampleChannels * 4
	budgetRows := resampleBandBudget / rowBytes
	scale := float64(job.crop.Dy()) / float64(job.destination.Rect.Dy())
	rows := int(float64(budgetRows-job.vertical.maxTaps()) / max(scale, 1e-9))
	return min(max(rows, 1), job.destination.Rect.Dy())
}

func (job *linearResample) renderBand(firstRow, endRow int, scratch *resampleScratch) {
	width := job.destination.Rect.Dx()
	rowSamples := width * resampleChannels
	firstSource, endSource := job.vertical.sourceSpan(firstRow, endRow)
	needed := (endSource - firstSource) * rowSamples
	if cap(scratch.rows) < needed {
		scratch.rows = make([]float32, needed)
	}
	scratch.rows = scratch.rows[:needed]
	if cap(scratch.accumulator) < rowSamples {
		scratch.accumulator = make([]float32, rowSamples)
	}
	accumulator := scratch.accumulator[:rowSamples]
	for sourceRow := firstSource; sourceRow < endSource; sourceRow++ {
		offset := (sourceRow - firstSource) * rowSamples
		job.filterRow(sourceRow, scratch.rows[offset:offset+rowSamples])
	}
	for row := firstRow; row < endRow; row++ {
		clear(accumulator)
		start, weights := job.vertical.taps(row)
		for tap, weight := range weights {
			offset := (start + tap - firstSource) * rowSamples
			line := scratch.rows[offset : offset+rowSamples]
			for index, value := range line {
				accumulator[index] += weight * value
			}
		}
		job.writeRow(row, accumulator)
	}
}

// filterRow horizontally resamples one source row of the crop into
// premultiplied linear RGBA samples.
func (job *linearResample) filterRow(sourceRow int, output []float32) {
	pixels := job.src.Pix[job.src.PixOffset(job.crop.Min.X, job.crop.Min.Y+sourceRow):]
	decode := &job.tables.decode32
	for x := range job.horizontal.start {
		start, weights := job.horizontal.taps(x)
		var red, green, blue, alpha float32
		offset := start * 4
		for _, weight := range weights {
			sample := pixels[offset : offset+4 : offset+4]
			switch sample[3] {
			case 255:
				red += weight * decode[sample[0]]
				green += weight * decode[sample[1]]
				blue += weight * decode[sample[2]]
				alpha += weight
			case 0:
			default:
				linear := decodePremultiplied(sample)
				red += weight * linear[0]
				green += weight * linear[1]
				blue += weight * linear[2]
				alpha += weight * linear[3]
			}
			offset += 4
		}
		output[x*4], output[x*4+1], output[x*4+2], output[x*4+3] = red, green, blue, alpha
	}
}

// decodePremultiplied converts a translucent premultiplied sRGB sample to
// premultiplied linear light.
func decodePremultiplied(sample []uint8) [resampleChannels]float32 {
	alpha := float64(sample[3])
	coverage := alpha / 255
	channel := func(value uint8) float32 {
		return float32(srgbDecode(min(float64(value)/alpha, 1)) * coverage)
	}
	return [resampleChannels]float32{channel(sample[0]), channel(sample[1]), channel(sample[2]), float32(coverage)}
}

// writeRow converts accumulated premultiplied linear samples to encoded,
// premultiplied 8-bit RGBA with the same 16-bit alpha quantization as the
// golang.org/x/image/draw path it replaces.
func (job *linearResample) writeRow(row int, samples []float32) {
	output := job.destination.Pix[row*job.destination.Stride:]
	for x := 0; x < len(samples); x += resampleChannels {
		alpha := min(max(float64(samples[x+3]), 0), 1)
		alpha16 := int(alpha*0xffff + 0.5)
		if alpha16 == 0 {
			output[x], output[x+1], output[x+2], output[x+3] = 0, 0, 0, 0
			continue
		}
		alpha8 := uint8((alpha16 + 128) / 257) //nolint:gosec // alpha16 is clamped to 1..65535
		quantizedAlpha := float64(alpha16) / 0xffff
		for channel := range 3 {
			linear := min(max(float64(samples[x+channel]), 0)/quantizedAlpha, 1)
			if alpha8 == 255 {
				output[x+channel] = job.tables.encode(linear)
			} else {
				output[x+channel] = clampByte(srgbEncode(linear) * float64(alpha8))
			}
		}
		output[x+3] = alpha8
	}
}

// resampleAxis holds normalized Catmull-Rom taps for one axis. Kernel
// placement, support broadening on shrink, and edge renormalization follow
// golang.org/x/image/draw so linear and gamma paths share geometry.
type resampleAxis struct {
	start   []int
	offset  []int
	weights []float32
}

func newResampleAxis(sourceSize, destinationSize int) resampleAxis {
	scale := float64(sourceSize) / float64(destinationSize)
	halfWidth, argumentScale := 2.0, 1.0
	if scale > 1 {
		halfWidth *= scale
		argumentScale = 1 / scale
	}
	axis := resampleAxis{start: make([]int, destinationSize), offset: make([]int, destinationSize+1)}
	taps := make([]float64, 0, int(2*halfWidth)+3)
	for index := range destinationSize {
		center := (float64(index)+0.5)*scale - 0.5
		first := max(int(math.Floor(center-halfWidth)), 0)
		end := min(int(math.Ceil(center+halfWidth)), sourceSize)
		taps, first = catmullRomTaps(taps[:0], center, argumentScale, first, end)
		total := 0.0
		for _, weight := range taps {
			total += weight
		}
		axis.start[index] = first
		for _, weight := range taps {
			axis.weights = append(axis.weights, float32(weight/total))
		}
		axis.offset[index+1] = len(axis.weights)
	}
	return axis
}

// catmullRomTaps appends the kernel weights for source coordinates
// [first, end) and trims zero-weight taps from both ends.
func catmullRomTaps(taps []float64, center, argumentScale float64, first, end int) ([]float64, int) {
	for coordinate := first; coordinate < end; coordinate++ {
		weight := catmullRom(math.Abs((center - float64(coordinate)) * argumentScale))
		if len(taps) == 0 && weight == 0 && coordinate+1 < end {
			first++
			continue
		}
		taps = append(taps, weight)
	}
	for len(taps) > 1 && taps[len(taps)-1] == 0 {
		taps = taps[:len(taps)-1]
	}
	return taps, first
}

func catmullRom(distance float64) float64 {
	switch {
	case distance < 1:
		return (1.5*distance-2.5)*distance*distance + 1
	case distance < 2:
		return ((-0.5*distance+2.5)*distance-4)*distance + 2
	default:
		return 0
	}
}

func (axis resampleAxis) taps(index int) (int, []float32) {
	return axis.start[index], axis.weights[axis.offset[index]:axis.offset[index+1]]
}

func (axis resampleAxis) maxTaps() int {
	widest := 0
	for index := range axis.start {
		widest = max(widest, axis.offset[index+1]-axis.offset[index])
	}
	return widest
}

// sourceSpan returns the half-open source range read by destination indexes
// [first, end).
func (axis resampleAxis) sourceSpan(first, end int) (int, int) {
	low, high := axis.start[first], 0
	for index := first; index < end; index++ {
		low = min(low, axis.start[index])
		high = max(high, axis.start[index]+axis.offset[index+1]-axis.offset[index])
	}
	return low, high
}
