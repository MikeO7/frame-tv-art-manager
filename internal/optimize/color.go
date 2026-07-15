package optimize

import (
	"math"
	"sync"
)

//nolint:gochecknoglobals // global read-only lookup table for performance-critical RGB-to-Lab calculations
var (
	lutRgbToLab     [256]float64
	lutRgbToLabOnce sync.Once
)

type labColor struct {
	l float64
	a float64
	b float64
}

// ciede2000 calculates the exact CIE 2000 color-difference standard between two CIELAB colors.
//
//nolint:funlen,gocyclo // mathematical formula requiring monolithic execution flow for performance and readability
func ciede2000(color1, color2 labColor) float64 {
	const degToRad = math.Pi / 180.0
	const e7 = 6103515625.0 // 25^7
	// Sharma's supplementary pairs 9-12 straddle the 180-degree hue
	// discontinuity. Treat floating-point values indistinguishable from pi as
	// equality so the formula follows its specified <= 180-degree branch.
	const hueBoundaryTolerance = 1e-12

	l1, a1, b1 := color1.l, color1.a, color1.b
	l2, a2, b2 := color2.l, color2.a, color2.b
	c1c := math.Sqrt(a1*a1 + b1*b1)
	c2c := math.Sqrt(a2*a2 + b2*b2)
	meanC := (c1c + c2c) * 0.5

	// explicit multiplication is significantly faster than math.Pow(x, 7)
	meanC2 := meanC * meanC
	meanC4 := meanC2 * meanC2
	meanC7 := meanC4 * meanC2 * meanC
	g := 0.5 * (1.0 - math.Sqrt(meanC7/(meanC7+e7)))

	aPrime1 := (1.0 + g) * a1
	aPrime2 := (1.0 + g) * a2

	cPrime1 := math.Sqrt(aPrime1*aPrime1 + b1*b1)
	cPrime2 := math.Sqrt(aPrime2*aPrime2 + b2*b2)

	// Calculate hues in radians directly to avoid repeated deg/rad conversions
	var hPrime1, hPrime2 float64
	if aPrime1 != 0 || b1 != 0 {
		hPrime1 = math.Atan2(b1, aPrime1)
		if hPrime1 < 0 {
			hPrime1 += 2 * math.Pi
		}
	}
	if aPrime2 != 0 || b2 != 0 {
		hPrime2 = math.Atan2(b2, aPrime2)
		if hPrime2 < 0 {
			hPrime2 += 2 * math.Pi
		}
	}

	deltaLPrime := l2 - l1
	deltaCPrime := cPrime2 - cPrime1

	deltaHPrime := 0.0
	if cPrime1*cPrime2 != 0 {
		deltahPrime := hPrime2 - hPrime1
		if deltahPrime > math.Pi+hueBoundaryTolerance {
			deltahPrime -= 2 * math.Pi
		} else if deltahPrime < -math.Pi-hueBoundaryTolerance {
			deltahPrime += 2 * math.Pi
		}
		deltaHPrime = 2.0 * math.Sqrt(cPrime1*cPrime2) * math.Sin(deltahPrime*0.5)
	}

	meanL := (l1 + l2) * 0.5
	meanCPrime := (cPrime1 + cPrime2) * 0.5

	var meanHPrime float64
	switch {
	case cPrime1*cPrime2 == 0:
		meanHPrime = hPrime1 + hPrime2
	case math.Abs(hPrime1-hPrime2) <= math.Pi+hueBoundaryTolerance:
		meanHPrime = (hPrime1 + hPrime2) * 0.5
	case hPrime1+hPrime2 < 2*math.Pi:
		meanHPrime = (hPrime1 + hPrime2 + 2*math.Pi) * 0.5
	default:
		meanHPrime = (hPrime1 + hPrime2 - 2*math.Pi) * 0.5
	}

	hRad := meanHPrime
	t := 1.0 - 0.17*math.Cos(hRad-30.0*degToRad) +
		0.24*math.Cos(2.0*hRad) +
		0.32*math.Cos(3.0*hRad+6.0*degToRad) -
		0.20*math.Cos(4.0*hRad-63.0*degToRad)

	// The formula constants here are defined in degrees, so convert meanHPrime for the calc
	dtCalc := (meanHPrime/degToRad - 275.0) / 25.0
	deltaTheta := 30.0 * math.Exp(-(dtCalc * dtCalc)) // replaced math.Pow(..., 2) with dtCalc*dtCalc for speed

	// explicit multiplication is significantly faster than math.Pow(x, 7)
	meanCP2 := meanCPrime * meanCPrime
	meanCP4 := meanCP2 * meanCP2
	meanCPrime7 := meanCP4 * meanCP2 * meanCPrime
	rc := 2.0 * math.Sqrt(meanCPrime7/(meanCPrime7+e7))

	rt := -rc * math.Sin(2.0*deltaTheta*degToRad)

	// replaced math.Pow(..., 2) with explicit multiplication for speed
	mL50 := meanL - 50.0
	mL50Sq := mL50 * mL50
	sl := 1.0 + (0.015*mL50Sq)/math.Sqrt(20.0+mL50Sq)
	sc := 1.0 + 0.045*meanCPrime
	sh := 1.0 + 0.015*meanCPrime*t

	// replaced math.Pow(..., 2) with explicit multiplications for speed
	dLsl := deltaLPrime / sl
	dCsc := deltaCPrime / sc
	dHsh := deltaHPrime / sh
	valSq := (dLsl * dLsl) +
		(dCsc * dCsc) +
		(dHsh * dHsh) +
		rt*dCsc*dHsh

	if valSq < 0 {
		return 0
	}
	return math.Sqrt(valSq)
}

