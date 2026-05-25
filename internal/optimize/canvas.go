package optimize

import (
	"image"
	std_draw "image/draw"
	"math"
	"sync"
)

func applyCanvasTexture(src *image.RGBA, intensity int) *image.RGBA {
	lutCraquelureOnce.Do(initializeCraquelure)

	opacity := 0.04 * math.Pow(1.32, float64(intensity-1))
	if opacity > 0.60 {
		opacity = 0.60
	}

	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	dst := image.NewRGBA(bounds)
	std_draw.Draw(dst, bounds, src, bounds.Min, std_draw.Src)

	var wg sync.WaitGroup
	workers := 8
	chunk := (height - 2 + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := 1 + j*chunk
		endY := startY + chunk
		if endY > height-1 {
			endY = height - 1
		}
		if startY >= height-1 {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()

			//nolint:gosec // sy is a positive chunk offset, conversion is mathematically safe
			state := uint32(sy + 42) // Seed based on row

			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 1; x < width-1; x++ {
					processCanvasPixel(src, dst, offset+x*4, x, y, &state, opacity)
				}
			}
		}(startY, endY)
	}

	wg.Wait()
	return dst
}

//nolint:revive // complexity justified for this domain-specific path
func processCanvasPixel(src, dst *image.RGBA, i, x, y int, state *uint32, opacity float64) {
	impasto := calculateScharrImpasto(src, x, y)
	weave, varnishPool := calculateWeave(x, y)

	*state ^= *state << 13
	*state ^= *state >> 17
	*state ^= *state << 5
	if float32(*state)/float32(0xFFFFFFFF) > 0.98 {
		weave -= 0.05
	}

	weave += lookupCraquelure(x, y)
	weave += impasto

	aR := float64(src.Pix[i]) / 255.0 * 1.01
	r := applySoftLight(aR, weave, opacity)

	aG := float64(src.Pix[i+1]) / 255.0
	g := applySoftLight(aG, weave, opacity)

	aB := float64(src.Pix[i+2]) / 255.0 * (varnishPool * 0.99)
	b := applySoftLight(aB, weave, opacity)

	dst.Pix[i] = r
	dst.Pix[i+1] = g
	dst.Pix[i+2] = b
}

//nolint:funlen // complexity justified for this domain-specific mathematical path
func calculateScharrImpasto(src *image.RGBA, x, y int) float64 {
	stride := src.Stride
	pix := src.Pix

	// Pre-calculate row offsets.
	// Since 1 <= y < height-1 and 1 <= x < width-1, no bounds checking is needed.
	rowPrev := (y - 1) * stride
	rowCurr := y * stride
	rowNext := (y + 1) * stride

	// Column offsets in bytes
	colPrev := (x - 1) * 4
	colCurr := x * 4
	colNext := (x + 1) * 4

	idx00 := rowPrev + colPrev
	idx10 := rowPrev + colCurr
	idx20 := rowPrev + colNext
	idx01 := rowCurr + colPrev
	idx21 := rowCurr + colNext
	idx02 := rowNext + colPrev
	idx12 := rowNext + colCurr
	idx22 := rowNext + colNext

	d20x00R := int(pix[idx20]) - int(pix[idx00])
	d20x00G := int(pix[idx20+1]) - int(pix[idx00+1])
	d20x00B := int(pix[idx20+2]) - int(pix[idx00+2])

	d21x01R := int(pix[idx21]) - int(pix[idx01])
	d21x01G := int(pix[idx21+1]) - int(pix[idx01+1])
	d21x01B := int(pix[idx21+2]) - int(pix[idx01+2])

	d22x02R := int(pix[idx22]) - int(pix[idx02])
	d22x02G := int(pix[idx22+1]) - int(pix[idx02+1])
	d22x02B := int(pix[idx22+2]) - int(pix[idx02+2])

	d02x00R := int(pix[idx02]) - int(pix[idx00])
	d02x00G := int(pix[idx02+1]) - int(pix[idx00+1])
	d02x00B := int(pix[idx02+2]) - int(pix[idx00+2])

	d12x10R := int(pix[idx12]) - int(pix[idx10])
	d12x10G := int(pix[idx12+1]) - int(pix[idx10+1])
	d12x10B := int(pix[idx12+2]) - int(pix[idx10+2])

	d22x20R := int(pix[idx22]) - int(pix[idx20])
	d22x20G := int(pix[idx22+1]) - int(pix[idx20+1])
	d22x20B := int(pix[idx22+2]) - int(pix[idx20+2])

	d3R := d20x00R + d02x00R + d22x02R + d22x20R
	d10R := d21x01R + d12x10R
	gR := (d3R<<1 + d3R) + (d10R<<3 + d10R<<1)

	d3G := d20x00G + d02x00G + d22x02G + d22x20G
	d10G := d21x01G + d12x10G
	gG := (d3G<<1 + d3G) + (d10G<<3 + d10G<<1)

	d3B := d20x00B + d02x00B + d22x02B + d22x20B
	d10B := d21x01B + d12x10B
	gB := (d3B<<1 + d3B) + (d10B<<3 + d10B<<1)

	// Combine differences weighted by perceived luminosity components: (299*R + 587*G + 114*B)
	// We aggregate them globally to delay expensive float conversions until the absolute end
	g := gR*299 + gG*587 + gB*114

	// scale = (-0.707 * 0.3) / (4080.0 * 1000.0) where 1000.0 is from summing 299+587+114
	const scale = (-0.707 * 0.3) / (4080.0 * 1000.0)
	return float64(g) * scale
}

func applySoftLight(a, b, opacity float64) uint8 {
	var res float64
	if b <= 0.5 {
		res = a - (1.0-2.0*b)*a*(1.0-a)
	} else {
		res = a + (2.0*b-1.0)*(math.Sqrt(a)-a)
	}
	final := a*(1.0-opacity) + res*opacity
	v := final * 255.0
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
