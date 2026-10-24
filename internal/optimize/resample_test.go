package optimize

import (
	"encoding/binary"
	"image"
	"math"
	"testing"

	"golang.org/x/image/draw"
)

// referenceLinearResize is the previous golang.org/x/image/draw RGBA64
// linear-light implementation, retained to bound the replacement's drift.
func referenceLinearResize(src *image.RGBA, crop image.Rectangle, targetWidth, targetHeight int) *image.RGBA {
	linearSource := image.NewRGBA64(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	for y := 0; y < crop.Dy(); y++ {
		for x := 0; x < crop.Dx(); x++ {
			sourceOffset := src.PixOffset(crop.Min.X+x, crop.Min.Y+y)
			destinationOffset := y*linearSource.Stride + x*8
			alpha := src.Pix[sourceOffset+3]
			alpha16 := uint16(alpha) * 257
			for channel := 0; channel < 3; channel++ {
				encoded := 0.0
				if alpha > 0 {
					encoded = min(float64(src.Pix[sourceOffset+channel])/float64(alpha), 1)
				}
				linear := srgbDecode(encoded) * float64(alpha16)
				binary.BigEndian.PutUint16(linearSource.Pix[destinationOffset+channel*2:], uint16(math.Round(linear)))
			}
			binary.BigEndian.PutUint16(linearSource.Pix[destinationOffset+6:], alpha16)
		}
	}
	linearOutput := image.NewRGBA64(image.Rect(0, 0, targetWidth, targetHeight))
	draw.CatmullRom.Scale(linearOutput, linearOutput.Bounds(), linearSource, linearSource.Bounds(), draw.Src, nil)
	output := image.NewRGBA(linearOutput.Bounds())
	for y := 0; y < targetHeight; y++ {
		for x := 0; x < targetWidth; x++ {
			sourceOffset := y*linearOutput.Stride + x*8
			destinationOffset := y*output.Stride + x*4
			alpha16 := binary.BigEndian.Uint16(linearOutput.Pix[sourceOffset+6:])
			alpha8 := uint8((uint32(alpha16) + 128) / 257)
			for channel := 0; channel < 3; channel++ {
				premultiplied := binary.BigEndian.Uint16(linearOutput.Pix[sourceOffset+channel*2:])
				linear := 0.0
				if alpha16 > 0 {
					linear = min(float64(premultiplied)/float64(alpha16), 1)
				}
				output.Pix[destinationOffset+channel] = clampByte(srgbEncode(linear) * float64(alpha8))
			}
			output.Pix[destinationOffset+3] = alpha8
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

func TestResampleLinearMatchesPreviousImplementation(t *testing.T) {
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
					t.Fatalf("sample %d = %d, previous implementation %d", index, got.Pix[index], want.Pix[index])
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
