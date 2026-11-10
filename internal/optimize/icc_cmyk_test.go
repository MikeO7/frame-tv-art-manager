package optimize

import (
	"encoding/binary"
	"image"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
)

// testInkLab is the measured color the synthetic press profiles assign to an
// ink combination, each ink in 0..1. It is linear in every ink, so a lookup
// table sampled from it reproduces it exactly between grid points. floor is
// the L* of the press's black.
func testInkLab(cyan, magenta, yellow, black, floor float64) labColor {
	light := (1 - black) * (1 - (cyan+magenta+yellow)/5)
	return labColor{
		l: floor + (100-floor)*light,
		a: 30 * (magenta - cyan) * (1 - black),
		b: 30 * (yellow - (cyan+magenta)/2) * (1 - black),
	}
}

type cmykProfileKind int

const (
	cmykProfileLut8 cmykProfileKind = iota
	cmykProfileLut16
	cmykProfileAToB
)

func be16(value float64) []byte {
	return binary.BigEndian.AppendUint16(nil, uint16(math.Round(min(max(value, 0), 65535))))
}

func fixed1516(value float64) []byte {
	return binary.BigEndian.AppendUint32(nil, uint32(int32(math.Round(value*65536))))
}

// testCMYKLookup builds the device-to-connection tag for a press described by
// testInkLab, on a three-point grid per ink.
func testCMYKLookup(kind cmykProfileKind, floor float64) []byte {
	const grid = 3
	var table []byte
	for cyan := range grid {
		for magenta := range grid {
			for yellow := range grid {
				for black := range grid {
					lab := testInkLab(float64(cyan)/2, float64(magenta)/2, float64(yellow)/2, float64(black)/2, floor)
					switch kind {
					case cmykProfileLut8:
						table = append(table, uint8(math.Round(lab.l*2.55)), uint8(math.Round(lab.a+128)), uint8(math.Round(lab.b+128)))
					case cmykProfileLut16:
						table = append(table, be16(lab.l/100*65280)...)
						table = append(table, be16((lab.a+128)*256)...)
						table = append(table, be16((lab.b+128)*256)...)
					case cmykProfileAToB:
						// Stored with a* and b* exchanged; the tag's matrix
						// exchanges them back.
						table = append(table, be16(lab.l/100*65535)...)
						table = append(table, be16((lab.b+128)/255*65535)...)
						table = append(table, be16((lab.a+128)/255*65535)...)
					}
				}
			}
		}
	}
	switch kind {
	case cmykProfileLut8:
		tag := append([]byte("mft1\x00\x00\x00\x00"), 4, 3, grid, 0)
		tag = append(tag, make([]byte, 36)...)
		ramp := make([]byte, 256)
		for index := range ramp {
			ramp[index] = uint8(index)
		}
		for range 4 {
			tag = append(tag, ramp...)
		}
		tag = append(tag, table...)
		for range 3 {
			tag = append(tag, ramp...)
		}
		return tag
	case cmykProfileLut16:
		tag := append([]byte("mft2\x00\x00\x00\x00"), 4, 3, grid, 0)
		tag = append(tag, make([]byte, 36)...)
		tag = append(tag, 0, 2, 0, 2)
		for range 4 {
			tag = append(tag, 0, 0, 0xff, 0xff)
		}
		tag = append(tag, table...)
		for range 3 {
			tag = append(tag, 0, 0, 0xff, 0xff)
		}
		return tag
	}
	identity := []byte("curv\x00\x00\x00\x00\x00\x00\x00\x00")
	gammaOne := append([]byte("para\x00\x00\x00\x00\x00\x00\x00\x00"), fixed1516(1)...)
	final := append(append(append([]byte{}, identity...), identity...), identity...)
	var matrix []byte
	for _, value := range []float64{1, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0} {
		matrix = append(matrix, fixed1516(value)...)
	}
	middle := append(append(append([]byte{}, gammaOne...), gammaOne...), gammaOne...)
	lattice := append([]byte{grid, grid, grid, grid}, make([]byte, 12)...)
	lattice = append(lattice, 2, 0, 0, 0)
	lattice = append(lattice, table...)
	for len(lattice)%4 != 0 {
		lattice = append(lattice, 0)
	}
	input := append(append(append(append([]byte{}, identity...), identity...), identity...), identity...)
	tag := append([]byte("mAB \x00\x00\x00\x00"), 4, 3, 0, 0)
	offset := 32
	for _, section := range [][]byte{final, matrix, middle, lattice, input} {
		tag = binary.BigEndian.AppendUint32(tag, uint32(offset))
		offset += len(section)
	}
	for _, section := range [][]byte{final, matrix, middle, lattice, input} {
		tag = append(tag, section...)
	}
	return tag
}

