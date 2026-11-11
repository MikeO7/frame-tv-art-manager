// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Forked from Go 1.27 src/image/jpeg/writer.go. Changes: only *image.RGBA
// sources, the one kind this application encodes, always as Y, Cb, and Cr
// with caller-supplied options; 4:4:4 chroma
// sampling (the new default) alongside the original 4:2:0; a two-pass encode
// that gathers symbol statistics and writes optimized Huffman tables (JPEG
// Annex K.2) instead of the fixed Annex K.3 tables; package tables expressed
// as constants; and quantization split from entropy coding, with the
// quantized coefficients kept between the passes so color conversion, the
// FDCT, and quantization run once. Output stays baseline sequential (SOF0).

// Package jpegenc encodes baseline JPEG images with optional full-resolution
// chroma and per-image optimized Huffman tables.
package jpegenc

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	"io"
	"math/bits"
)

// nComponent is the number of components in every frame: Y, Cb, and Cr.
const nComponent = 3

// Subsampling selects how chroma is sampled relative to luma.
type Subsampling int

const (
	// Subsampling444 keeps full-resolution chroma: one Cb and one Cr block
	// for every luma block. It is the zero value and the default.
	Subsampling444 Subsampling = iota
	// Subsampling420 halves chroma resolution in both directions, as the
	// standard library encoder always does.
	Subsampling420
)

// String returns the conventional J:a:b name without separators.
func (s Subsampling) String() string {
	switch s {
	case Subsampling444:
		return "444"
	case Subsampling420:
		return "420"
	default:
		return fmt.Sprintf("Subsampling(%d)", int(s))
	}
}

// ParseSubsampling converts "444" or "420" to a Subsampling.
func ParseSubsampling(name string) (Subsampling, error) {
	switch name {
	case "444":
		return Subsampling444, nil
	case "420":
		return Subsampling420, nil
	default:
		return 0, fmt.Errorf("jpegenc: unknown chroma subsampling %q", name)
	}
}

// Options are the encoding parameters. Quality ranges from 1 to 100
// inclusive, higher is better; values outside that range are clamped.
type Options struct {
	Quality     int
	Subsampling Subsampling
}

const (
	sof0Marker = 0xc0 // Start Of Frame (Baseline Sequential).
	dhtMarker  = 0xc4 // Define Huffman Table.
	soiMarker  = 0xd8 // Start Of Image.
	eoiMarker  = 0xd9 // End Of Image.
	sosMarker  = 0xda // Start Of Scan.
	dqtMarker  = 0xdb // Define Quantization Table.
)

type quantIndex int

const (
	quantIndexLuminance quantIndex = iota
	quantIndexChrominance
	nQuantIndex
)

// unscaledQuant holds the Annex K.1 quantization tables in zig-zag order.
// Each encoder scales a copy according to its quality parameter.
const unscaledQuant = "" +
	// Luminance.
	"\x10\x0b\x0c\x0e\x0c\x0a\x10\x0e\x0d\x0e\x12\x11\x10\x13\x18\x28" +
	"\x1a\x18\x16\x16\x18\x31\x23\x25\x1d\x28\x3a\x33\x3d\x3c\x39\x33" +
	"\x38\x37\x40\x48\x5c\x4e\x40\x44\x57\x45\x37\x38\x50\x6d\x51\x57" +
	"\x5f\x62\x67\x68\x67\x3e\x4d\x71\x79\x70\x64\x78\x5c\x65\x67\x63" +
	// Chrominance.
	"\x11\x12\x12\x18\x15\x18\x2f\x1a\x1a\x2f\x63\x42\x38\x42\x63\x63" +
	"\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63" +
	"\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63" +
	"\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63\x63"

// unzig maps zig-zag order to natural order.
const unzig = "" +
	"\x00\x01\x08\x10\x09\x02\x03\x0a\x11\x18\x20\x19\x12\x0b\x04\x05" +
	"\x0c\x13\x1a\x21\x28\x30\x29\x22\x1b\x14\x0d\x06\x07\x0e\x15\x1c" +
	"\x23\x2a\x31\x38\x39\x32\x2b\x24\x1d\x16\x0f\x17\x1e\x25\x2c\x33" +
	"\x3a\x3b\x34\x2d\x26\x1f\x27\x2e\x35\x3c\x3d\x36\x2f\x37\x3e\x3f"

// writer is a buffered writer.
type writer interface {
	Flush() error
	io.Writer
	io.ByteWriter
}

