package optimize

import (
	"image"
	"testing"
)

func BenchmarkGenerateSaliencyMap(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 256, 256))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateSaliencyMap(src)
	}
}
