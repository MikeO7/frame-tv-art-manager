package optimize

import (
	"image"
	std_draw "image/draw"
	"math"
	"sync"
)

var (
	lutSrgb           [16384]uint8
	lutSrgbOnce       sync.Once
	lutWeave          [400]float64
	lutVarnishPool    [400]float64
	lutWeaveOnce      sync.Once
	lutCraquelure     [65536]float64
	lutCraquelureOnce sync.Once
)

// applyMuseumMode orchestrates a suite of visual filters to simulate physical artwork.
func applyMuseumMode(src *image.RGBA, intensity int) *image.RGBA {
	if intensity > 10 {
		intensity = 10
	}
	if intensity < 0 {
		intensity = 0
	}

	img := unifyCollection(src)

	if intensity > 0 {
		img = applyCanvasTexture(img, intensity)
	}

	img = galleryMasterPolish(img)

	return img
}

func unifyCollection(src *image.RGBA) *image.RGBA {
	_, rms := calculateRMSContrast(src)

	// Target Gallery RMS (Rich Contrast)
	const targetRMS = 58.0
	contrastGamma := 1.0 + (rms-targetRMS)/100.0
	if contrastGamma < 0.85 {
		contrastGamma = 0.85
	}
	if contrastGamma > 1.15 {
		contrastGamma = 1.15
	}

	applyContrastAndGamut(src, contrastGamma)
	return src
}

func calculateRMSContrast(src *image.RGBA) (float64, float64) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var sumSq, sum float64

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers
	sums := make([]float64, workers)
	sumSqs := make([]float64, workers)

	for i := 0; i < workers; i++ {
		startY := i * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(workerIdx, sy, ey int) {
			defer wg.Done()
			var localSum, localSumSq float64
			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					idx := offset + x*4
					lum := 0.299*float64(src.Pix[idx]) + 0.587*float64(src.Pix[idx+1]) + 0.114*float64(src.Pix[idx+2])
					localSum += lum
					localSumSq += lum * lum
				}
			}
			sums[workerIdx] = localSum
			sumSqs[workerIdx] = localSumSq
		}(i, startY, endY)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		sum += sums[i]
		sumSq += sumSqs[i]
	}
	mean := sum / float64(width*height)
	rms := math.Sqrt(sumSq/float64(width*height) - mean*mean)
	return mean, rms
}

// processGamutPixel applies the gamma contrast and pigment gamut compression to a pixel.
func processGamutPixel(r, g, b uint8, lutLin *[256]float64) (uint8, uint8, uint8) {
	rLin := lutLin[r]
	gLin := lutLin[g]
	bLin := lutLin[b]

	avg := (rLin + gLin + bLin) * 0.333333333
	rLin = rLin*0.97 + avg*0.03
	gLin = gLin*0.97 + avg*0.03
	bLin = bLin*0.97 + avg*0.03

	fR := rLin * 16383.0
	idxR := 0
	if fR >= 16383.0 {
		idxR = 16383
	} else if fR > 0 {
		idxR = int(fR)
	}

	fG := gLin * 16383.0
	idxG := 0
	if fG >= 16383.0 {
		idxG = 16383
	} else if fG > 0 {
		idxG = int(fG)
	}

	fB := bLin * 16383.0
	idxB := 0
	if fB >= 16383.0 {
		idxB = 16383
	} else if fB > 0 {
		idxB = int(fB)
	}

	return lutSrgb[idxR], lutSrgb[idxG], lutSrgb[idxB]
}

func applyContrastAndGamut(src *image.RGBA, contrastGamma float64) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var lutLin [256]float64
	for i := 0; i < 256; i++ {
		lutLin[i] = math.Pow(float64(i)/255.0, 2.2*contrastGamma)
	}

	lutSrgbOnce.Do(func() {
		for i := 0; i < 16384; i++ {
			val := math.Pow(float64(i)/16383.0, 0.454545454545) * 255.0
			switch {
			case val < 0:
				lutSrgb[i] = 0
			case val > 255:
				lutSrgb[i] = 255
			default:
				lutSrgb[i] = uint8(val)
			}
		}
	})

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := j * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()
			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					outR, outG, outB := processGamutPixel(src.Pix[i], src.Pix[i+1], src.Pix[i+2], &lutLin)
					src.Pix[i] = outR
					src.Pix[i+1] = outG
					src.Pix[i+2] = outB
				}
			}
		}(startY, endY)
	}
	wg.Wait()
}

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

