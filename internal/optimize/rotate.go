package optimize

import "image"

// RotateImage rotates the image according to the EXIF orientation tag (values 1-8).
func RotateImage(img image.Image, orientation int) image.Image {
	switch orientation {
	case 2: // Mirror horizontal
		return flipHorizontal(img)
	case 3: // 180 degrees
		return rotate180(img)
	case 4: // Mirror vertical
		return flipVertical(img)
	case 5: // Mirror horizontal and rotate 270 CW
		return rotate270(flipHorizontal(img))
	case 6: // 90 degrees CW
		return rotate90(img)
	case 7: // Mirror horizontal and rotate 90 CW
		return rotate90(flipHorizontal(img))
	case 8: // 270 degrees CW (90 CCW)
		return rotate270(img)
	default:
		return img
	}
}

func flipHorizontal(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(bounds.Max.X-1-x, y, img.At(x, y))
		}
	}
	return dest
}

func flipVertical(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(x, bounds.Max.Y-1-y, img.At(x, y))
		}
	}
	return dest
}

func rotate90(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(bounds.Max.Y-1-y, x, img.At(x, y))
		}
	}
	return dest
}

func rotate180(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(bounds.Max.X-1-x, bounds.Max.Y-1-y, img.At(x, y))
		}
	}
	return dest
}

func rotate270(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(y, bounds.Max.X-1-x, img.At(x, y))
		}
	}
	return dest
}
