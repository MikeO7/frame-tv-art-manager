package jpegenc

import (
	"bytes"
	"image"
	"testing"
)

func TestOptimalHuffmanSpec(t *testing.T) {
	t.Parallel()
	fibonacci := func() *[symbolCount]uint64 {
		// Fibonacci weights build the deepest possible tree, forcing the
		// 16-bit length limit to rebalance it.
		var f [symbolCount]uint64
		a, b := uint64(1), uint64(1)
		for i := range 40 {
			f[i] = a
			a, b = b, a+b
		}
		return &f
	}
	uniform := func() *[symbolCount]uint64 {
		var f [symbolCount]uint64
		for i := range f {
			f[i] = 7
		}
		return &f
	}
	tests := []struct {
		name        string
		freq        *[symbolCount]uint64
		wantSymbols int
		wantLengths map[byte]int
	}{
		{name: "unused table", freq: &[symbolCount]uint64{}, wantSymbols: 1, wantLengths: map[byte]int{0: 1}},
		{name: "single symbol", freq: &[symbolCount]uint64{5: 100}, wantSymbols: 1, wantLengths: map[byte]int{5: 1}},
		{
			name: "skewed", freq: &[symbolCount]uint64{0: 1000, 1: 10, 2: 10, 0xf0: 1},
			wantSymbols: 4, wantLengths: map[byte]int{0: 1, 1: 2, 2: 3, 0xf0: 4},
		},
		{name: "fibonacci depth is limited", freq: fibonacci(), wantSymbols: 40},
		{name: "all 256 symbols", freq: uniform(), wantSymbols: 256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := optimalHuffmanSpec(tt.freq)
			if len(spec.value) != tt.wantSymbols {
				t.Fatalf("spec has %d symbols, want %d", len(spec.value), tt.wantSymbols)
			}
			total, kraft := 0, 0
			for i, n := range spec.count {
				total += int(n)
				kraft += int(n) << (15 - i)
			}
			if total != len(spec.value) {
				t.Fatalf("counts sum to %d for %d symbols", total, len(spec.value))
			}
			if kraft >= 1<<16 {
				t.Fatalf("code is over-full or uses the all-ones code (Kraft %d/65536)", kraft)
			}
			lengths := codeLengths(&spec)
			for symbol, n := range tt.freq {
				if n > 0 && lengths[byte(symbol)] == 0 {
					t.Fatalf("used symbol %#x has no code", symbol)
				}
			}
			for symbol, want := range tt.wantLengths {
				if lengths[symbol] != want {
					t.Fatalf("symbol %#x has length %d, want %d", symbol, lengths[symbol], want)
				}
			}
			var lut huffmanLUT
			lut.init(&spec)
			for _, symbol := range spec.value {
				code, n := lut[symbol]&(1<<24-1), lut[symbol]>>24
				if code == 1<<n-1 {
					t.Fatalf("symbol %#x was assigned the all-ones code", symbol)
				}
			}
		})
	}
}

func codeLengths(spec *huffmanSpec) map[byte]int {
	lengths := map[byte]int{}
	k := 0
	for i, n := range spec.count {
		for range n {
			lengths[spec.value[k]] = i + 1
			k++
		}
	}
	return lengths
}

func BenchmarkEncode4K(b *testing.B) {
	src := artwork(3840, 2160)
	var buf bytes.Buffer
	for _, sub := range []Subsampling{Subsampling444, Subsampling420} {
		b.Run(sub.String(), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				buf.Reset()
				if err := Encode(&buf, src, &Options{Quality: 95, Subsampling: sub}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(buf.Len()), "bytes")
		})
	}
	b.Run("stdlib-420", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			buf.Reset()
			benchStdlib(b, &buf, src)
		}
		b.ReportMetric(float64(buf.Len()), "bytes")
	})
}

func benchStdlib(b *testing.B, buf *bytes.Buffer, src image.Image) {
	b.Helper()
	buf.Write(stdlibEncode(b, src, 95))
}
