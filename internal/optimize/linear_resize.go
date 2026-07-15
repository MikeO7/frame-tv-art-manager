package optimize

import (
	"encoding/binary"
	"image"
	"math"

	"golang.org/x/image/draw"
)

func resizeCrop(src *image.RGBA, crop image.Rectangle, targetWidth, targetHeight int, linearLight bool) *image.RGBA {
	if !linearLight {
		output := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		draw.CatmullRom.Scale(output, output.Bounds(), src, crop, draw.Src, nil)
		return output
	}
	linearSource := image.NewRGBA64(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	for y := 0; y < crop.Dy(); y++ {
		for x := 0; x < crop.Dx(); x++ {
			sourceOffset := (crop.Min.Y+y)*src.Stride + (crop.Min.X+x)*4
			destinationOffset := y*linearSource.Stride + x*8
			alpha := src.Pix[sourceOffset+3]
			alpha16 := uint16(alpha) * 257
			for channel := 0; channel < 3; channel++ {
				encoded := 0.0
				if alpha > 0 {
					encoded = min(float64(src.Pix[sourceOffset+channel])/float64(alpha), 1)
				}
				linearPremultiplied := srgbDecode(encoded) * float64(alpha16)
				binary.BigEndian.PutUint16(linearSource.Pix[destinationOffset+channel*2:], uint16(math.Round(linearPremultiplied)))
			}
			binary.BigEndian.PutUint16(linearSource.Pix[destinationOffset+6:], alpha16)
		}
	}

	linearOutput := image.NewRGBA64(image.Rect(0, 0, targetWidth, targetHeight))
	draw.CatmullRom.Scale(linearOutput, linearOutput.Bounds(), linearSource, linearSource.Bounds(), draw.Src, nil)
	return encodeLinearRGBA64(linearOutput)
}

func encodeLinearRGBA64(src *image.RGBA64) *image.RGBA {
	output := image.NewRGBA(src.Bounds())
	for y := 0; y < src.Bounds().Dy(); y++ {
		for x := 0; x < src.Bounds().Dx(); x++ {
			sourceOffset := y*src.Stride + x*8
			destinationOffset := y*output.Stride + x*4
			alpha16 := binary.BigEndian.Uint16(src.Pix[sourceOffset+6:])
			alpha8 := uint8((uint32(alpha16) + 128) / 257) //nolint:gosec // bounded 16-to-8-bit conversion
			for channel := 0; channel < 3; channel++ {
				linearPremultiplied := binary.BigEndian.Uint16(src.Pix[sourceOffset+channel*2:])
				linear := 0.0
				if alpha16 > 0 {
					linear = min(float64(linearPremultiplied)/float64(alpha16), 1)
				}
				output.Pix[destinationOffset+channel] = clampByte(srgbEncode(linear) * float64(alpha8))
			}
			output.Pix[destinationOffset+3] = alpha8
		}
	}
	return output
}

func srgbDecode(encoded float64) float64 {
	if encoded <= 0.04045 {
		return encoded / 12.92
	}
	return math.Pow((encoded+0.055)/1.055, 2.4)
}

func srgbEncode(linear float64) float64 {
	if linear <= 0.0031308 {
		return linear * 12.92
	}
	return 1.055*math.Pow(linear, 1.0/2.4) - 0.055
}