func processCanvasPixel(src, dst *image.RGBA, i, x, y int, state *uint32, opacity float64) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()

	impasto := calculateScharrImpasto(src, x, y, w, h)
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

func getLuminance(src *image.RGBA, x, y, w, h int) float64 {
	if x < 0 {
		x = 0
	} else if x >= w {
		x = w - 1
	}
	if y < 0 {
		y = 0
	} else if y >= h {
		y = h - 1
	}
	idx := y*src.Stride + x*4
	return 0.299*float64(src.Pix[idx]) + 0.587*float64(src.Pix[idx+1]) + 0.114*float64(src.Pix[idx+2])
}

func calculateScharrImpasto(src *image.RGBA, x, y, w, h int) float64 {
	m00 := getLuminance(src, x-1, y-1, w, h)
	m10 := getLuminance(src, x, y-1, w, h)
	m20 := getLuminance(src, x+1, y-1, w, h)

	m01 := getLuminance(src, x-1, y, w, h)
	m21 := getLuminance(src, x+1, y, w, h)

	m02 := getLuminance(src, x-1, y+1, w, h)
	m12 := getLuminance(src, x, y+1, w, h)
	m22 := getLuminance(src, x+1, y+1, w, h)

	gx := 3.0*(m20-m00) + 10.0*(m21-m01) + 3.0*(m22-m02)
	gy := 3.0*(m02-m00) + 10.0*(m12-m10) + 3.0*(m22-m20)

	return (gx*-0.707 + gy*-0.707) / 4080.0 * 0.3
}

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

// polishPixel limits the maximum brightness and adds paper grain noise to a pixel.
func polishPixel(r, g, b float32, state *uint32) (uint8, uint8, uint8) {
	const maxBright = 235.0
	if r > maxBright {
		r = maxBright
	}
	if g > maxBright {
		g = maxBright
	}
	if b > maxBright {
		b = maxBright
	}

	avg := (r + g + b) * 0.33333333
	r = r*0.92 + avg*0.08
	g = g*0.92 + avg*0.08
	b = b*0.92 + avg*0.08

	*state ^= *state << 13
	*state ^= *state >> 17
	*state ^= *state << 5

	noise := (float32(*state)/float32(0xFFFFFFFF) - 0.5) * 5.0
	r += noise
	g += noise
	b += noise

	var outR, outG, outB uint8
	switch {
	case r < 0:
		outR = 0
	case r > 255:
		outR = 255
	default:
		outR = uint8(r)
	}

	switch {
	case g < 0:
		outG = 0
	case g > 255:
		outG = 255
	default:
		outG = uint8(g)
	}

	switch {
	case b < 0:
		outB = 0
	case b > 255:
		outB = 255
	default:
		outB = uint8(b)
	}

	return outR, outG, outB
}

func galleryMasterPolish(src *image.RGBA) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	var wg sync.WaitGroup
	workers := 8
	chunk := (height + workers - 1) / workers

	for j := 0; j < workers; j++ {
		startY := j * chunk
		endY := startY + chunk
		if endY > height {
			endY = height
		}
		if startY >= height {
			break
		}

		wg.Add(1)
		go func(sy, ey int) {
			defer wg.Done()

			//nolint:gosec // sy is a positive chunk offset, conversion is mathematically safe
			state := uint32(sy + 1) // Seed based on row

			for y := sy; y < ey; y++ {
				offset := y * src.Stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					outR, outG, outB := polishPixel(float32(src.Pix[i]), float32(src.Pix[i+1]), float32(src.Pix[i+2]), &state)
					src.Pix[i] = outR
					src.Pix[i+1] = outG
					src.Pix[i+2] = outB
				}
			}
		}(startY, endY)
	}
	wg.Wait()
	return src
}