// encoder encodes an image to the JPEG format.
type encoder struct {
	// w is the writer to write to. err is the first error encountered during
	// writing. All attempted writes after the first error become no-ops.
	w   writer
	err error
	// buf is a scratch buffer.
	buf [16]byte
	// bits and nBits are accumulated bits to write to w.
	bits, nBits uint32
	// quant is the scaled quantization tables, in zig-zag order, and divisor
	// is eight times quant to match the FDCT output scale.
	quant   [nQuantIndex][blockSize]byte
	divisor [nQuantIndex][blockSize]int32
	// subsampling describes the frame layout.
	subsampling Subsampling
	// prevDC holds the last DC value of each component for delta coding.
	prevDC [3]int32
	// freq counts symbol use per table during the statistics pass.
	freq [nHuffIndex][symbolCount]uint64
	// spec and lut are the Huffman tables written to DHT and used to code.
	spec [nHuffIndex]huffmanSpec
	lut  [nHuffIndex]huffmanLUT
	// code is scratch space for the symbols of one block.
	code blockCode
}

// Encode writes m to w as a baseline three-component JPEG. It converts,
// transforms, and quantizes m once, collecting symbol statistics as it goes,
// and then entropy-codes the kept coefficients with Huffman tables built from
// those statistics. WorkingBytes is what keeping them costs.
func Encode(w io.Writer, m *image.RGBA, o Options) error {
	e, err := newEncoder(w, m, o)
	if err != nil {
		return err
	}
	coefficients := e.quantizeImage(m)
	e.buildOptimalTables()
	return e.writeImage(m.Bounds().Size(), coefficients)
}

// quantizeImage converts, transforms, and quantizes m once. It counts the
// symbols of every block for the Huffman tables and returns the quantized
// coefficients of all blocks in scan order, 64 to a block in zig-zag order,
// for writeImage to entropy-code.
func (e *encoder) quantizeImage(m *image.RGBA) []int16 {
	coefficients := make([]int16, blockCount(m.Bounds().Dx(), m.Bounds().Dy(), e.subsampling)*blockSize)
	next := coefficients
	e.scan(m, func(comp int, zz *block) {
		e.countBlock(comp, zz)
		stored := (*[blockSize]int16)(next)
		for zig, coefficient := range zz {
			stored[zig] = int16(coefficient) //nolint:gosec // baseline 8-bit coefficients fit 12 bits
		}
		next = next[blockSize:]
	})
	return coefficients
}

// blockCount is the number of blocks a scan of a width x height image passes
// to its sink: six per 16x16 MCU with 4:2:0 chroma, otherwise three per 8x8.
func blockCount(width, height int, subsampling Subsampling) int {
	if subsampling == Subsampling420 {
		return ((width + 15) / 16) * ((height + 15) / 16) * 6
	}
	return ((width + 7) / 8) * ((height + 7) / 8) * nComponent
}

// WorkingBytes is the memory Encode holds for a width x height image beyond
// the image itself: the quantized coefficients of every block, kept between
// the statistics pass and the output pass.
func WorkingBytes(width, height int, subsampling Subsampling) int64 {
	return int64(blockCount(width, height, subsampling)) * blockSize * 2
}

func newEncoder(w io.Writer, m *image.RGBA, o Options) (*encoder, error) {
	b := m.Bounds()
	if b.Dx() >= 1<<16 || b.Dy() >= 1<<16 {
		return nil, errors.New("jpegenc: image is too large to encode")
	}
	if b.Empty() {
		return nil, errors.New("jpegenc: image is empty")
	}
	e := &encoder{subsampling: o.Subsampling}
	if e.subsampling != Subsampling444 && e.subsampling != Subsampling420 {
		return nil, fmt.Errorf("jpegenc: unsupported chroma subsampling %v", e.subsampling)
	}
	e.initQuant(min(max(o.Quality, 1), 100))
	if ww, ok := w.(writer); ok {
		e.w = ww
	} else {
		e.w = bufio.NewWriterSize(w, 64<<10)
	}
	return e, nil
}

// initQuant converts a quality rating to a scaling factor and scales the
// Annex K.1 tables by it.
func (e *encoder) initQuant(quality int) {
	scale := 200 - quality*2
	if quality < 50 {
		scale = 5000 / quality
	}
	for i := range e.quant {
		for j := range e.quant[i] {
			x := int(unscaledQuant[i*blockSize+j])
			x = min(max((x*scale+50)/100, 1), 255)
			e.quant[i][j] = uint8(x)
			e.divisor[i][j] = 8 * int32(x)
		}
	}
}

