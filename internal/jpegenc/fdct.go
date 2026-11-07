// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Forked from Go 1.27 src/image/jpeg/dct.go. Changes: only the forward DCT is
// kept, and constant names use Go camel case. The arithmetic is unchanged, so
// coefficients match the standard library encoder bit for bit.

package jpegenc

// The forward DCT uses the algorithm from Christoph Loeffler, Adriaan
// Lightenberg, and George S. Mostchytz, "Practical Fast 1-D DCT Algorithms
// with 11 Multiplications," ICASSP 1989. Each 1D pass runs four stages of
// butterflies, rotations ("boxes"), and √2 scalings over eight values, and the
// 2D transform applies the 1D transform to the columns and then the rows.
//
// The notation "QN.M" below refers to a signed fixed-point value of 1+N+M
// significant bits, one of which is the sign bit and M of which hold
// fractional precision. "UQN.M" is the unsigned equivalent.

// block is an 8x8 input to the 2D FDCT. The inputs are 8-bit samples and the
// outputs fit in int16, but int32 intermediate storage keeps the fixed-point
// arithmetic simple.
type block [blockSize]int32

const blockSize = 8 * 8

// These are 60-bit precision fixed-point constants; c rounds one to the given
// number of bits. Calls with constant arguments are inlined and folded.
const (
	cos1         = 1130768441178740757 // cos 1*pi/16
	sin1         = 224923827593068887  // sin 1*pi/16
	cos3         = 958619196450722178  // cos 3*pi/16
	sin3         = 640528868967736374  // sin 3*pi/16
	sqrt2        = 1630477228166597777 // sqrt 2
	sqrt2TimesC6 = 623956622067911264  // (sqrt 2)*cos 6*pi/16
	sqrt2TimesS6 = 1506364539328854985 // (sqrt 2)*sin 6*pi/16
)

func c(x uint64, bits int) int32 {
	return int32((x + (1 << (59 - bits))) >> (60 - bits)) //nolint:gosec // 60-bit constants rounded to at most 18 bits
}

// dctBox implements a 3-multiply, 3-add rotation and scaling. Given x0, x1,
// k*cos θ, and k*sin θ, it returns the rotated and scaled coordinates.
func dctBox(x0, x1, kcos, ksin int32) (y0, y1 int32) {
	ksum := kcos * (x0 + x1)
	y0 = ksum + (ksin-kcos)*x1
	y1 = ksum - (kcos+ksin)*x0
	return y0, y1
}

// fdct implements the forward DCT. Inputs are UQ8.0; outputs are Q13.0, which
// is eight times the orthonormal DCT-II coefficient.
func fdct(b *block) {
	fdctCols(b)
	fdctRows(b)
}

// fdctCols applies the 1D DCT to the columns of b. Inputs are UQ8.0 in
// [0,255] but interpreted as [-128,127]. Outputs are Q10.18.
func fdctCols(b *block) {
	for i := range 8 {
		x0 := b[0*8+i]
		x1 := b[1*8+i]
		x2 := b[2*8+i]
		x3 := b[3*8+i]
		x4 := b[4*8+i]
		x5 := b[5*8+i]
		x6 := b[6*8+i]
		x7 := b[7*8+i]

		// Stage 1: four butterflies. x[0123] become UQ9.0 and x[4567] Q8.0.
		x0, x7 = x0+x7, x0-x7
		x1, x6 = x1+x6, x1-x6
		x2, x5 = x2+x5, x2-x5
		x3, x4 = x3+x4, x3-x4

		// Stage 2: two boxes and two butterflies. x[4567] become Q9.18,
		// x[01] UQ10.0, and x[23] Q9.0.
		x4, x7 = dctBox(x4, x7, c(cos3, 18), c(sin3, 18))
		x5, x6 = dctBox(x5, x6, c(cos1, 18), c(sin1, 18))
		x0, x3 = x0+x3, x0-x3
		x1, x2 = x1+x2, x1-x2

		// Stage 3: one box and three butterflies. x[23] become Q10.18, x0
		// UQ11.0, and x1 Q10.0.
		x2, x3 = dctBox(x2, x3, c(sqrt2TimesC6, 18), c(sqrt2TimesS6, 18))
		x0, x1 = x0+x1, x0-x1

		// The +128 bias of every input cancels except in the DC term, so
		// subtracting 128*8 here level-shifts the whole block.
		b[0*8+i] = (x0 - 128*8) << 18
		b[4*8+i] = x1 << 18
		b[2*8+i] = x2
		b[6*8+i] = x3

		x4, x6 = x4+x6, x4-x6
		x7, x5 = x7+x5, x7-x5

		// Stage 4: two √2 scalings and one butterfly. x[4567] stay Q10.18
		// with magnitude below 925 (255 times 3.6246, the largest gain).
		x5 = (x5 >> 12) * c(sqrt2, 12)
		x6 = (x6 >> 12) * c(sqrt2, 12)
		x7, x4 = x7+x4, x7-x4

		b[1*8+i] = x7
		b[3*8+i] = x5
		b[5*8+i] = x6
		b[7*8+i] = x4
	}
}

// fdctRows applies the 1D DCT to the rows of b. Inputs are Q10.18; outputs
// are Q13.0.
func fdctRows(b *block) {
	for i := range 8 {
		x := b[8*i : 8*i+8 : 8*i+8]
		x0 := x[0]
		x1 := x[1]
		x2 := x[2]
		x3 := x[3]
		x4 := x[4]
		x5 := x[5]
		x6 := x[6]
		x7 := x[7]

		// Stage 1: four butterflies. Values become Q11.18.
		x0, x7 = x0+x7, x0-x7
		x1, x6 = x1+x6, x1-x6
		x2, x5 = x2+x5, x2-x5
		x3, x4 = x3+x4, x3-x4

		// Stage 2: two boxes and two butterflies. Values become Q12.18.
		x4, x7 = dctBox(x4>>14, x7>>14, c(cos3, 14), c(sin3, 14))
		x5, x6 = dctBox(x5>>14, x6>>14, c(cos1, 14), c(sin1, 14))
		x0, x3 = x0+x3, x0-x3
		x1, x2 = x1+x2, x1-x2

		// Stage 3: one box and three butterflies. Values become Q13.18.
		x2, x3 = dctBox(x2>>14, x3>>14, c(sqrt2TimesC6, 14), c(sqrt2TimesS6, 14))
		x0, x1 = x0+x1, x0-x1
		x4, x6 = x4+x6, x4-x6
		x7, x5 = x7+x5, x7-x5

		// Stage 4: two √2 scalings and one butterfly. x[4567] stay below
		// 7397 in magnitude (2040 times 3.6246).
		x5 = (x5 >> 14) * c(sqrt2, 14)
		x6 = (x6 >> 14) * c(sqrt2, 14)
		x7, x4 = x7+x4, x7-x4

		// Round from Q13.18 to Q13.0.
		x0 = (x0 + 1<<17) >> 18
		x1 = (x1 + 1<<17) >> 18
		x2 = (x2 + 1<<17) >> 18
		x3 = (x3 + 1<<17) >> 18
		x4 = (x4 + 1<<17) >> 18
		x5 = (x5 + 1<<17) >> 18
		x6 = (x6 + 1<<17) >> 18
		x7 = (x7 + 1<<17) >> 18

		// Stores are grouped at the end so adjacent writes can merge.
		x[0] = x0
		x[1] = x7
		x[2] = x2
		x[3] = x5
		x[4] = x1
		x[5] = x6
		x[6] = x3
		x[7] = x4
	}
}
