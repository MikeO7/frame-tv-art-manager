package optimize

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
)

// referenceICCToSRGB is the previous per-pixel conversion, retained to bound
// the table-driven replacement's drift.
func referenceICCToSRGB(t *testing.T, src image.Image, profileData []byte) *image.RGBA {
	t.Helper()
	profile, err := parseMatrixProfile(profileData)
	if err != nil {
		t.Fatal(err)
	}
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			r16, g16, b16, a16 := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if a16 == 0 {
				continue
			}
			device := [3]float64{
				profile.curves[0].evaluate(float64(r16) / float64(a16)),
				profile.curves[1].evaluate(float64(g16) / float64(a16)),
				profile.curves[2].evaluate(float64(b16) / float64(a16)),
			}
			xyz50 := [3]float64{}
			for channel := range 3 {
				for row := range 3 {
					xyz50[row] += profile.columns[channel][row] * device[channel]
				}
			}
			xyz65 := d50ToD65(xyz50)
			linear := [3]float64{
				3.2404542*xyz65[0] - 1.5371385*xyz65[1] - 0.4985314*xyz65[2],
				-0.969266*xyz65[0] + 1.8760108*xyz65[1] + 0.041556*xyz65[2],
				0.0556434*xyz65[0] - 0.2040259*xyz65[1] + 1.0572252*xyz65[2],
			}
			encoded := color.RGBA{
				R: uint8(math.Round(clampUnit(srgbEncode(linear[0])) * 255)),
				G: uint8(math.Round(clampUnit(srgbEncode(linear[1])) * 255)),
				B: uint8(math.Round(clampUnit(srgbEncode(linear[2])) * 255)),
				A: clampByte(float64(a16) / 257),
			}
			dst.SetRGBA(x, y, encoded)
		}
	}
	return dst
}

// iccTestProfile builds a v4 matrix/TRC profile with Display P3 colorants
// (D50-adapted) and the supplied tone curve tag for every channel.
func iccTestProfile(curve []byte) []byte {
	xyz := func(values [3]float64) []byte {
		data := make([]byte, 20)
		copy(data, "XYZ ")
		for index, value := range values {
			binary.BigEndian.PutUint32(data[8+index*4:], uint32(int32(math.Round(value*65536))))
		}
		return data
	}
	tags := []struct {
		signature string
		data      []byte
	}{
		{"rXYZ", xyz([3]float64{0.5151, 0.2412, -0.0011})},
		{"gXYZ", xyz([3]float64{0.2920, 0.6922, 0.0419})},
		{"bXYZ", xyz([3]float64{0.1571, 0.0666, 0.7841})},
		{"rTRC", curve},
		{"gTRC", curve},
		{"bTRC", curve},
	}
	offset := 132 + 12*len(tags)
	profile := make([]byte, offset)
	profile[8] = 4
	copy(profile[16:20], "RGB ")
	copy(profile[20:24], "XYZ ")
	copy(profile[36:40], "acsp")
	binary.BigEndian.PutUint32(profile[68:72], uint32(math.Round(0.9642*65536)))
	binary.BigEndian.PutUint32(profile[72:76], 1<<16)
	binary.BigEndian.PutUint32(profile[76:80], uint32(math.Round(0.8249*65536)))
	binary.BigEndian.PutUint32(profile[128:132], uint32(len(tags)))
	for index, tag := range tags {
		entry := 132 + index*12
		copy(profile[entry:], tag.signature)
		binary.BigEndian.PutUint32(profile[entry+4:], uint32(offset))
		binary.BigEndian.PutUint32(profile[entry+8:], uint32(len(tag.data)))
		profile = append(profile, tag.data...)
		offset += len(tag.data)
	}
	binary.BigEndian.PutUint32(profile[:4], uint32(len(profile)))
	return profile
}

func iccGammaCurve(gamma float64) []byte {
	curve := make([]byte, 14)
	copy(curve, "curv")
	binary.BigEndian.PutUint32(curve[8:], 1)
	binary.BigEndian.PutUint16(curve[12:], uint16(math.Round(gamma*256)))
	return curve
}

func iccSRGBParametricCurve() []byte {
	curve := make([]byte, 12+5*4)
	copy(curve, "para")
	binary.BigEndian.PutUint16(curve[8:], 3)
	for index, value := range []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045} {
		binary.BigEndian.PutUint32(curve[12+index*4:], uint32(int32(math.Round(value*65536))))
	}
	return curve
}