// testCMYKProfile wraps a lookup tag in a CMYK press profile with a Lab
// connection space.
func testCMYKProfile(kind cmykProfileKind, floor float64) []byte {
	tag := testCMYKLookup(kind, floor)
	profile := make([]byte, 144, 144+len(tag))
	profile[8] = 2
	if kind == cmykProfileAToB {
		profile[8] = 4
	}
	copy(profile[12:16], "prtr")
	copy(profile[16:20], "CMYK")
	copy(profile[20:24], "Lab ")
	copy(profile[36:40], "acsp")
	copy(profile[68:80], append(append(fixed1516(0.9642), fixed1516(1)...), fixed1516(0.8249)...))
	binary.BigEndian.PutUint32(profile[128:132], 1)
	copy(profile[132:136], "A2B0")
	binary.BigEndian.PutUint32(profile[136:140], 144)
	binary.BigEndian.PutUint32(profile[140:144], uint32(len(tag)))
	profile = append(profile, tag...)
	binary.BigEndian.PutUint32(profile[:4], uint32(len(profile)))
	return profile
}

func cmykPixels(inks ...[4]uint8) *image.CMYK {
	img := image.NewCMYK(image.Rect(0, 0, len(inks), 1))
	for index, ink := range inks {
		copy(img.Pix[index*4:], ink[:])
	}
	return img
}

// A CMYK picture must be rendered through the press profile it embeds. The
// formula the standard library applies when the profile is ignored turns full
// cyan ink into pure display cyan, which no press can print.
func TestCMYKProfileLookupTablesReproduceThePress(t *testing.T) {
	t.Parallel()
	inks := [][4]uint8{
		{0, 0, 0, 0}, {255, 0, 0, 0}, {0, 255, 0, 0}, {0, 0, 255, 0}, {0, 0, 0, 255},
		{128, 0, 0, 0}, {64, 191, 0, 0}, {30, 60, 90, 120}, {200, 40, 160, 20}, {0, 0, 0, 128}, {255, 255, 255, 0},
	}
	cases := []struct {
		name      string
		kind      cmykProfileKind
		tolerance float64
	}{
		{name: "8-bit table", kind: cmykProfileLut8, tolerance: 1.5},
		{name: "16-bit table", kind: cmykProfileLut16, tolerance: 0.6},
		{name: "curves, matrix and grid", kind: cmykProfileAToB, tolerance: 0.6},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := convertICCToSRGB(cmykPixels(inks...), testCMYKProfile(test.kind, 0))
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			naive := toRGBA(cmykPixels(inks...))
			for index, ink := range inks {
				want := testInkLab(float64(ink[0])/255, float64(ink[1])/255, float64(ink[2])/255, float64(ink[3])/255, 0)
				pixel := got.RGBAAt(index, 0)
				l, a, b := rgbToLab(pixel.R, pixel.G, pixel.B)
				if difference := ciede2000(want, labColor{l: l, a: a, b: b}); difference > test.tolerance || pixel.A != 255 {
					t.Errorf("inks %v rendered %v, %.2f dE from the profile's color", ink, pixel, difference)
				}
			}
			cyan, ignored := got.RGBAAt(1, 0), naive.RGBAAt(1, 0)
			if cyan == ignored {
				t.Fatalf("full cyan rendered %v, the same as with the profile ignored", cyan)
			}
		})
	}
}

