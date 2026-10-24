package optimize

import (
	"image"
)

// iccLookupSize covers every 16-bit sample value a decoder can report.
const iccLookupSize = 1 << 16

// linearSRGBFromXYZD65 is the IEC 61966-2-1 XYZ (D65) to linear sRGB matrix.
//
//nolint:gochecknoglobals // immutable standard colorimetric constant
var linearSRGBFromXYZD65 = [3][3]float64{
	{3.2404542, -1.5371385, -0.4985314},
	{-0.969266, 1.8760108, 0.041556},
	{0.0556434, -0.2040259, 1.0572252},
}

// iccTransform maps device RGB through a matrix/TRC profile to sRGB. Opaque
// samples use per-channel tone-curve tables and a single folded matrix.
type iccTransform struct {
	curves [3]iccCurve
	lookup [3][]float64
	matrix [3][3]float64
	tables *srgbTables
}

func newICCTransform(profile matrixProfile) iccTransform {
	transform := iccTransform{curves: profile.curves, tables: srgbLookup()}
	var deviceToXYZD50 [3][3]float64
	for channel := range 3 {
		for row := range 3 {
			deviceToXYZD50[row][channel] = profile.columns[channel][row]
		}
	}
	adaptation := [3][3]float64{
		d50ToD65([3]float64{1, 0, 0}),
		d50ToD65([3]float64{0, 1, 0}),
		d50ToD65([3]float64{0, 0, 1}),
	}
	transform.matrix = multiply3x3(linearSRGBFromXYZD65, multiply3x3(transpose3x3(adaptation), deviceToXYZD50))
	for channel, curve := range profile.curves {
		table := make([]float64, iccLookupSize)
		for value := range table {
			table[value] = curve.evaluate(float64(value) / (iccLookupSize - 1))
		}
		transform.lookup[channel] = table
	}
	return transform
}

func multiply3x3(left, right [3][3]float64) [3][3]float64 {
	var product [3][3]float64
	for row := range 3 {
		for column := range 3 {
			for inner := range 3 {
				product[row][column] += left[row][inner] * right[inner][column]
			}
		}
	}
	return product
}

func transpose3x3(matrix [3][3]float64) [3][3]float64 {
	var transposed [3][3]float64
	for row := range 3 {
		for column := range 3 {
			transposed[column][row] = matrix[row][column]
		}
	}
	return transposed
}

// convertICCToSRGB converts src from its embedded matrix/TRC profile to
// premultiplied sRGB. Rows are converted in parallel partitions.
func convertICCToSRGB(src image.Image, profileData []byte) (*image.RGBA, error) {
	profile, err := parseMatrixProfile(profileData)
	if err != nil {
		return nil, err
	}
	transform := newICCTransform(profile)
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	read := premultipliedReader(src)
	runPixelRows(0, bounds.Dy(), func(_, startY, endY int) {
		for y := startY; y < endY; y++ {
			row := dst.Pix[y*dst.Stride:]
			for x := range bounds.Dx() {
				red, green, blue, alpha := read(bounds.Min.X+x, bounds.Min.Y+y)
				transform.convert(row[x*4:x*4+4:x*4+4], red, green, blue, alpha)
			}
		}
	})
	return dst, nil
}

// convert writes one premultiplied sRGB pixel from 16-bit premultiplied
// device samples.
func (transform *iccTransform) convert(output []uint8, red, green, blue, alpha uint32) {
	if alpha == 0 {
		return
	}
	var device [3]float64
	if alpha == 0xffff {
		device = [3]float64{transform.lookup[0][red], transform.lookup[1][green], transform.lookup[2][blue]}
	} else {
		coverage := float64(alpha)
		device = [3]float64{
			transform.curves[0].evaluate(float64(red) / coverage),
			transform.curves[1].evaluate(float64(green) / coverage),
			transform.curves[2].evaluate(float64(blue) / coverage),
		}
	}
	alpha8 := clampByte(float64(alpha) / 257)
	for row := range 3 {
		linear := transform.matrix[row][0]*device[0] + transform.matrix[row][1]*device[1] + transform.matrix[row][2]*device[2]
		if alpha8 == 255 {
			output[row] = transform.tables.encode(linear)
		} else {
			output[row] = clampByte(clampUnit(srgbEncode(clampUnit(linear))) * float64(alpha8))
		}
	}
	output[3] = alpha8
}

// premultipliedReader returns src's 16-bit premultiplied samples through the
// concrete decoder types without boxing each pixel in a color.Color.
func premultipliedReader(src image.Image) func(x, y int) (uint32, uint32, uint32, uint32) {
	switch typed := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.YCbCrAt(x, y).RGBA() }
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.RGBAAt(x, y).RGBA() }
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.NRGBAAt(x, y).RGBA() }
	case *image.RGBA64:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.RGBA64At(x, y).RGBA() }
	case *image.NRGBA64:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.NRGBA64At(x, y).RGBA() }
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.GrayAt(x, y).RGBA() }
	case *image.Gray16:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return typed.Gray16At(x, y).RGBA() }
	default:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return src.At(x, y).RGBA() }
	}
}
