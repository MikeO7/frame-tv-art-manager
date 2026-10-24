package optimize

import (
	"encoding/binary"
	"image"
)

// orientationTile is the square block copied at a time so transposing
// orientations read and write within cache-resident rows.
const orientationTile = 64

// RotateImage returns img transformed for the EXIF orientation tag (values
// 1-8). Orientation 1 and unknown values return img unchanged; every other
// orientation returns a new zero-origin *image.RGBA.
func RotateImage(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	return orientRGBA(toRGBA(img), orientation)
}

// orientRGBA applies a 2-8 orientation with a single tiled pass. Source
// pixel (x, y), relative to src.Rect.Min, lands at destination byte offset
// base + x*stepX + y*stepY.
func orientRGBA(src *image.RGBA, orientation int) *image.RGBA {
	width, height := src.Rect.Dx(), src.Rect.Dy()
	destinationWidth, destinationHeight := width, height
	if orientation >= 5 {
		destinationWidth, destinationHeight = height, width
	}
	dst := image.NewRGBA(image.Rect(0, 0, destinationWidth, destinationHeight))
	base, stepX, stepY := orientationSteps(orientation, width, height, dst.Stride)
	for tileY := 0; tileY < height; tileY += orientationTile {
		for tileX := 0; tileX < width; tileX += orientationTile {
			endX := min(tileX+orientationTile, width)
			for y := tileY; y < min(tileY+orientationTile, height); y++ {
				source := src.Pix[src.PixOffset(src.Rect.Min.X+tileX, src.Rect.Min.Y+y):]
				destination := base + tileX*stepX + y*stepY
				for x := 0; x < endX-tileX; x++ {
					binary.LittleEndian.PutUint32(dst.Pix[destination:], binary.LittleEndian.Uint32(source[x*4:]))
					destination += stepX
				}
			}
		}
	}
	return dst
}

// orientationSteps returns the destination byte offset of source pixel
// (0, 0) and the offset deltas for one source step along x and y.
func orientationSteps(orientation, width, height, stride int) (int, int, int) {
	switch orientation {
	case 2: // mirror horizontal: (W-1-x, y)
		return (width - 1) * 4, -4, stride
	case 3: // rotate 180: (W-1-x, H-1-y)
		return (height-1)*stride + (width-1)*4, -4, -stride
	case 4: // mirror vertical: (x, H-1-y)
		return (height - 1) * stride, 4, -stride
	case 5: // transpose: (y, x)
		return 0, stride, 4
	case 6: // rotate 90 clockwise: (H-1-y, x)
		return (height - 1) * 4, stride, -4
	case 7: // transverse: (H-1-y, W-1-x)
		return (width-1)*stride + (height-1)*4, -stride, -4
	default: // 8, rotate 270 clockwise: (y, W-1-x)
		return (width - 1) * stride, -stride, 4
	}
}
