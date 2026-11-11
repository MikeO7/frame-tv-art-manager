package optimize

import (
	"image"
	"testing"
)

// benchmarkPicture is a 4K frame of gradients, texture, bright highlights and
// hard edges. A blank frame would skip the highlight roll-off and every
// data-dependent branch the effects take on real artwork.
func benchmarkPicture() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	state := uint32(1)
	for y := range 2160 {
		for x := range 3840 {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			grain := int(state>>28) - 8
			red, green, blue := 40+x*200/3840+grain, 30+y*210/2160+grain, 128+(x+y)%97+grain
			if (x/240+y/270)%3 == 0 {
				red, green, blue = 250+grain/2, 244+grain/2, 236+grain/2
			}
			offset := img.PixOffset(x, y)
			img.Pix[offset], img.Pix[offset+1] = clampByte(float64(red)), clampByte(float64(green))
			img.Pix[offset+2], img.Pix[offset+3] = clampByte(float64(blue)), 255
		}
	}
	return img
}

func BenchmarkGalleryMasterPolish(b *testing.B) {
	source := benchmarkPicture()
	img := image.NewRGBA(source.Bounds())
	for b.Loop() {
		// The polish works in place, so each run starts from the picture.
		b.StopTimer()
		copy(img.Pix, source.Pix)
		b.StartTimer()
		galleryMasterPolish(img)
	}
}

func BenchmarkApplyCanvasTexture(b *testing.B) {
	img := benchmarkPicture()
	for b.Loop() {
		applyCanvasTexture(img, 5)
	}
}

func BenchmarkRgbToLab(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rgbToLab(100, 150, 200)
	}
}
