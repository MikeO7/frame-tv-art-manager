package jpegenc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func TestEncodeDecodesWithStdlibAndMatchesItsPSNR(t *testing.T) {
	t.Parallel()
	type tc struct {
		w, h    int
		quality int
		sub     Subsampling
	}
	cases := make([]tc, 0, 42)
	for _, size := range [][2]int{{1, 1}, {7, 9}, {17, 33}, {64, 48}} {
		for _, quality := range []int{1, 50, 75, 95, 100} {
			for _, sub := range []Subsampling{Subsampling444, Subsampling420} {
				cases = append(cases, tc{size[0], size[1], quality, sub})
			}
		}
	}
	cases = append(cases, tc{3840, 2160, 95, Subsampling444}, tc{3840, 2160, 95, Subsampling420})
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dx%d/q%d/%v", c.w, c.h, c.quality, c.sub), func(t *testing.T) {
			t.Parallel()
			src := artwork(c.w, c.h)
			got := mustDecode(t, mustEncode(t, src, &Options{Quality: c.quality, Subsampling: c.sub}))
			if got.Bounds() != src.Bounds() {
				t.Fatalf("decoded bounds %v, want %v", got.Bounds(), src.Bounds())
			}
			ycc, ok := got.(*image.YCbCr)
			if !ok {
				t.Fatalf("decoded %T, want *image.YCbCr", got)
			}
			wantRatio := image.YCbCrSubsampleRatio444
			if c.sub == Subsampling420 {
				wantRatio = image.YCbCrSubsampleRatio420
			}
			if ycc.SubsampleRatio != wantRatio {
				t.Fatalf("decoded subsample ratio %v, want %v", ycc.SubsampleRatio, wantRatio)
			}
			ours := psnr(src, got)
			stdlib := psnr(src, mustDecode(t, stdlibEncode(t, src, c.quality)))
			if ours < stdlib {
				t.Fatalf("PSNR %.3f dB is below image/jpeg's %.3f dB", ours, stdlib)
			}
			t.Logf("PSNR %.3f dB, image/jpeg %.3f dB", ours, stdlib)
		})
	}
}

func TestEncodeImageKinds(t *testing.T) {
	t.Parallel()
	rgba := artwork(37, 21)
	nrgba := image.NewNRGBA(rgba.Bounds())
	draw.Draw(nrgba, nrgba.Bounds(), rgba, image.Point{}, draw.Src)
	ycbcr := image.NewYCbCr(rgba.Bounds(), image.YCbCrSubsampleRatio422)
	for y := range rgba.Bounds().Dy() {
		for x := range rgba.Bounds().Dx() {
			yy, cb, cr := toYCbCrColor(rgba.At(x, y))
			ycbcr.Y[ycbcr.YOffset(x, y)] = yy
			ycbcr.Cb[ycbcr.COffset(x, y)] = cb
			ycbcr.Cr[ycbcr.COffset(x, y)] = cr
		}
	}
	gray := image.NewGray(rgba.Bounds())
	draw.Draw(gray, gray.Bounds(), rgba, image.Point{}, draw.Src)
	offset := artwork(64, 64).SubImage(image.Rect(5, 7, 40, 30))
	tests := []struct {
		name   string
		img    image.Image
		wantYC bool
	}{
		{name: "rgba", img: rgba, wantYC: true},
		{name: "nrgba generic path", img: nrgba, wantYC: true},
		{name: "ycbcr fast path", img: ycbcr, wantYC: true},
		{name: "gray single component", img: gray},
		{name: "rgba sub-image with offset origin", img: offset, wantYC: true},
	}
	for _, tt := range tests {
		for _, sub := range []Subsampling{Subsampling444, Subsampling420} {
			t.Run(tt.name+"/"+sub.String(), func(t *testing.T) {
				t.Parallel()
				data := mustEncode(t, tt.img, &Options{Quality: 90, Subsampling: sub})
				got := mustDecode(t, data)
				if got.Bounds().Size() != tt.img.Bounds().Size() {
					t.Fatalf("decoded size %v, want %v", got.Bounds().Size(), tt.img.Bounds().Size())
				}
				if _, isYCbCr := got.(*image.YCbCr); isYCbCr != tt.wantYC {
					t.Fatalf("decoded %T, want YCbCr=%t", got, tt.wantYC)
				}
				stdlib := mustDecode(t, stdlibEncode(t, tt.img, 90))
				src := image.NewRGBA(image.Rect(0, 0, tt.img.Bounds().Dx(), tt.img.Bounds().Dy()))
				draw.Draw(src, src.Bounds(), tt.img, tt.img.Bounds().Min, draw.Src)
				if ours, theirs := psnr(src, got), psnr(src, stdlib); ours < theirs {
					t.Fatalf("PSNR %.3f dB is below image/jpeg's %.3f dB", ours, theirs)
				}
			})
		}
	}
}

func TestSubsampling420MatchesStdlibPixelsWithSmallerFile(t *testing.T) {
	t.Parallel()
	for _, quality := range []int{10, 50, 75, 95, 100} {
		t.Run(fmt.Sprintf("q%d", quality), func(t *testing.T) {
			t.Parallel()
			src := artwork(200, 120)
			ours := mustEncode(t, src, &Options{Quality: quality, Subsampling: Subsampling420})
			theirs := stdlibEncode(t, src, quality)
			if !samePixels(mustDecode(t, ours), mustDecode(t, theirs)) {
				t.Fatal("4:2:0 output decodes differently from image/jpeg at the same quality")
			}
			if len(ours) >= len(theirs) {
				t.Fatalf("optimized tables produced %d bytes, image/jpeg %d", len(ours), len(theirs))
			}
		})
	}
}