func iccTableCurve() []byte {
	const entries = 1024
	curve := make([]byte, 12+entries*2)
	copy(curve, "curv")
	binary.BigEndian.PutUint32(curve[8:], entries)
	for index := range entries {
		value := math.Pow(float64(index)/(entries-1), 1.8)
		binary.BigEndian.PutUint16(curve[12+index*2:], uint16(math.Round(value*65535)))
	}
	return curve
}

func iccSources(t *testing.T) map[string]image.Image {
	t.Helper()
	opaque := resampleFixture(97, 61, false)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, opaque, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	ycbcr, err := jpeg.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	deep := image.NewNRGBA64(opaque.Bounds())
	gray := image.NewGray16(opaque.Bounds())
	paletted := image.NewPaletted(opaque.Bounds(), color.Palette{color.Black, color.White, color.RGBA{R: 200, G: 30, B: 90, A: 255}})
	for y := 0; y < 61; y++ {
		for x := 0; x < 97; x++ {
			deep.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x * 677), G: uint16(y * 1031), B: uint16((x * y * 37) & 0xffff), A: 0xffff})
			gray.SetGray16(x, y, color.Gray16{Y: uint16(x*y*11) & 0xffff})
			paletted.SetColorIndex(x, y, uint8((x+y)%3))
		}
	}
	rgba64 := image.NewRGBA64(opaque.Bounds())
	gray8 := image.NewGray(opaque.Bounds())
	for y := 0; y < 61; y++ {
		for x := 0; x < 97; x++ {
			rgba64.SetRGBA64(x, y, color.RGBA64{R: uint16(x * 401), G: uint16(y * 977), B: 0x8000, A: 0xffff})
			gray8.SetGray(x, y, color.Gray{Y: uint8(x + y)})
		}
	}
	nrgba := image.NewNRGBA(opaque.Bounds())
	for index := range nrgba.Pix {
		nrgba.Pix[index] = opaque.Pix[index]
	}
	return map[string]image.Image{
		"ycbcr": ycbcr, "rgba": opaque, "nrgba": nrgba, "nrgba64": deep, "gray16": gray, "paletted": paletted,
		"rgba64": rgba64, "gray": gray8,
	}
}

func TestConvertICCToSRGBMatchesPreviousImplementation(t *testing.T) {
	t.Parallel()
	curves := map[string][]byte{"gamma": iccGammaCurve(2.2), "parametric": iccSRGBParametricCurve(), "table": iccTableCurve()}
	sources := iccSources(t)
	for curveName, curve := range curves {
		profile := iccTestProfile(curve)
		for sourceName, source := range sources {
			t.Run(curveName+"/"+sourceName, func(t *testing.T) {
				t.Parallel()
				want := referenceICCToSRGB(t, source, profile)
				got, err := convertICCToSRGB(source, profile)
				if err != nil {
					t.Fatal(err)
				}
				for index := range want.Pix {
					if delta := int(got.Pix[index]) - int(want.Pix[index]); delta < -1 || delta > 1 {
						t.Fatalf("sample %d = %d, previous implementation %d", index, got.Pix[index], want.Pix[index])
					}
				}
			})
		}
	}
}

func TestConvertICCToSRGBPremultipliesTranslucentPixels(t *testing.T) {
	t.Parallel()
	source := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	source.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 128})
	source.SetNRGBA(1, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 64})
	source.SetNRGBA(2, 0, color.NRGBA{R: 9, G: 9, B: 9, A: 0})
	converted, err := convertICCToSRGB(source, testMatrixICCProfile(false))
	if err != nil {
		t.Fatal(err)
	}
	for x := range 3 {
		pixel := converted.RGBAAt(x, 0)
		if pixel.R > pixel.A || pixel.G > pixel.A || pixel.B > pixel.A {
			t.Fatalf("pixel %d = %v is not premultiplied", x, pixel)
		}
	}
	if white := converted.RGBAAt(0, 0); white.A != 128 || white.R < 126 || white.R > 128 {
		t.Fatalf("half-transparent white = %v, want about {128 128 128 128}", white)
	}
	if transparent := converted.RGBAAt(2, 0); transparent != (color.RGBA{}) {
		t.Fatalf("transparent pixel = %v", transparent)
	}
	if _, err := convertICCToSRGB(source, []byte("short")); err == nil {
		t.Fatal("convertICCToSRGB(invalid profile) error = nil")
	}
}
