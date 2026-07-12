package optimize

import "testing"

func TestPolishPixel(t *testing.T) {
	state := uint32(0)
	r, g, b := polishPixel(255, 255, 255, &state)
	if r > 255 || g > 255 || b > 255 {
		t.Fatalf("unexpected overflow: %d,%d,%d", r, g, b)
	}

	state = 0
	r2, g2, b2 := polishPixel(0, 0, 0, &state)
	if r2 != 0 || g2 != 0 || b2 != 0 {
		t.Fatalf("expected clamp-to-zero, got %d,%d,%d", r2, g2, b2)
	}
}

func TestPolishPixel_MaxBrightnessClamp(t *testing.T) {
	state := uint32(1)
	r, g, b := polishPixel(255.0*2, 255.0*2, 255.0*2, &state)
	if r < 230 || g < 230 || b < 230 {
		t.Fatalf("expected clamp + noise adjustment to stay bright, got %d,%d,%d", r, g, b)
	}
	if r > 238 || g > 238 || b > 238 {
		t.Fatalf("expected result not to clip to full white, got %d,%d,%d", r, g, b)
	}
}

func TestCalculateWarpAndWeftHelpers(t *testing.T) {
	warp := calculateWarpCell(4, -0.7)
	if warp <= 0 || warp > 1 {
		t.Fatalf("unexpected warp cell value: %f", warp)
	}
	weft := calculateWeftCell(4, -0.7)
	if weft <= warp {
		t.Fatalf("expected weft weave to be >= warp for center cell, got %f < %f", weft, warp)
	}

	weaveV, varnishV := calculateWeave(0, 0)
	weaveN, varnishN := calculateWeave(1, 11)
	if weaveV == weaveN {
		t.Fatalf("expected weave variation between valley and non-valley: %f vs %f", weaveV, weaveN)
	}
	if varnishV >= varnishN {
		t.Fatalf("expected valley varnish suppression, got %f vs %f", varnishV, varnishN)
	}
}

func TestProcessGamutPixel_Branches(t *testing.T) {
	var lut [256]float64
	lut[0] = 0.0
	lut[2] = 0.001
	lut[255] = 1.1

	original := make([]uint8, srgbLUTSize)
	copy(original, lutSrgb[:])
	defer copy(lutSrgb[:], original)

	lutSrgb[0] = 9
	lutSrgb[16] = 16
	lutSrgb[srgbLUTMaxIdx] = 255

	outR, outG, outB := processGamutPixel(0, 0, 0, &lut)
	if outR != 9 {
		t.Fatalf("expected branch for zero channel to clamp to lut[0], got %d", outR)
	}
	if outG != 9 || outB != 9 {
		t.Fatalf("expected all zero channels to map to lut[0], got %d,%d,%d", outR, outG, outB)
	}

	outR, outG, outB = processGamutPixel(2, 2, 2, &lut)
	if outR != 16 || outG != 16 || outB != 16 {
		t.Fatalf("expected else-if branch for positive channel to map to lut[16], got %d,%d,%d", outR, outG, outB)
	}

	outR, outG, outB = processGamutPixel(255, 255, 255, &lut)
	if outR != 255 || outG != 255 || outB != 255 {
		t.Fatalf("expected max branch for 255 index, got %d,%d,%d", outR, outG, outB)
	}
}
