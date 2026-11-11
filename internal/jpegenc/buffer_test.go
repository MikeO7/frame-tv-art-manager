package jpegenc

import (
	"bytes"
	"image"
	"testing"
)

// encodeScanningTwice encodes m the way Encode did before it kept the
// quantized coefficients: one full scan to count symbols and a second to emit
// them.
func encodeScanningTwice(t *testing.T, m *image.RGBA, o Options) []byte {
	t.Helper()
	var buf bytes.Buffer
	e, err := newEncoder(&buf, m, o)
	if err != nil {
		t.Fatalf("newEncoder: %v", err)
	}
	e.scan(m, e.countBlock)
	e.buildOptimalTables()
	e.writeMarker(soiMarker)
	e.writeDQT()
	e.writeSOF0(m.Bounds().Size())
	e.writeDHT()
	e.writeSOSHeader()
	e.prevDC = [3]int32{}
	e.scan(m, e.emitBlock)
	e.emit(0x7f, 7)
	e.writeMarker(eoiMarker)
	e.flush()
	if e.err != nil {
		t.Fatalf("encode: %v", e.err)
	}
	return buf.Bytes()
}

func TestKeptCoefficientsEncodeTheSameBytesAsASecondScan(t *testing.T) {
	t.Parallel()
	offset, isRGBA := artwork(700, 500).SubImage(image.Rect(5, 7, 603, 411)).(*image.RGBA)
	if !isRGBA {
		t.Fatal("sub-image of an RGBA image is not RGBA")
	}
	images := map[string]*image.RGBA{
		"one pixel": artwork(1, 1), "odd size": artwork(37, 21), "photo": artwork(641, 479),
		"stripes": stripes(64, 64), "offset origin": offset,
	}
	for name, m := range images {
		for _, sub := range []Subsampling{Subsampling444, Subsampling420} {
			for _, quality := range []int{1, 30, 90, 100} {
				o := Options{Quality: quality, Subsampling: sub}
				if !bytes.Equal(mustEncode(t, m, &o), encodeScanningTwice(t, m, o)) {
					t.Errorf("%s %v q%d: keeping the coefficients changed the output bytes", name, sub, quality)
				}
			}
		}
	}
}

func TestWorkingBytesIsTheCoefficientBuffer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		width, height int
		sub           Subsampling
		want          int64
	}{
		{width: 3840, height: 2160, sub: Subsampling444, want: 480 * 270 * 3 * 64 * 2},
		{width: 3840, height: 2160, sub: Subsampling420, want: 240 * 135 * 6 * 64 * 2},
		{width: 1, height: 1, sub: Subsampling444, want: 3 * 64 * 2},
		{width: 17, height: 9, sub: Subsampling420, want: 2 * 1 * 6 * 64 * 2},
	}
	for _, tt := range tests {
		if got := WorkingBytes(tt.width, tt.height, tt.sub); got != tt.want {
			t.Errorf("WorkingBytes(%d, %d, %v) = %d, want %d", tt.width, tt.height, tt.sub, got, tt.want)
		}
		e, err := newEncoder(&bytes.Buffer{}, artwork(tt.width, tt.height), Options{Quality: 80, Subsampling: tt.sub})
		if err != nil {
			t.Fatal(err)
		}
		if kept := int64(len(e.quantizeImage(artwork(tt.width, tt.height)))) * 2; kept != tt.want {
			t.Errorf("%dx%d %v keeps %d bytes of coefficients, want %d", tt.width, tt.height, tt.sub, kept, tt.want)
		}
	}
}