// buildOptimalTables derives the Huffman tables from the statistics pass.
func (e *encoder) buildOptimalTables() {
	for i := range e.spec {
		e.spec[i] = optimalHuffmanSpec(&e.freq[i])
		e.lut[i].init(&e.spec[i])
	}
}

// writeImage writes the complete JPEG stream for a frame of the given size,
// entropy-coding coefficients, as quantizeImage returns them, with the
// current tables.
func (e *encoder) writeImage(size image.Point, coefficients []int16) error {
	e.writeMarker(soiMarker)
	e.writeDQT()
	e.writeSOF0(size)
	e.writeDHT()
	e.writeSOSHeader()
	e.prevDC = [3]int32{}
	components := "\x00\x01\x02"
	if e.subsampling == Subsampling420 {
		components = "\x00\x00\x00\x00\x01\x02"
	}
	var zz block
	for index := 0; len(coefficients) >= blockSize; index++ {
		for zig, coefficient := range (*[blockSize]int16)(coefficients) {
			zz[zig] = int32(coefficient)
		}
		coefficients = coefficients[blockSize:]
		e.emitBlock(int(components[index%len(components)]), &zz)
	}
	// Pad the last byte with 1's.
	e.emit(0x7f, 7)
	e.writeMarker(eoiMarker)
	e.flush()
	return e.err
}

func (e *encoder) flush() {
	if e.err != nil {
		return
	}
	e.err = e.w.Flush()
}

func (e *encoder) write(p []byte) {
	if e.err != nil {
		return
	}
	_, e.err = e.w.Write(p)
}

func (e *encoder) writeByte(b byte) {
	if e.err != nil {
		return
	}
	e.err = e.w.WriteByte(b)
}

// emit emits the least significant nBits bits of bits to the bit-stream.
// The precondition is bits < 1<<nBits && nBits <= 16.
func (e *encoder) emit(bits, nBits uint32) {
	nBits += e.nBits
	bits <<= 32 - nBits
	bits |= e.bits
	for nBits >= 8 {
		b := uint8(bits >> 24)
		e.writeByte(b)
		if b == 0xff {
			e.writeByte(0x00)
		}
		bits <<= 8
		nBits -= 8
	}
	e.bits, e.nBits = bits, nBits
}

// writeMarker writes a two-byte marker with no payload.
func (e *encoder) writeMarker(marker uint8) {
	e.buf[0] = 0xff
	e.buf[1] = marker
	e.write(e.buf[:2])
}

// writeMarkerHeader writes the header for a marker with the given length.
func (e *encoder) writeMarkerHeader(marker uint8, markerlen int) {
	e.buf[0] = 0xff
	e.buf[1] = marker
	e.buf[2] = uint8(markerlen >> 8) //nolint:gosec // marker segments are shorter than 64 KiB
	e.buf[3] = uint8(markerlen & 0xff)
	e.write(e.buf[:4])
}

// writeDQT writes the Define Quantization Table marker.
func (e *encoder) writeDQT() {
	e.writeMarkerHeader(dqtMarker, 2+int(nQuantIndex)*(1+blockSize))
	for i := range int(nQuantIndex) {
		e.writeByte(uint8(i))
		e.write(e.quant[i][:])
	}
}

// writeSOF0 writes the Start Of Frame (Baseline Sequential) marker. Each
// component entry is its identifier, its horizontal and vertical sampling
// factors packed into one byte, and its quantization table.
func (e *encoder) writeSOF0(size image.Point) {
	e.writeMarkerHeader(sof0Marker, 8+3*nComponent)
	e.buf[0] = 8                  // 8-bit color.
	e.buf[1] = uint8(size.Y >> 8) //nolint:gosec // dimensions are below 1<<16
	e.buf[2] = uint8(size.Y & 0xff)
	e.buf[3] = uint8(size.X >> 8) //nolint:gosec // dimensions are below 1<<16
	e.buf[4] = uint8(size.X & 0xff)
	e.buf[5] = nComponent
	lumaSampling := byte(0x11)
	if e.subsampling == Subsampling420 {
		lumaSampling = 0x22
	}
	for i := range nComponent {
		e.buf[3*i+6] = uint8(i + 1)
		e.buf[3*i+7] = 0x11
		e.buf[3*i+8] = 0x01
	}
	e.buf[7] = lumaSampling
	e.buf[8] = 0x00
	e.write(e.buf[:6+3*nComponent])
}

