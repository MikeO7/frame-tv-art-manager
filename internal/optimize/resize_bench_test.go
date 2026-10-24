package optimize

import (
	"image"
	"testing"
)

func BenchmarkGalleryMasterPolish(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		galleryMasterPolishWithWorkers(img, defaultPixelWorkers())
	}
}

func BenchmarkApplyCanvasTexture(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		applyCanvasTextureWithWorkers(img, 5, defaultPixelWorkers())
	}
}

func BenchmarkRgbToLab(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rgbToLab(100, 150, 200)
	}
}
