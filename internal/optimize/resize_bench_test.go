package optimize

import (
	"image"
	"testing"
)

func BenchmarkGalleryMasterPolish(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GalleryMasterPolish(img)
	}
}

func BenchmarkApplyCanvasTexture(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ApplyCanvasTexture(img, 5)
	}
}