// writeDHT writes one Define Huffman Table marker holding every table the
// frame uses. Table classes and IDs are 0x00 (luma DC), 0x10 (luma AC),
// 0x01 (chroma DC), and 0x11 (chroma AC).
func (e *encoder) writeDHT() {
	specs := e.spec[:]
	markerlen := 2
	for i := range specs {
		markerlen += 1 + maxCodeLength + len(specs[i].value)
	}
	e.writeMarkerHeader(dhtMarker, markerlen)
	for i := range specs {
		e.writeByte("\x00\x10\x01\x11"[i])
		e.write(specs[i].count[:])
		e.write(specs[i].value)
	}
}

// writeSOSHeader writes the Start Of Scan marker. Each component names its
// DC and AC tables, followed by the spectral selection bytes 0x00, 0x3f, and
// 0x00 that section B.2.3 requires for sequential DCT.
func (e *encoder) writeSOSHeader() {
	e.write([]byte{
		0xff, sosMarker, 0x00, 0x0c, 0x03, 0x01, 0x00, 0x02,
		0x11, 0x03, 0x11, 0x00, 0x3f, 0x00,
	})
}

// blockCode is one block's entropy-coding symbols: the Huffman symbol and
// the extra magnitude bits that follow it. A block has at most one DC
// symbol, 63 AC symbols, and an EOB, and ZRL runs only replace zeros.
type blockCode struct {
	n      int
	table  [blockSize + 1]huffIndex
	symbol [blockSize + 1]uint8
	extra  [blockSize + 1]uint32
	nExtra [blockSize + 1]uint8
}

func (c *blockCode) add(table huffIndex, symbol uint8, extra uint32, nExtra uint8) {
	c.table[c.n] = table
	c.symbol[c.n] = symbol
	c.extra[c.n] = extra
	c.nExtra[c.n] = nExtra
	c.n++
}

// addValue appends a run/size symbol and the value's magnitude bits, coded
// as in section F.1.2.1: negative values are stored as value-1 in nBits.
func (c *blockCode) addValue(table huffIndex, runLength, value int32) {
	a, b := value, value
	if a < 0 {
		a, b = -value, value-1
	}
	nBits := uint32(bits.Len32(uint32(a)))                     //nolint:gosec // a is non-negative
	symbol := uint8(runLength<<4) | uint8(nBits)               //nolint:gosec // run < 16 and size <= 11
	c.add(table, symbol, uint32(b)&(1<<nBits-1), uint8(nBits)) //nolint:gosec // masked to nBits
}

// codeBlock fills e.code with the symbols for zz, a quantized block in
// zig-zag order belonging to component comp, and updates the DC predictor.
func (e *encoder) codeBlock(comp int, zz *block) {
	dcTable, acTable := huffIndexLuminanceDC, huffIndexLuminanceAC
	if comp > 0 {
		dcTable, acTable = huffIndexChrominanceDC, huffIndexChrominanceAC
	}
	c := &e.code
	c.n = 0
	c.addValue(dcTable, 0, zz[0]-e.prevDC[comp])
	e.prevDC[comp] = zz[0]
	runLength := int32(0)
	for zig := 1; zig < blockSize; zig++ {
		ac := zz[zig]
		if ac == 0 {
			runLength++
			continue
		}
		for runLength > 15 {
			c.add(acTable, 0xf0, 0, 0)
			runLength -= 16
		}
		c.addValue(acTable, runLength, ac)
		runLength = 0
	}
	if runLength > 0 {
		c.add(acTable, 0x00, 0, 0)
	}
}

// countBlock records the symbols of one block for the statistics pass.
func (e *encoder) countBlock(comp int, zz *block) {
	e.codeBlock(comp, zz)
	c := &e.code
	for i := range c.n {
		e.freq[c.table[i]][c.symbol[i]]++
	}
}

// emitBlock writes the entropy-coded symbols of one block.
func (e *encoder) emitBlock(comp int, zz *block) {
	e.codeBlock(comp, zz)
	c := &e.code
	for i := range c.n {
		x := e.lut[c.table[i]][c.symbol[i]]
		e.emit(x&(1<<24-1), x>>24)
		if c.nExtra[i] > 0 {
			e.emit(c.extra[i], uint32(c.nExtra[i]))
		}
	}
}
