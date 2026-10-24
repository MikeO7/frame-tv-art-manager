package optimize

import (
	"math"
	"sync"
)

// srgbEncodeBuckets sizes the coarse index into the byte thresholds. Adjacent
// encoded bytes are at least 1/3300 apart in linear light, so a 4096-bucket
// starting guess needs at most a few threshold comparisons.
const srgbEncodeBuckets = 4096

// srgbTables holds exact sRGB transfer lookups for 8-bit samples.
//
// decode[i] equals srgbDecode(i/255) bit for bit. threshold[b] is the smallest
// linear value whose rounded encoding is byte b, so counting the thresholds at
// or below a value reproduces uint8(math.Round(srgbEncode(v)*255)) without a
// per-sample math.Pow.
type srgbTables struct {
	decode    [256]float64
	decode32  [256]float32
	threshold [256]float64
	bucket    [srgbEncodeBuckets + 1]uint8
}

//nolint:gochecknoglobals // immutable transfer tables are computed once and shared by every transform
var srgbLookup = sync.OnceValue(newSRGBTables)

func newSRGBTables() *srgbTables {
	tables := &srgbTables{}
	for value := range tables.decode {
		tables.decode[value] = srgbDecode(float64(value) / 255)
		tables.decode32[value] = float32(tables.decode[value])
	}
	tables.threshold[0] = math.Inf(-1)
	for target := 1; target < 256; target++ {
		tables.threshold[target] = srgbEncodeThreshold(uint8(target))
	}
	encoded := uint8(0)
	for index := range tables.bucket {
		linear := float64(index) / srgbEncodeBuckets
		for encoded < 255 && linear >= tables.threshold[encoded+1] {
			encoded++
		}
		tables.bucket[index] = encoded
	}
	return tables
}

// srgbEncodeThreshold bisects to the smallest float64 whose rounded sRGB
// encoding reaches target. Bisection over the exact function keeps the table
// consistent with srgbEncode rather than with an analytic inverse.
func srgbEncodeThreshold(target uint8) float64 {
	low, high := 0.0, 1.0
	for {
		middle := low + (high-low)/2
		if middle <= low || middle >= high {
			return high
		}
		if roundedSRGBByte(middle) >= target {
			high = middle
		} else {
			low = middle
		}
	}
}

func roundedSRGBByte(linear float64) uint8 {
	return uint8(math.Round(clampUnit(srgbEncode(linear)) * 255))
}

// encode returns uint8(math.Round(clampUnit(srgbEncode(linear))*255)).
// Non-finite or non-positive input encodes to zero.
func (tables *srgbTables) encode(linear float64) uint8 {
	if !(linear > 0) {
		return 0
	}
	if linear >= 1 {
		return 255
	}
	encoded := tables.bucket[int(linear*srgbEncodeBuckets)]
	for encoded < 255 && linear >= tables.threshold[encoded+1] {
		encoded++
	}
	return encoded
}

func srgbDecode(encoded float64) float64 {
	if encoded <= 0.04045 {
		return encoded / 12.92
	}
	return math.Pow((encoded+0.055)/1.055, 2.4)
}

func srgbEncode(linear float64) float64 {
	if linear <= 0.0031308 {
		return linear * 12.92
	}
	return 1.055*math.Pow(linear, 1.0/2.4) - 0.055
}
