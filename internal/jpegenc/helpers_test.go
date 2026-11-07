// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The Annex K.3 tables below are copied from Go 1.27 src/image/jpeg/writer.go
// so tests can encode the same image with the fixed standard tables.

package jpegenc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"testing"
)

// standardSpecs returns the Annex K.3 Huffman tables used by image/jpeg.
func standardSpecs() [nHuffIndex]huffmanSpec {
	return [nHuffIndex]huffmanSpec{
		{
			count: [16]byte{0, 1, 5, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0},
			value: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		},
		{
			count: [16]byte{0, 2, 1, 3, 3, 2, 4, 3, 5, 5, 4, 4, 0, 0, 1, 125},
			value: []byte{
				0x01, 0x02, 0x03, 0x00, 0x04, 0x11, 0x05, 0x12, 0x21, 0x31, 0x41, 0x06, 0x13, 0x51, 0x61, 0x07,
				0x22, 0x71, 0x14, 0x32, 0x81, 0x91, 0xa1, 0x08, 0x23, 0x42, 0xb1, 0xc1, 0x15, 0x52, 0xd1, 0xf0,
				0x24, 0x33, 0x62, 0x72, 0x82, 0x09, 0x0a, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x25, 0x26, 0x27, 0x28,
				0x29, 0x2a, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49,
				0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
				0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89,
				0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
				0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3, 0xc4, 0xc5,
				0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xe1, 0xe2,
				0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
				0xf9, 0xfa,
			},
		},
		{
			count: [16]byte{0, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0},
			value: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		},
		{
			count: [16]byte{0, 2, 1, 2, 4, 4, 3, 4, 7, 5, 4, 4, 0, 1, 2, 119},
			value: []byte{
				0x00, 0x01, 0x02, 0x03, 0x11, 0x04, 0x05, 0x21, 0x31, 0x06, 0x12, 0x41, 0x51, 0x07, 0x61, 0x71,
				0x13, 0x22, 0x32, 0x81, 0x08, 0x14, 0x42, 0x91, 0xa1, 0xb1, 0xc1, 0x09, 0x23, 0x33, 0x52, 0xf0,
				0x15, 0x62, 0x72, 0xd1, 0x0a, 0x16, 0x24, 0x34, 0xe1, 0x25, 0xf1, 0x17, 0x18, 0x19, 0x1a, 0x26,
				0x27, 0x28, 0x29, 0x2a, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
				0x49, 0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
				0x69, 0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87,
				0x88, 0x89, 0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5,
				0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3,
				0xc4, 0xc5, 0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda,
				0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
				0xf9, 0xfa,
			},
		},
	}
}

// encodeWithStandardTables encodes m like Encode but with the Annex K.3
// tables in place of optimized ones.
func encodeWithStandardTables(t testing.TB, m image.Image, o *Options) []byte {
	t.Helper()
	var buf bytes.Buffer
	e, err := newEncoder(&buf, m, o)
	if err != nil {
		t.Fatalf("newEncoder: %v", err)
	}
	e.spec = standardSpecs()
	for i := range e.spec {
		e.lut[i].init(&e.spec[i])
	}
	if err := e.writeImage(m); err != nil {
		t.Fatalf("writeImage: %v", err)
	}
	return buf.Bytes()
}

// artwork returns a deterministic RGBA test image with smooth gradients,
// fine texture, saturated hard edges, and thin dark line work: the content
// that distinguishes chroma sampling and table choices.
func artwork(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(uint64(w), uint64(h)))
	for y := range h {
		for x := range w {
			fx, fy := float64(x)/float64(max(w, 1)), float64(y)/float64(max(h, 1))
			r := 128 + 100*math.Sin(6*fx+2*fy)
			g := 128 + 90*math.Cos(5*fy-3*fx)
			b := 128 + 80*math.Sin(9*fx*fy)
			if (x/23+y/31)%2 == 0 {
				r, g, b = 220, 30, 40
			}
			if x%37 == 0 || y%41 == 0 {
				r, g, b = 20, 20, 120
			}
			noise := float64(rng.IntN(17) - 8)
			m.SetRGBA(x, y, color.RGBA{R: clampByte(r + noise), G: clampByte(g + noise), B: clampByte(b + noise), A: 255})
		}
	}
	return m
}

// stripes returns alternating one-pixel red and blue vertical lines.
func stripes(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 230, G: 20, B: 30, A: 255}
			if x%2 == 1 {
				c = color.RGBA{R: 20, G: 40, B: 230, A: 255}
			}
			m.SetRGBA(x, y, c)
		}
	}
	return m
}

func clampByte(v float64) uint8 {
	return uint8(math.Round(math.Min(math.Max(v, 0), 255)))
}

// psnr returns the peak signal-to-noise ratio of b against a over the RGB
// channels.
func psnr(a, b image.Image) float64 {
	var sum float64
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r0, g0, b0, _ := a.At(x, y).RGBA()
			r1, g1, b1, _ := b.At(x, y).RGBA()
			for _, d := range [3]float64{
				float64(r0>>8) - float64(r1>>8), float64(g0>>8) - float64(g1>>8), float64(b0>>8) - float64(b1>>8),
			} {
				sum += d * d
			}
		}
	}
	return peakRatio(sum / float64(3*bounds.Dx()*bounds.Dy()))
}

// chromaPSNR returns the PSNR of the Cb and Cr planes of b against a.
func chromaPSNR(a, b image.Image) float64 {
	var sum float64
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, cb0, cr0 := toYCbCrColor(a.At(x, y))
			_, cb1, cr1 := toYCbCrColor(b.At(x, y))
			dcb, dcr := float64(cb0)-float64(cb1), float64(cr0)-float64(cr1)
			sum += dcb*dcb + dcr*dcr
		}
	}
	return peakRatio(sum / float64(2*bounds.Dx()*bounds.Dy()))
}

func toYCbCrColor(c color.Color) (yy, cb, cr uint8) {
	r, g, b, _ := c.RGBA()
	return color.RGBToYCbCr(uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

func peakRatio(mse float64) float64 {
	if mse == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/mse)
}

func mustDecode(t testing.TB, data []byte) image.Image {
	t.Helper()
	m, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("image/jpeg cannot decode output: %v", err)
	}
	return m
}

func mustEncode(t testing.TB, m image.Image, o *Options) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(&buf, m, o); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf.Bytes()
}

func stdlibEncode(t testing.TB, m image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("image/jpeg Encode: %v", err)
	}
	return buf.Bytes()
}

// samePixels reports whether two decoded images are identical.
func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r0, g0, b0, a0 := a.At(x, y).RGBA()
			r1, g1, b1, a1 := b.At(x, y).RGBA()
			if r0 != r1 || g0 != g1 || b0 != b1 || a0 != a1 {
				return false
			}
		}
	}
	return true
}
