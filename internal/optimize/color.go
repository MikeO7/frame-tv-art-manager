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
//nolint:funlen,gocognit,gocyclo // mathematical formula requiring monolithic execution flow for performance and readability
func ciede2000(color1, color2 labColor) float64 {
	const degToRad = math.Pi / 180.0
	const e7 = 6103515625.0 // 25^7

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
		if deltahPrime > math.Pi {
			deltahPrime -= 2 * math.Pi
		} else if deltahPrime < -math.Pi {
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
	case math.Abs(hPrime1-hPrime2) <= math.Pi:
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

// rgbToLab performs a fast, simplified conversion from RGB to CIE Lab space.
func rgbToLab(r, g, b uint8) (float64, float64, float64) {
	lutRgbToLabOnce.Do(func() {
		for i := 0; i < 256; i++ {
			lutRgbToLab[i] = math.Pow(float64(i)/255.0, 2.2)
		}
	})

	// 1. Linearize RGB
	rf := lutRgbToLab[r]
	gf := lutRgbToLab[g]
	bf := lutRgbToLab[b]

	// 2. RGB to XYZ
	x := rf*0.4124 + gf*0.3576 + bf*0.1805
	y := rf*0.2126 + gf*0.7152 + bf*0.0722
	z := rf*0.0193 + gf*0.1192 + bf*0.9505

	// 3. XYZ to Lab (Simplified D65)
	fy := y
	if fy > 0.008856 {
		fy = math.Cbrt(fy)
	} else {
		fy = 7.787*fy + 0.13793103448275862
	}

	fx := x
	if fx > 0.008856 {
		fx = math.Cbrt(fx)
	} else {
		fx = 7.787*fx + 0.13793103448275862
	}

	fz := z
	if fz > 0.008856 {
		fz = math.Cbrt(fz)
	} else {
		fz = 7.787*fz + 0.13793103448275862
	}

	l := 116.0*fy - 16.0
	a := 500.0 * (fx - fy)
	bLab := 200.0 * (fy - fz)

	return l, a, bLab
}
