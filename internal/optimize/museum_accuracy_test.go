package optimize

import (
	"image"
	"math"
	"testing"
)

// greyBands is a picture of vertical grey bands spanning low..high.
func greyBands(low, high int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 256, 64))
	for y := range 64 {
		for x := range 256 {
			level := uint8(low + (high-low)*x/255)
			offset := img.PixOffset(x, y)
			img.Pix[offset], img.Pix[offset+1], img.Pix[offset+2], img.Pix[offset+3] = level, level, level, 255
		}
	}
	return img
}

func TestMuseumContrastMovesTowardTheTargetWithoutShiftingBrightness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		low, high int
		wantMore  bool
	}{
		{name: "flat dark picture gains contrast", low: 20, high: 80, wantMore: true},
		{name: "flat bright picture gains contrast", low: 150, high: 230, wantMore: true},
		{name: "harsh picture loses contrast", low: 0, high: 255, wantMore: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			img := greyBands(test.low, test.high)
			meanBefore, rmsBefore := lumaMeanAndRMS(img)
			applyContrastAndGamut(img, meanBefore/255, min(max(58/max(rmsBefore, 1), 0.85), 1.15))
			meanAfter, rmsAfter := lumaMeanAndRMS(img)
			if math.Abs(meanAfter-meanBefore) > 2.5 {
				t.Fatalf("mean brightness moved from %.1f to %.1f", meanBefore, meanAfter)
			}
			if more := rmsAfter > rmsBefore; more != test.wantMore {
				t.Fatalf("contrast went from %.2f to %.2f", rmsBefore, rmsAfter)
			}
			if ratio := rmsAfter / rmsBefore; ratio < 0.84 || ratio > 1.16 {
				t.Fatalf("contrast changed by %.3f, want within 15%%", ratio)
			}
		})
	}
}

func TestPivotedContrastIsMonotonicAndNeverClips(t *testing.T) {
	t.Parallel()
	for _, strength := range []float64{0.85, 1, 1.15} {
		for _, pivot := range []float64{0.02, 0.3, 0.5, 0.8, 0.99} {
			previous := -1.0
			for level := range 256 {
				value := pivotedContrast(float64(level)/255, pivot, strength)
				if value <= previous || value < 0 || value > 1 {
					t.Fatalf("strength %.2f pivot %.2f: level %d maps to %v after %v", strength, pivot, level, value, previous)
				}
				previous = value
			}
			if pivotedContrast(0, pivot, strength) != 0 || pivotedContrast(1, pivot, strength) != 1 {
				t.Fatalf("strength %.2f pivot %.2f moves black or white", strength, pivot)
			}
			held := min(max(pivot, 0.1), 0.9)
			if got := pivotedContrast(held, pivot, strength); math.Abs(got-held) > 1e-12 {
				t.Fatalf("pivot %.2f moved to %v", held, got)
			}
		}
	}
}

func TestHighlightRollOffKeepsHue(t *testing.T) {
	t.Parallel()
	for _, pixel := range [][3]float32{{255, 230, 200}, {120, 200, 255}, {255, 200, 120}, {250, 250, 250}} {
		// Average many grain samples: the grain is the same on all channels.
		var sum [3]float64
		state := uint32(7)
		const samples = 4096
		for range samples {
			r, g, b := polishPixel(pixel[0], pixel[1], pixel[2], &state)
			sum[0] += float64(r)
			sum[1] += float64(g)
			sum[2] += float64(b)
		}
		brightest := max(pixel[0], pixel[1], pixel[2])
		scale := float64(rollOffHighlight(brightest) / brightest)
		average := (float64(pixel[0]) + float64(pixel[1]) + float64(pixel[2])) / 3 * scale
		for channel := range 3 {
			want := float64(pixel[channel])*scale*0.92 + average*0.08
			if got := sum[channel] / samples; math.Abs(got-want) > 0.6 {
				t.Fatalf("pixel %v channel %d = %.2f, want %.2f with all channels scaled alike", pixel, channel, got, want)
			}
		}
	}
}

func TestCanvasTextureReachesTheImageEdge(t *testing.T) {
	t.Parallel()
	src := image.NewRGBA(image.Rect(0, 0, 96, 64))
	for offset := 0; offset < len(src.Pix); offset += 4 {
		src.Pix[offset], src.Pix[offset+1], src.Pix[offset+2], src.Pix[offset+3] = 235, 235, 235, 255
	}
	textured := applyCanvasTexture(src, 5)
	blue := func(pixels []image.Point) float64 {
		total := 0.0
		for _, point := range pixels {
			total += float64(textured.RGBAAt(point.X, point.Y).B)
		}
		return total / float64(len(pixels))
	}
	var border []image.Point
	for x := range 96 {
		border = append(border, image.Pt(x, 0), image.Pt(x, 63))
	}
	for y := 1; y < 63; y++ {
		border = append(border, image.Pt(0, y), image.Pt(95, y))
	}
	// An untextured border would keep the source's 235.
	if edge := blue(border); edge > 231 {
		t.Fatalf("border blue averages %.1f, want the varnish tint the rest of the picture gets", edge)
	}
}

// The crack network must not repeat across the frame: a tiled pattern shows as
// a visible grid on a 4K panel.
func TestCraquelureDoesNotTile(t *testing.T) {
	t.Parallel()
	const width, height = 1024, 512
	field := newCraquelureField(width, height)
	cracked, repeated := 0, 0
	for y := range height {
		for x := range width - 256 {
			here := field.at(x, y)
			if here == 0 {
				continue
			}
			cracked++
			if field.at(x+256, y) == here {
				repeated++
			}
		}
	}
	if share := float64(cracked) / float64((width-256)*height); share < 0.02 || share > 0.2 {
		t.Fatalf("cracks cover %.1f%% of the canvas, want a sparse network", share*100)
	}
	if repeated*20 > cracked {
		t.Fatalf("%d of %d crack pixels repeat 256 pixels along", repeated, cracked)
	}
	// Cracks are continuous lines: no crack pixel stands alone.
	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			if field.at(x, y) > -0.3 {
				continue
			}
			neighbors := 0
			for _, step := range [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}} {
				if field.at(x+step[0], y+step[1]) < 0 {
					neighbors++
				}
			}
			if neighbors < 2 {
				t.Fatalf("crack pixel at %d,%d has %d crack neighbors", x, y, neighbors)
			}
		}
	}
}
