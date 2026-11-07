// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Forked from Go 1.27 src/image/jpeg/writer.go. Changes: the block walk is
// shared by the statistics and output passes through a callback, 4:4:4 MCUs
// are added beside the original 4:2:0 ones, and quantization is separated
// from entropy coding.

package jpegenc

import (
	"image"
	"image/color"
)

// blockSink consumes one quantized block, in zig-zag order, of component
// comp (0 for Y, 1 for Cb, 2 for Cr).
type blockSink func(comp int, zz *block)

// div returns a/b rounded to the nearest integer, instead of rounded to zero.
func div(a, b int32) int32 {
	if a >= 0 {
		return (a + (b >> 1)) / b
	}
	return -((-a + (b >> 1)) / b)
}

// quantize transforms the natural-order samples in b and writes the
// quantized coefficients to zz in zig-zag order.
func (e *encoder) quantize(b *block, q quantIndex, zz *block) {
	fdct(b)
	d := &e.divisor[q]
	for zig := range blockSize {
		zz[zig] = div(b[unzig[zig]], d[zig])
	}
}

// scan walks m in MCU order and passes every quantized block to sink.
func (e *encoder) scan(m image.Image, sink blockSink) {
	if gray, ok := m.(*image.Gray); ok {
		e.scanGray(gray, sink)
		return
	}
	if e.subsampling == Subsampling420 {
		e.scan420(m, sink)
		return
	}
	e.scan444(m, sink)
}

func (e *encoder) scanGray(m *image.Gray, sink blockSink) {
	var b, zz block
	bounds := m.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 8 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 8 {
			grayToY(m, image.Pt(x, y), &b)
			e.quantize(&b, quantIndexLuminance, &zz)
			sink(0, &zz)
		}
	}
}

// scan444 codes one 8x8 block of each component per MCU.
func (e *encoder) scan444(m image.Image, sink blockSink) {
	var yb, cb, cr, zz block
	bounds := m.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 8 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 8 {
			toYCbCrBlocks(m, image.Pt(x, y), &yb, &cb, &cr)
			e.quantize(&yb, quantIndexLuminance, &zz)
			sink(0, &zz)
			e.quantize(&cb, quantIndexChrominance, &zz)
			sink(1, &zz)
			e.quantize(&cr, quantIndexChrominance, &zz)
			sink(2, &zz)
		}
	}
}

// scan420 codes four luma blocks and one averaged block of each chroma
// component per 16x16 MCU.
func (e *encoder) scan420(m image.Image, sink blockSink) {
	var (
		yb, zz block
		cb, cr [4]block
	)
	bounds := m.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 16 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 16 {
			for i := range 4 {
				xOff := (i & 1) * 8
				yOff := (i & 2) * 4
				toYCbCrBlocks(m, image.Pt(x+xOff, y+yOff), &yb, &cb[i], &cr[i])
				e.quantize(&yb, quantIndexLuminance, &zz)
				sink(0, &zz)
			}
			scale(&yb, &cb)
			e.quantize(&yb, quantIndexChrominance, &zz)
			sink(1, &zz)
			scale(&yb, &cr)
			e.quantize(&yb, quantIndexChrominance, &zz)
			sink(2, &zz)
		}
	}
}

// toYCbCrBlocks converts the 8x8 region of m whose top-left corner is p,
// using the fast paths for *image.RGBA and *image.YCbCr when possible.
// Pixels beyond the image edge replicate the last row or column.
func toYCbCrBlocks(m image.Image, p image.Point, yBlock, cbBlock, crBlock *block) {
	switch m := m.(type) {
	case *image.RGBA:
		rgbaToYCbCr(m, p, yBlock, cbBlock, crBlock)
	case *image.YCbCr:
		yCbCrToYCbCr(m, p, yBlock, cbBlock, crBlock)
	default:
		toYCbCr(m, p, yBlock, cbBlock, crBlock)
	}
}

// toYCbCr converts the 8x8 region of m whose top-left corner is p to its
// YCbCr values.
func toYCbCr(m image.Image, p image.Point, yBlock, cbBlock, crBlock *block) {
	b := m.Bounds()
	xmax := b.Max.X - 1
	ymax := b.Max.Y - 1
	for j := range 8 {
		for i := range 8 {
			r, g, b, _ := m.At(min(p.X+i, xmax), min(p.Y+j, ymax)).RGBA()
			yy, cb, cr := color.RGBToYCbCr(uint8(r>>8), uint8(g>>8), uint8(b>>8)) //nolint:gosec // 16-bit channels shifted to 8 bits
			yBlock[8*j+i] = int32(yy)
			cbBlock[8*j+i] = int32(cb)
			crBlock[8*j+i] = int32(cr)
		}
	}
}

// grayToY stores the 8x8 region of m whose top-left corner is p in yBlock.
func grayToY(m *image.Gray, p image.Point, yBlock *block) {
	b := m.Bounds()
	xmax := b.Max.X - 1
	ymax := b.Max.Y - 1
	pix := m.Pix
	for j := range 8 {
		for i := range 8 {
			idx := m.PixOffset(min(p.X+i, xmax), min(p.Y+j, ymax))
			yBlock[8*j+i] = int32(pix[idx])
		}
	}
}

// rgbaToYCbCr is a specialized version of toYCbCr for image.RGBA images.
func rgbaToYCbCr(m *image.RGBA, p image.Point, yBlock, cbBlock, crBlock *block) {
	b := m.Bounds()
	xmax := b.Max.X - 1
	ymax := b.Max.Y - 1
	for j := range 8 {
		sj := min(p.Y+j, ymax)
		offset := (sj-b.Min.Y)*m.Stride - b.Min.X*4
		for i := range 8 {
			sx := min(p.X+i, xmax)
			pix := m.Pix[offset+sx*4 : offset+sx*4+3 : offset+sx*4+3]
			yy, cb, cr := color.RGBToYCbCr(pix[0], pix[1], pix[2])
			yBlock[8*j+i] = int32(yy)
			cbBlock[8*j+i] = int32(cb)
			crBlock[8*j+i] = int32(cr)
		}
	}
}

// yCbCrToYCbCr is a specialized version of toYCbCr for image.YCbCr images.
func yCbCrToYCbCr(m *image.YCbCr, p image.Point, yBlock, cbBlock, crBlock *block) {
	b := m.Bounds()
	xmax := b.Max.X - 1
	ymax := b.Max.Y - 1
	for j := range 8 {
		sy := min(p.Y+j, ymax)
		for i := range 8 {
			sx := min(p.X+i, xmax)
			yi := m.YOffset(sx, sy)
			ci := m.COffset(sx, sy)
			yBlock[8*j+i] = int32(m.Y[yi])
			cbBlock[8*j+i] = int32(m.Cb[ci])
			crBlock[8*j+i] = int32(m.Cr[ci])
		}
	}
}

// scale scales the 16x16 region represented by the 4 src blocks to the 8x8
// dst block.
func scale(dst *block, src *[4]block) {
	for i := range 4 {
		dstOff := (i&2)<<4 | (i&1)<<2
		for y := range 4 {
			for x := range 4 {
				j := 16*y + 2*x
				sum := src[i][j] + src[i][j+1] + src[i][j+8] + src[i][j+9]
				dst[8*y+x+dstOff] = (sum + 2) >> 2
			}
		}
	}
}
