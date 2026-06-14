package optimize

import (
	"image"
	"testing"
)

func BenchmarkGenerateSaliencyMap_Prof(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 256, 144))
	for i := 0; i < len(src.Pix); i++ {
		src.Pix[i] = uint8(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generateSaliencyMap(src)
	}
}
