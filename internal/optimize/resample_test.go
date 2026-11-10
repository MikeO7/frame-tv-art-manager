package optimize

import (
	"image"
	"math"
	"testing"

	"golang.org/x/image/draw"
)

// referenceTaps returns the normalized Catmull-Rom taps of one output index,
// placed and broadened as golang.org/x/image/draw does.
func referenceTaps(index, sourceSize, destinationSize int) (int, []float64) {
	scale := float64(sourceSize) / float64(destinationSize)
	stretch := max(scale, 1)
	center := (float64(index)+0.5)*scale - 0.5
	first := max(int(math.Floor(center-2*stretch)), 0)
	end := min(int(math.Ceil(center+2*stretch)), sourceSize)
	weights := make([]float64, 0, end-first)
	total := 0.0
	for coordinate := first; coordinate < end; coordinate++ {
		weight := catmullRom(math.Abs(center-float64(coordinate)) / stretch)
		weights = append(weights, weight)
		total += weight
	}
	for tap := range weights {
		weights[tap] /= total
	}
	return first, weights
}

// referenceFilter applies taps to samples and limits the result to the range
// of the samples under the kernel's positive lobe.
func referenceFilter(weights []float64, sample func(tap int) float64) float64 {
	sum, lowest, highest := 0.0, math.Inf(1), math.Inf(-1)
	for tap, weight := range weights {
		value := sample(tap)
		sum += weight * value
		if weight > positiveLobeWeight {
			lowest, highest = min(lowest, value), max(highest, value)
		}
	}
	return min(max(sum, lowest), highest)
}

// referenceLinearResize is a direct float64 statement of the resize: separable
// Catmull-Rom over premultiplied linear light, ring-limited per pass.
func referenceLinearResize(src *image.RGBA, crop image.Rectangle, targetWidth, targetHeight int) *image.RGBA {
	linear := make([][resampleChannels]float64, crop.Dx()*crop.Dy())
	for y := range crop.Dy() {
		for x := range crop.Dx() {
			offset := src.PixOffset(crop.Min.X+x, crop.Min.Y+y)
			alpha := float64(src.Pix[offset+3])
			pixel := &linear[y*crop.Dx()+x]
			pixel[3] = alpha / 255
			for channel := range 3 {
				if alpha > 0 {
					pixel[channel] = srgbDecode(min(float64(src.Pix[offset+channel])/alpha, 1)) * pixel[3]
				}
			}
		}
	}
	rows := make([][resampleChannels]float64, targetWidth*crop.Dy())
	for x := range targetWidth {
		first, weights := referenceTaps(x, crop.Dx(), targetWidth)
		for y := range crop.Dy() {
			for channel := range resampleChannels {
				rows[y*targetWidth+x][channel] = referenceFilter(weights, func(tap int) float64 {
					return linear[y*crop.Dx()+first+tap][channel]
				})
			}
		}
	}
	output := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := range targetHeight {
		first, weights := referenceTaps(y, crop.Dy(), targetHeight)
		for x := range targetWidth {
			var pixel [resampleChannels]float64
			for channel := range resampleChannels {
				pixel[channel] = referenceFilter(weights, func(tap int) float64 {
					return rows[(first+tap)*targetWidth+x][channel]
				})
			}
			offset := output.PixOffset(x, y)
			alpha16 := math.Round(min(max(pixel[3], 0), 1) * 0xffff)
			if alpha16 == 0 {
				continue
			}
			alpha8 := math.Floor((alpha16 + 128) / 257)
			for channel := range 3 {
				straight := min(max(pixel[channel], 0)/(alpha16/0xffff), 1)
				output.Pix[offset+channel] = clampByte(srgbEncode(straight) * alpha8)
			}
			output.Pix[offset+3] = uint8(alpha8)
		}
	}
	return output
}

func resampleFixture(width, height int, translucent bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	state := uint32(0x9e3779b9)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			offset := img.PixOffset(x, y)
			alpha := uint8(255)
			if translucent {
				alpha = uint8(state >> 24)
			}
			red := uint8((x*255/max(width-1, 1) + int(state&31)) & 255)
			green := uint8((y*255/max(height-1, 1) + int(state>>8&31)) & 255)
			blue := uint8(128 + 100*math.Sin(float64(x*3+y)/7))
			img.Pix[offset] = uint8(uint32(red) * uint32(alpha) / 255)
			img.Pix[offset+1] = uint8(uint32(green) * uint32(alpha) / 255)
			img.Pix[offset+2] = uint8(uint32(blue) * uint32(alpha) / 255)
			img.Pix[offset+3] = alpha
		}
	}
	return img
}