// rgbToLab converts encoded sRGB to CIE Lab using the standard piecewise sRGB
// transfer, a D65 XYZ intermediate, Bradford adaptation, and the D50 Lab white.
func rgbToLab(r, g, b uint8) (float64, float64, float64) {
	lutRgbToLabOnce.Do(func() {
		for i := 0; i < 256; i++ {
			encoded := float64(i) / 255.0
			if encoded <= 0.04045 {
				lutRgbToLab[i] = encoded / 12.92
			} else {
				lutRgbToLab[i] = math.Pow((encoded+0.055)/1.055, 2.4)
			}
		}
	})

	// 1. Linearize RGB
	rf := lutRgbToLab[r]
	gf := lutRgbToLab[g]
	bf := lutRgbToLab[b]

	// Linear sRGB to CIE XYZ D65.
	x65 := rf*0.4123907992659595 + gf*0.3575843393838780 + bf*0.1804807884018343
	y65 := rf*0.2126390058715104 + gf*0.7151686787677560 + bf*0.0721923153607337
	z65 := rf*0.0193308187155919 + gf*0.1191947797946260 + bf*0.9505321522496607

	// Bradford chromatic adaptation from D65 to D50.
	x50 := x65*1.0479298208405488 + y65*0.0229467933410191 - z65*0.0501922295431356
	y50 := x65*0.0296278156881593 + y65*0.9904344845732490 - z65*0.0170738250293851
	z50 := -x65*0.0092430581525912 + y65*0.0150551448965779 + z65*0.7518742814281371

	// CIE Lab D50 reference white.
	fx := labTransfer(x50 / 0.96422)
	fy := labTransfer(y50)
	fz := labTransfer(z50 / 0.82521)

	l := 116.0*fy - 16.0
	a := 500.0 * (fx - fy)
	bLab := 200.0 * (fy - fz)

	return l, a, bLab
}

func labTransfer(value float64) float64 {
	const epsilon = 216.0 / 24389.0
	const kappa = 24389.0 / 27.0
	if value > epsilon {
		return math.Cbrt(value)
	}
	return (kappa*value + 16.0) / 116.0
}