func TestOptimizedHuffmanTablesShrinkOutputLosslessly(t *testing.T) {
	t.Parallel()
	gray := image.NewGray(image.Rect(0, 0, 96, 80))
	draw.Draw(gray, gray.Bounds(), artwork(96, 80), image.Point{}, draw.Src)
	tests := []struct {
		name string
		img  image.Image
		opts Options
	}{
		{name: "444 q95", img: artwork(160, 96), opts: Options{Quality: 95}},
		{name: "444 q75", img: artwork(160, 96), opts: Options{Quality: 75}},
		{name: "444 q30", img: artwork(160, 96), opts: Options{Quality: 30}},
		{name: "420 q95", img: artwork(160, 96), opts: Options{Quality: 95, Subsampling: Subsampling420}},
		{name: "stripes 444 q95", img: stripes(64, 64), opts: Options{Quality: 95}},
		{name: "gray q90", img: gray, opts: Options{Quality: 90}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			optimized := mustEncode(t, tt.img, &tt.opts)
			standard := encodeWithStandardTables(t, tt.img, &tt.opts)
			if len(optimized) >= len(standard) {
				t.Fatalf("optimized tables produced %d bytes, standard tables %d", len(optimized), len(standard))
			}
			if !samePixels(mustDecode(t, optimized), mustDecode(t, standard)) {
				t.Fatal("Huffman table choice changed decoded pixels")
			}
		})
	}
}

func TestSubsampling444KeepsRedBlueStripeChroma(t *testing.T) {
	t.Parallel()
	src := stripes(128, 64)
	full := chromaPSNR(src, mustDecode(t, mustEncode(t, src, &Options{Quality: 95})))
	half := chromaPSNR(src, mustDecode(t, mustEncode(t, src, &Options{Quality: 95, Subsampling: Subsampling420})))
	t.Logf("chroma PSNR 4:4:4 %.2f dB, 4:2:0 %.2f dB", full, half)
	if full < half+15 {
		t.Fatalf("4:4:4 chroma PSNR %.2f dB does not clearly beat 4:2:0's %.2f dB", full, half)
	}
}

func TestEncodeDefaults(t *testing.T) {
	t.Parallel()
	src := artwork(40, 24)
	if !bytes.Equal(mustEncode(t, src, nil), mustEncode(t, src, &Options{Quality: defaultQuality})) {
		t.Fatal("nil options differ from default quality with 4:4:4")
	}
	if !bytes.Equal(mustEncode(t, src, &Options{Quality: -3}), mustEncode(t, src, &Options{Quality: 1})) {
		t.Fatal("quality below 1 is not clamped to 1")
	}
	if !bytes.Equal(mustEncode(t, src, &Options{Quality: 400}), mustEncode(t, src, &Options{Quality: 100})) {
		t.Fatal("quality above 100 is not clamped to 100")
	}
}

type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after < len(p) {
		n := max(w.after, 0)
		w.after = 0
		return n, errWriteFailed
	}
	w.after -= len(p)
	return len(p), nil
}

var errWriteFailed = errors.New("write failed")

func TestEncodeErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		w    func() *failingWriter
		img  image.Image
		opts *Options
		want string
	}{
		{name: "too wide", img: image.NewGray(image.Rect(0, 0, 1<<16, 1)), want: "too large"},
		{name: "empty", img: image.NewRGBA(image.Rect(0, 0, 0, 5)), want: "empty"},
		{name: "unknown subsampling", img: artwork(8, 8), opts: &Options{Quality: 90, Subsampling: 7}, want: "unsupported chroma subsampling"},
		{name: "writer fails in header", w: func() *failingWriter { return &failingWriter{after: 10} }, img: artwork(64, 64)},
		{name: "writer fails in entropy data", w: func() *failingWriter { return &failingWriter{after: 70000} }, img: artwork(512, 512)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &failingWriter{after: 1 << 30}
			if tt.w != nil {
				w = tt.w()
			}
			err := Encode(w, tt.img, tt.opts)
			if err == nil {
				t.Fatal("Encode succeeded, want error")
			}
			if tt.want == "" {
				if !errors.Is(err, errWriteFailed) {
					t.Fatalf("Encode error %v, want the writer's error", err)
				}
				return
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tt.want)) {
				t.Fatalf("Encode error %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParseSubsampling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    Subsampling
		wantErr bool
	}{
		{in: "444", want: Subsampling444},
		{in: "420", want: Subsampling420},
		{in: "422", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseSubsampling(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Fatalf("ParseSubsampling(%q) = %v, %v; want %v, error=%t", tt.in, got, err, tt.want, tt.wantErr)
		}
		if !tt.wantErr && got.String() != tt.in {
			t.Fatalf("String() = %q, want %q", got.String(), tt.in)
		}
	}
	if got := Subsampling(9).String(); got != "Subsampling(9)" {
		t.Fatalf("unknown String() = %q", got)
	}
}

func TestEncodeUniformImageUsesMinimalTables(t *testing.T) {
	t.Parallel()
	src := image.NewRGBA(image.Rect(0, 0, 48, 32))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.RGBA{R: 200, G: 120, B: 60, A: 255}}, image.Point{}, draw.Src)
	data := mustEncode(t, src, &Options{Quality: 95})
	got := mustDecode(t, data)
	if p := psnr(src, got); p < 40 {
		t.Fatalf("uniform image PSNR %.2f dB", p)
	}
	segments := parseSegments(t, data)
	for _, table := range segments.huffman {
		if len(table.values) > 2 {
			t.Fatalf("table %#x has %d symbols for a flat image, want at most 2", table.classID, len(table.values))
		}
	}
}
