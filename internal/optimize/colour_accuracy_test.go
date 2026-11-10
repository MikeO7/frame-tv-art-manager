package optimize

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"
)

func TestICCTransformKeepsSRGBProfileAnIdentity(t *testing.T) {
	t.Parallel()
	// Colorants as shipped sRGB profiles store them: they sum to the ICC
	// connection white (0.9642, 1, 0.8249), which differs in the fifth decimal
	// from the white the Bradford adaptation was derived for.
	curve := iccCurve{kind: iccCurveParametric, params: []float64{3, 2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045}}
	transform := newICCTransform(matrixProfile{
		columns: [3][3]float64{{0.4361, 0.2225, 0.0139}, {0.3851, 0.7169, 0.0971}, {0.1430, 0.0606, 0.7139}},
		curves:  [3]iccCurve{curve, curve, curve},
	})
	var output [4]uint8
	for _, pixel := range [][3]uint8{{255, 255, 255}, {128, 128, 128}, {1, 1, 1}, {255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {200, 120, 40}, {17, 230, 99}} {
		transform.convert(output[:], uint32(pixel[0])*257, uint32(pixel[1])*257, uint32(pixel[2])*257, 0xffff)
		if got := [3]uint8{output[0], output[1], output[2]}; got != pixel {
			t.Errorf("sRGB %v converted to %v, want it unchanged", pixel, got)
		}
	}
	for level := range 256 {
		sample := uint32(level) * 257
		transform.convert(output[:], sample, sample, sample, 0xffff)
		if output[0] != uint8(level) || output[1] != uint8(level) || output[2] != uint8(level) {
			t.Fatalf("grey %d converted to %v", level, output[:3])
		}
	}
}

func TestHLGSystemGammaFollowsLuminance(t *testing.T) {
	t.Parallel()
	// BT.2100-2 Table 5 reference for signal (0.75, 0.5, 0.25).
	got := hlgEOTF([3]float64{0.75, 0.5, 0.25})
	want := [3]float64{0.1755, 0.0552, 0.0138}
	for channel := range got {
		if math.Abs(got[channel]-want[channel]) > 5e-4 {
			t.Fatalf("HLG display light = %.4f, want %.4f", got, want)
		}
	}
	grey := hlgEOTF([3]float64{0.5, 0.5, 0.5})
	if grey[0] != grey[1] || grey[1] != grey[2] || math.Abs(grey[0]-math.Pow(1.0/12, 1.2)) > 1e-12 {
		t.Fatalf("HLG grey = %v", grey)
	}
	if hlgEOTF([3]float64{}) != [3]float64{} {
		t.Fatal("HLG black is not black")
	}
}

func TestToneMapKeepsGreysNeutralAndSaturatedColoursInRange(t *testing.T) {
	t.Parallel()
	for _, level := range []float64{0.001, 0.05, 0.2, 0.6, 1} {
		mapped := bt2446MethodA([3]float64{level, level, level}, 1000, 100)
		if math.Abs(mapped[0]-mapped[1]) > 1e-12 || math.Abs(mapped[1]-mapped[2]) > 1e-12 {
			t.Fatalf("grey %.3f tone-mapped to %v", level, mapped)
		}
	}
	// BT.2446-1 Method A keeps this BT.2020 yellow-green's blue above zero;
	// scaling R'G'B' by the luma ratio alone clips it.
	mapped := bt2020ToBT709(bt2446MethodA([3]float64{0.2, 0.2, 0.02}, 1000, 100))
	if got := rgbaFromFloat(mapped, 255); got.B < 20 || got.B > 45 {
		t.Fatalf("saturated colour tone-mapped to %v, want blue near 31", got)
	}
}

func TestToneMapPremultipliesTranslucentPixels(t *testing.T) {
	t.Parallel()
	source := image.NewNRGBA64(image.Rect(0, 0, 2, 1))
	source.SetNRGBA64(0, 0, color.NRGBA64{R: 0xc000, G: 0x6000, B: 0x1000, A: 0xffff})
	source.SetNRGBA64(1, 0, color.NRGBA64{R: 0xc000, G: 0x6000, B: 0x1000, A: 0x4040})
	output := toneMapHDRToSDR(source, hdrMetadata{primaries: 9, transfer: 16}, 1000, 100)
	opaque, translucent := output.RGBAAt(0, 0), output.RGBAAt(1, 0)
	if translucent.A != 0x40 || translucent.R > translucent.A || translucent.G > translucent.A || translucent.B > translucent.A {
		t.Fatalf("translucent pixel %v is not premultiplied", translucent)
	}
	straight, _ := color.NRGBAModel.Convert(translucent).(color.NRGBA)
	for _, pair := range [][2]uint8{{straight.R, opaque.R}, {straight.G, opaque.G}, {straight.B, opaque.B}} {
		if delta := int(pair[0]) - int(pair[1]); delta < -4 || delta > 4 {
			t.Fatalf("translucent colour %v differs from opaque %v", straight, opaque)
		}
	}
}

func TestPNGColorDataToneMapsEveryPrimariesItCanConvert(t *testing.T) {
	t.Parallel()
	for _, primaries := range []byte{1, 9, 12} {
		data := appendPNGColorChunk(pngSignature[:], "cICP", []byte{primaries, 16, 0, 1})
		data = appendPNGColorChunk(data, pngChunkEnd, nil)
		metadata, err := readPNGColorData(bytes.NewReader(data))
		if err != nil || metadata.hdr == nil || metadata.hdr.primaries != primaries {
			t.Fatalf("primaries %d: metadata = %+v, %v", primaries, metadata, err)
		}
	}
	// Display P3 red is outside BT.709: red rises and green falls below zero.
	p3 := hdrPrimariesToBT709(12, [3]float64{0.5, 0.1, 0.1})
	if p3[0] <= 0.5 || p3[1] >= 0.1 {
		t.Fatalf("P3 to BT.709 = %v", p3)
	}
	white := hdrPrimariesToBT709(12, [3]float64{1, 1, 1})
	for _, channel := range white {
		if math.Abs(channel-1) > 2e-3 {
			t.Fatalf("P3 white = %v", white)
		}
	}
	if got := hdrPrimariesToBT709(1, [3]float64{0.3, 0.2, 0.1}); got != [3]float64{0.3, 0.2, 0.1} {
		t.Fatalf("BT.709 primaries changed to %v", got)
	}
}

func TestToRGBARoundsSixteenBitSamples(t *testing.T) {
	t.Parallel()
	const width = 1 << 16
	deep := image.NewNRGBA64(image.Rect(3, 5, 3+width, 6))
	grey := image.NewGray16(image.Rect(0, 0, width, 1))
	for value := range width {
		sample := uint16(value)
		deep.SetNRGBA64(3+value, 5, color.NRGBA64{R: sample, G: sample, B: sample, A: 0xffff})
		grey.SetGray16(value, 0, color.Gray16{Y: sample})
	}
	for name, img := range map[string]image.Image{"NRGBA64": deep, "Gray16": grey} {
		narrowed := toRGBA(img)
		for value := range width {
			want := uint8(math.Round(float64(value) / 257))
			if got := narrowed.RGBAAt(value, 0); got != (color.RGBA{R: want, G: want, B: want, A: 255}) {
				t.Fatalf("%s sample %d narrowed to %v, want %d", name, value, got, want)
			}
		}
	}
	translucent := image.NewNRGBA64(image.Rect(0, 0, 1, 1))
	translucent.SetNRGBA64(0, 0, color.NRGBA64{R: 0xffff, G: 0x8000, B: 0, A: 0x8000})
	if got := toRGBA(translucent).RGBAAt(0, 0); got != (color.RGBA{R: 128, G: 64, B: 0, A: 128}) {
		t.Fatalf("translucent 16-bit pixel = %v, want premultiplied {128 64 0 128}", got)
	}
}
