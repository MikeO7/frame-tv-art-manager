package optimize

import (
	"image"
	std_draw "image/draw"
	"math"
	"sync"
)

var (
	lutWeave          [400]float64
	lutVarnishPool    [400]float64
	lutWeaveOnce      sync.Once
	lutCraquelure     [65536]float64
	lutCraquelureOnce sync.Once
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

	d20_00_r := int(pix[idx20]) - int(pix[idx00])
	d20_00_g := int(pix[idx20+1]) - int(pix[idx00+1])
	d20_00_b := int(pix[idx20+2]) - int(pix[idx00+2])

	d21_01_r := int(pix[idx21]) - int(pix[idx01])
	d21_01_g := int(pix[idx21+1]) - int(pix[idx01+1])
	d21_01_b := int(pix[idx21+2]) - int(pix[idx01+2])

	d22_02_r := int(pix[idx22]) - int(pix[idx02])
	d22_02_g := int(pix[idx22+1]) - int(pix[idx02+1])
	d22_02_b := int(pix[idx22+2]) - int(pix[idx02+2])

	d02_00_r := int(pix[idx02]) - int(pix[idx00])
	d02_00_g := int(pix[idx02+1]) - int(pix[idx00+1])
	d02_00_b := int(pix[idx02+2]) - int(pix[idx00+2])

	d12_10_r := int(pix[idx12]) - int(pix[idx10])
	d12_10_g := int(pix[idx12+1]) - int(pix[idx10+1])
	d12_10_b := int(pix[idx12+2]) - int(pix[idx10+2])

	d22_20_r := int(pix[idx22]) - int(pix[idx20])
	d22_20_g := int(pix[idx22+1]) - int(pix[idx20+1])
	d22_20_b := int(pix[idx22+2]) - int(pix[idx20+2])

	d_3_r := d20_00_r + d02_00_r + d22_02_r + d22_20_r
	d_10_r := d21_01_r + d12_10_r
	g_r := (d_3_r<<1 + d_3_r) + (d_10_r<<3 + d_10_r<<1)

	d_3_g := d20_00_g + d02_00_g + d22_02_g + d22_20_g
	d_10_g := d21_01_g + d12_10_g
	g_g := (d_3_g<<1 + d_3_g) + (d_10_g<<3 + d_10_g<<1)

	d_3_b := d20_00_b + d02_00_b + d22_02_b + d22_20_b
	d_10_b := d21_01_b + d12_10_b
	g_b := (d_3_b<<1 + d_3_b) + (d_10_b<<3 + d_10_b<<1)

	// Combine differences weighted by perceived luminosity components: (299*R + 587*G + 114*B)
	// We aggregate them globally to delay expensive float conversions until the absolute end
	g := g_r*299 + g_g*587 + g_b*114

	// scale = (-0.707 * 0.3) / (4080.0 * 1000.0) where 1000.0 is from summing 299+587+114
	const scale = (-0.707 * 0.3) / (4080.0 * 1000.0)
	return float64(g) * scale
}

//nolint:gocognit // complexity justified for this domain-specific path
func initializeCraquelure() {
	type pt struct {
		x, y float64
	}
	seeds := make([]pt, 16)
	state := uint32(12345)
	nextRand := func() float64 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return float64(state) / float64(0xFFFFFFFF)
	}

	for i := 0; i < 16; i++ {
		seeds[i] = pt{x: nextRand() * 256.0, y: nextRand() * 256.0}
	}

	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			f1 := 999999.0
			f2 := 999999.0

			for _, s := range seeds {
				dx := math.Abs(float64(x) - s.x)
				if dx > 128.0 {
					dx = 256.0 - dx
				}
				dy := math.Abs(float64(y) - s.y)
				if dy > 128.0 {
					dy = 256.0 - dy
				}
				d := math.Sqrt(dx*dx + dy*dy)

				if d < f1 {
					f2 = f1
					f1 = d
				} else if d < f2 {
					f2 = d
				}
			}

			diff := f2 - f1
			var val float64
			if diff < 2.0 {
				val = -0.4 * (1.0 - diff/2.0)
			}
			lutCraquelure[y*256+x] = val
		}
	}
}

func lookupCraquelure(x, y int) float64 {
	return lutCraquelure[(y%256)*256+(x%256)]
}

// calculateWarpCell calculates weave topography for a warp cell.
func calculateWarpCell(cellX int, lightDirX float64) float64 {
	nx := (float64(cellX) - 4.5) / 5.0
	diffuse := nx * lightDirX
	if diffuse < 0 {
		diffuse = 0
	}
	return 0.4 + (diffuse * 0.3)
}

// calculateWeftCell calculates weave topography for a weft cell.
func calculateWeftCell(cellY int, lightDirY float64) float64 {
	ny := (float64(cellY) - 4.5) / 5.0
	diffuse := ny * lightDirY
	if diffuse < 0 {
		diffuse = 0
	}
	weave := 0.4 + (diffuse * 0.3)

	absNy := ny
	if absNy < 0 {
		absNy = -absNy
	}
	if absNy < 0.2 {
		weave += 0.15
	}
	return weave
}

func calculateWeave(x, y int) (float64, float64) {
	lutWeaveOnce.Do(func() {
		for wy := 0; wy < 20; wy++ {
			for wx := 0; wx < 20; wx++ {
				idX, idY := wx/10, wy/10
				cellX, cellY := wx%10, wy%10
				isWarp := (idX+idY)%2 == 0

				var weave float64
				lightDirX, lightDirY := -0.707, -0.707

				if isWarp {
					weave = calculateWarpCell(cellX, lightDirX)
				} else {
					weave = calculateWeftCell(cellY, lightDirY)
				}

				isValley := cellX == 0 || cellX == 9 || cellY == 0 || cellY == 9
				varnishPool := 1.0
				if isValley {
					weave *= 0.8
					varnishPool = 0.96
				}

				idx := wy*20 + wx
				lutWeave[idx] = weave
				lutVarnishPool[idx] = varnishPool
			}
		}
	})

	idx := (y%20)*20 + (x % 20)
	return lutWeave[idx], lutVarnishPool[idx]
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
