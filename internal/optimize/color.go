package optimize

import (
	"math"
	"sync"
)

var (
	lutRgbToLab     [256]float64
	lutRgbToLabOnce sync.Once
)

// ciede2000 calculates the exact CIE 2000 color-difference standard between two CIELAB colors.
//

func ciede2000(l1, a1, b1, l2, a2, b2 float64) float64 {
	c1 := math.Sqrt(a1*a1 + b1*b1)
	c2 := math.Sqrt(a2*a2 + b2*b2)
	meanC := (c1 + c2) / 2.0

	// explicit multiplication is significantly faster than math.Pow(x, 7)
	meanC2 := meanC * meanC
	meanC4 := meanC2 * meanC2
	meanC7 := meanC4 * meanC2 * meanC
	const e7 = 6103515625.0 // 25^7
	g := 0.5 * (1.0 - math.Sqrt(meanC7/(meanC7+e7)))

	aPrime1 := (1.0 + g) * a1
	aPrime2 := (1.0 + g) * a2

	cPrime1 := math.Sqrt(aPrime1*aPrime1 + b1*b1)
	cPrime2 := math.Sqrt(aPrime2*aPrime2 + b2*b2)

	radToDeg := func(r float64) float64 {
		d := r * 180.0 / math.Pi
		if d < 0 {
			d += 360.0
		}
		return d
	}

	var hPrime1, hPrime2 float64
	if aPrime1 != 0 || b1 != 0 {
		hPrime1 = radToDeg(math.Atan2(b1, aPrime1))
	}
	if aPrime2 != 0 || b2 != 0 {
		hPrime2 = radToDeg(math.Atan2(b2, aPrime2))
	}

	deltaLPrime := l2 - l1
	deltaCPrime := cPrime2 - cPrime1
	deltaHPrime := calculateDeltaHPrime(cPrime1, cPrime2, hPrime1, hPrime2)

	meanL := (l1 + l2) / 2.0
	meanCPrime := (cPrime1 + cPrime2) / 2.0
	meanHPrime := calculateMeanHPrime(cPrime1, cPrime2, hPrime1, hPrime2)

	t := 1.0 - 0.17*math.Cos((meanHPrime-30.0)*math.Pi/180.0) +
		0.24*math.Cos(2.0*meanHPrime*math.Pi/180.0) +
		0.32*math.Cos((3.0*meanHPrime+6.0)*math.Pi/180.0) -
		0.20*math.Cos((4.0*meanHPrime-63.0)*math.Pi/180.0)

	dtCalc := (meanHPrime - 275.0) / 25.0
	deltaTheta := 30.0 * math.Exp(-(dtCalc * dtCalc)) // replaced math.Pow(..., 2) with dtCalc*dtCalc for speed

	// explicit multiplication is significantly faster than math.Pow(x, 7)
	meanCP2 := meanCPrime * meanCPrime
	meanCP4 := meanCP2 * meanCP2
	meanCPrime7 := meanCP4 * meanCP2 * meanCPrime
	rc := 2.0 * math.Sqrt(meanCPrime7/(meanCPrime7+e7))

	rt := -rc * math.Sin(2.0*deltaTheta*math.Pi/180.0)

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

// calculateDeltaHPrime computes delta H prime for CIEDE2000 color difference.
func calculateDeltaHPrime(cPrime1, cPrime2, hPrime1, hPrime2 float64) float64 {
	if cPrime1*cPrime2 == 0 {
		return 0
	}
	deltahPrime := hPrime2 - hPrime1
	if math.Abs(deltahPrime) > 180.0 {
		if hPrime2 > hPrime1 {
			deltahPrime -= 360.0
		} else {
			deltahPrime += 360.0
		}
	}
	return 2.0 * math.Sqrt(cPrime1*cPrime2) * math.Sin(deltahPrime*math.Pi/360.0)
}

// calculateMeanHPrime computes mean H prime for CIEDE2000 color difference.
func calculateMeanHPrime(cPrime1, cPrime2, hPrime1, hPrime2 float64) float64 {
	if cPrime1*cPrime2 == 0 {
		return hPrime1 + hPrime2
	}
	if math.Abs(hPrime1-hPrime2) <= 180.0 {
		return (hPrime1 + hPrime2) / 2.0
	}
	if hPrime1+hPrime2 < 360.0 {
		return (hPrime1 + hPrime2 + 360.0) / 2.0
	}
	return (hPrime1 + hPrime2 - 360.0) / 2.0
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