func TestResampleLinearMatchesReference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		crop          image.Rectangle
		targetW       int
		targetH       int
		translucent   bool
		workers       int
	}{
		{name: "downscale", width: 640, height: 360, crop: image.Rect(0, 0, 640, 360), targetW: 384, targetH: 216, workers: 4},
		{name: "strong downscale crop", width: 900, height: 700, crop: image.Rect(50, 40, 850, 490), targetW: 96, targetH: 54, workers: 3},
		{name: "upscale", width: 120, height: 68, crop: image.Rect(0, 0, 120, 68), targetW: 384, targetH: 216, workers: 2},
		{name: "identity", width: 64, height: 36, crop: image.Rect(0, 0, 64, 36), targetW: 64, targetH: 36, workers: 1},
		{name: "translucent", width: 300, height: 200, crop: image.Rect(10, 0, 290, 200), targetW: 140, targetH: 100, translucent: true, workers: 4},
		{name: "single row target", width: 200, height: 90, crop: image.Rect(0, 0, 200, 90), targetW: 50, targetH: 1, workers: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			src := resampleFixture(test.width, test.height, test.translucent)
			want := referenceLinearResize(src, test.crop, test.targetW, test.targetH)
			got := resampleLinear(src, test.crop, test.targetW, test.targetH, test.workers)
			for index := range want.Pix {
				if delta := int(got.Pix[index]) - int(want.Pix[index]); delta < -1 || delta > 1 {
					t.Fatalf("sample %d = %d, reference %d", index, got.Pix[index], want.Pix[index])
				}
			}
		})
	}
}

func TestResampleLinearIsWorkerIndependentAndHonorsSubImageOrigin(t *testing.T) {
	t.Parallel()
	parent := resampleFixture(500, 400, false)
	sub, ok := parent.SubImage(image.Rect(37, 21, 437, 246)).(*image.RGBA)
	if !ok {
		t.Fatal("SubImage did not return *image.RGBA")
	}
	serial := resampleLinear(sub, sub.Bounds(), 160, 90, 1)
	parallel := resampleLinear(sub, sub.Bounds(), 160, 90, 8)
	copied := resampleLinear(toZeroOrigin(sub), image.Rect(0, 0, 400, 225), 160, 90, 3)
	for index := range serial.Pix {
		if serial.Pix[index] != parallel.Pix[index] || serial.Pix[index] != copied.Pix[index] {
			t.Fatalf("sample %d differs: serial %d parallel %d zero-origin %d",
				index, serial.Pix[index], parallel.Pix[index], copied.Pix[index])
		}
	}
}

func toZeroOrigin(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		copy(dst.Pix[y*dst.Stride:(y+1)*dst.Stride], src.Pix[src.PixOffset(bounds.Min.X, bounds.Min.Y+y):])
	}
	return dst
}

func TestResampleLinearBandsStayWithinBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		cropW, cropH           int
		targetW, targetH       int
		wantMultipleBandsAtMin int
	}{
		{name: "camera downscale", cropW: 6000, cropH: 3375, targetW: 3840, targetH: 2160, wantMultipleBandsAtMin: 2},
		{name: "upscale", cropW: 1920, cropH: 1080, targetW: 3840, targetH: 2160, wantMultipleBandsAtMin: 2},
		{name: "tall shrink", cropW: 40, cropH: 400000, targetW: 4, targetH: 2, wantMultipleBandsAtMin: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			job := linearResample{
				crop:        image.Rect(0, 0, test.cropW, test.cropH),
				horizontal:  newResampleAxis(test.cropW, test.targetW),
				vertical:    newResampleAxis(test.cropH, test.targetH),
				destination: &image.RGBA{Rect: image.Rect(0, 0, test.targetW, test.targetH)},
			}
			rows := job.outputRowsPerBand()
			if rows < 1 || (test.targetH+rows-1)/rows < test.wantMultipleBandsAtMin {
				t.Fatalf("outputRowsPerBand() = %d for %d target rows", rows, test.targetH)
			}
			for first := 0; first < test.targetH; first += rows {
				low, high := job.vertical.sourceSpan(first, min(first+rows, test.targetH))
				bytes := (high - low) * test.targetW * resampleChannels * 4
				singleLow, singleHigh := job.vertical.sourceSpan(first, first+1)
				singleRow := (singleHigh - singleLow) * test.targetW * resampleChannels * 4
				if bytes > max(resampleBandBudget, singleRow) {
					t.Fatalf("band at row %d needs %d bytes, budget %d", first, bytes, resampleBandBudget)
				}
			}
		})
	}
}

