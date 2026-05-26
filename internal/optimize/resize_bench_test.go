package optimize

import (
	"image"
	"testing"
)

func BenchmarkGalleryMasterPolish(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		galleryMasterPolish(img)
	}
}

func BenchmarkApplyCanvasTexture(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		applyCanvasTexture(img, 5)
	}
}

func BenchmarkRgbToLab(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rgbToLab(100, 150, 200)
	}
}

func BenchmarkGenerateBMSMap(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generateBMSMap(img)
	}
}
