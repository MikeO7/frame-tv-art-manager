package optimize

import (
	"image"
	"testing"
)

func TestClampOffsetKeepsWindowInsideSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		offset, window, size, want int
	}{
		{offset: -3, window: 4, size: 10, want: 0},
		{offset: 9, window: 4, size: 10, want: 6},
		{offset: 4, window: 3, size: 10, want: 4},
	}
	for _, test := range tests {
		if got := clampOffset(test.offset, test.window, test.size); got != test.want {
			t.Errorf("clampOffset(%d, %d, %d) = %d, want %d", test.offset, test.window, test.size, got, test.want)
		}
	}
}

func TestRefineOffsetKeepsOffsetWithoutSearchRoom(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	if got := refineOffset(img, 5, 18, 18, true); got != 5 {
		t.Fatalf("refineOffset() = %d, want unchanged offset 5", got)
	}
}