func TestResampleAxisWeightsAreNormalizedAndBounded(t *testing.T) {
	t.Parallel()
	tests := []struct{ source, destination int }{{6000, 3840}, {1080, 2160}, {7, 3}, {1, 5}, {5, 1}}
	for _, test := range tests {
		axis := newResampleAxis(test.source, test.destination)
		for index := range test.destination {
			start, weights := axis.taps(index)
			sum := float32(0)
			for _, weight := range weights {
				sum += weight
			}
			if math.Abs(float64(sum-1)) > 1e-5 || start < 0 || start+len(weights) > test.source {
				t.Fatalf("axis %d->%d index %d: start %d taps %d sum %f", test.source, test.destination, index, start, len(weights), sum)
			}
		}
	}
	if catmullRom(2.5) != 0 || catmullRom(0) != 1 {
		t.Fatal("Catmull-Rom kernel support or center is wrong")
	}
}

func TestSRGBTablesMatchExactTransfer(t *testing.T) {
	t.Parallel()
	tables := srgbLookup()
	for value := range 256 {
		if tables.decode[value] != srgbDecode(float64(value)/255) {
			t.Fatalf("decode[%d] differs from srgbDecode", value)
		}
	}
	check := func(linear float64) {
		if got, want := tables.encode(linear), roundedSRGBByte(linear); got != want {
			t.Fatalf("encode(%v) = %d, want %d", linear, got, want)
		}
	}
	const gridSteps = 1 << 20
	for step := 0; step <= gridSteps; step++ {
		check(float64(step) / gridSteps)
	}
	for target := 1; target < 256; target++ {
		threshold := tables.threshold[target]
		below, above := threshold, threshold
		for range 4 {
			below = math.Nextafter(below, 0)
			above = math.Nextafter(above, 1)
			check(below)
			check(above)
		}
		check(threshold)
	}
	for _, edge := range []float64{-1, 0, 1, 2, math.NaN(), math.Inf(1), math.Inf(-1)} {
		want := uint8(0)
		if edge >= 1 {
			want = 255
		}
		if got := tables.encode(edge); got != want {
			t.Fatalf("encode(%v) = %d, want %d", edge, got, want)
		}
	}
}

func TestResizeCropGammaPathUsesKernelScale(t *testing.T) {
	t.Parallel()
	src := resampleFixture(160, 90, false)
	crop := image.Rect(8, 0, 152, 81)
	want := image.NewRGBA(image.Rect(0, 0, 64, 36))
	draw.CatmullRom.Scale(want, want.Bounds(), src, crop, draw.Src, nil)
	got := resizeCrop(src, crop, 64, 36, false)
	for index := range want.Pix {
		if got.Pix[index] != want.Pix[index] {
			t.Fatalf("gamma-space sample %d = %d, want %d", index, got.Pix[index], want.Pix[index])
		}
	}
}

func TestResampleLinearDoesNotRingBesideEdges(t *testing.T) {
	t.Parallel()
	const dark, bright = 60, 255
	tests := []struct {
		name             string
		width, height    int
		targetW, targetH int
		isBright         func(x, y int) bool
	}{
		{name: "vertical edge upscaled 2x", width: 64, height: 16, targetW: 128, targetH: 32, isBright: func(x, _ int) bool { return x >= 32 }},
		{name: "horizontal edge upscaled 1.5x", width: 16, height: 64, targetW: 24, targetH: 96, isBright: func(_, y int) bool { return y >= 32 }},
		{name: "one pixel line upscaled 2x", width: 65, height: 16, targetW: 130, targetH: 32, isBright: func(x, _ int) bool { return x == 32 }},
		{name: "vertical edge downscaled", width: 201, height: 40, targetW: 100, targetH: 20, isBright: func(x, _ int) bool { return x >= 100 }},
		{name: "corner downscaled", width: 1000, height: 600, targetW: 384, targetH: 230, isBright: func(x, y int) bool { return x >= 500 && y >= 300 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			src := image.NewRGBA(image.Rect(0, 0, test.width, test.height))
			for y := range test.height {
				for x := range test.width {
					level := uint8(dark)
					if test.isBright(x, y) {
						level = bright
					}
					offset := src.PixOffset(x, y)
					src.Pix[offset], src.Pix[offset+1], src.Pix[offset+2], src.Pix[offset+3] = level, level, level, 255
				}
			}
			got := resampleLinear(src, src.Bounds(), test.targetW, test.targetH, 3)
			brightSeen := false
			for index, value := range got.Pix {
				if index%4 == 3 {
					continue
				}
				if value < dark {
					t.Fatalf("sample %d = %d, darker than the darkest source tone %d", index, value, dark)
				}
				brightSeen = brightSeen || value > 200
			}
			if !brightSeen {
				t.Fatal("the bright side of the edge was lost")
			}
		})
	}
}
