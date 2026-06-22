package optimize

import (
	"image"
	"sync"
)

// polishPixel limits the maximum brightness and adds paper grain noise to a pixel.
//
//nolint:funlen // brightness clamp + paper-grain noise applied inline to each R/G/B channel
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
	workers := pixelWorkers
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

			// OPTIMIZATION: Extracting pointer fields to local variables prevents continuous pointer indirection overhead in tight loops
			pix := src.Pix
			stride := src.Stride

			for y := sy; y < ey; y++ {
				offset := y * stride
				for x := 0; x < width; x++ {
					i := offset + x*4
					outR, outG, outB := polishPixel(float32(pix[i]), float32(pix[i+1]), float32(pix[i+2]), &state)
					pix[i] = outR
					pix[i+1] = outG
					pix[i+2] = outB
				}
			}
		}(startY, endY)
	}
	wg.Wait()
	return src
}