// Paper black is far lighter than a display's. The press's black must land on
// display black and paper on display white, or shadows render as dark gray.
func TestCMYKProfileMapsPressBlackToDisplayBlack(t *testing.T) {
	t.Parallel()
	got, err := convertICCToSRGB(
		cmykPixels([4]uint8{0, 0, 0, 255}, [4]uint8{0, 0, 0, 128}, [4]uint8{0, 0, 0, 0}),
		testCMYKProfile(cmykProfileLut16, 12),
	)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	black, middle, paper := got.RGBAAt(0, 0), got.RGBAAt(1, 0), got.RGBAAt(2, 0)
	if black.R != 0 || black.G != 0 || black.B != 0 {
		t.Errorf("press black rendered %v, want display black", black)
	}
	if paper.R != 255 || paper.G != 255 || paper.B != 255 {
		t.Errorf("paper rendered %v, want display white", paper)
	}
	if middle.R <= 90 || middle.R >= 140 || middle.R != middle.G || middle.G != middle.B {
		t.Errorf("half black rendered %v, want a neutral middle gray", middle)
	}
}

func TestCMYKProfileRejectsWhatItCannotConvert(t *testing.T) {
	t.Parallel()
	valid := testCMYKProfile(cmykProfileLut16, 0)
	mutate := func(change func(profile []byte) []byte) []byte {
		return change(append([]byte{}, valid...))
	}
	cases := []struct {
		name    string
		src     image.Image
		profile []byte
		want    string
	}{
		{name: "RGB picture", src: image.NewRGBA(image.Rect(0, 0, 1, 1)), profile: valid, want: "image is not"},
		{name: "no lookup tag", profile: mutate(func(p []byte) []byte { copy(p[132:136], "B2A0"); return p }), want: "no device-to-connection"},
		{name: "unknown tag type", profile: mutate(func(p []byte) []byte { copy(p[144:148], "clut"); return p }), want: "unsupported"},
		{name: "three inks", profile: mutate(func(p []byte) []byte { p[152] = 3; return p }), want: "not CMYK"},
		{name: "one grid point", profile: mutate(func(p []byte) []byte { p[154] = 1; return p }), want: "fewer than two points"},
		{name: "grid larger than the tag", profile: mutate(func(p []byte) []byte { p[154] = 200; return p }), want: "truncated"},
		{name: "unsupported connection space", profile: mutate(func(p []byte) []byte { copy(p[20:24], "Luv "); return p }), want: "unsupported"},
		{name: "truncated tag", profile: mutate(func(p []byte) []byte {
			p = p[:len(p)-8]
			binary.BigEndian.PutUint32(p[:4], uint32(len(p)))
			binary.BigEndian.PutUint32(p[140:144], uint32(len(p)-144))
			return p
		}), want: "truncated"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			src := test.src
			if src == nil {
				src = cmykPixels([4]uint8{})
			}
			if _, err := convertICCToSRGB(src, test.profile); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, test.want)
			}
		})
	}

	// A profile that cannot be used falls back to the plain conversion and
	// still yields a picture.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{ColorProfilePolicy: profileConvertSRGB}
	fallback := normalizeColor(cmykPixels([4]uint8{255, 0, 0, 0}), embeddedColorData{icc: cases[1].profile}, cfg, logger, "press.jpg")
	if got, want := fallback.RGBAAt(0, 0), toRGBA(cmykPixels([4]uint8{255, 0, 0, 0})).RGBAAt(0, 0); got != want {
		t.Fatalf("fallback rendered %v, want the plain conversion %v", got, want)
	}
	converted := normalizeColor(cmykPixels([4]uint8{255, 0, 0, 0}), embeddedColorData{icc: valid}, cfg, logger, "press.jpg")
	if got, plain := converted.RGBAAt(0, 0), fallback.RGBAAt(0, 0); got == plain {
		t.Fatalf("profiled CMYK rendered %v, the same as the plain conversion", got)
	}
}
