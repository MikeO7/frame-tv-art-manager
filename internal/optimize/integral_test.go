package optimize

import (
	"image"
	"reflect"
	"testing"
)

func TestCalculateIntegralImage(t *testing.T) {
	saliencyMap := []float64{
		1, 2, 3,
		4, 5, 6,
		7, 8, 9,
	}
	got := calculateIntegralImage(saliencyMap, 3, 3)
	want := []float64{
		1, 3, 6,
		5, 12, 21,
		12, 27, 45,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calculateIntegralImage() = %v, want %v", got, want)
	}
}

func TestGetRectSumVariants(t *testing.T) {
	integral := calculateIntegralImage([]float64{
		1, 2, 3,
		4, 5, 6,
		7, 8, 9,
	}, 3, 3)

	t.Run("from origin", func(t *testing.T) {
		rect := image.Rect(0, 0, 1, 1)
		if got, want := getRectSum(integral, rect, 3), 12.0; got != want {
			t.Fatalf("getRectSum(%v) = %g, want %g", rect, got, want)
		}
	})

	t.Run("centered", func(t *testing.T) {
		rect := image.Rect(1, 1, 2, 2)
		if got, want := getRectSum(integral, rect, 3), 28.0; got != want {
			t.Fatalf("getRectSum(%v) = %g, want %g", rect, got, want)
		}
	})

	t.Run("striped", func(t *testing.T) {
		rect := image.Rect(0, 1, 2, 2)
		if got, want := getRectSum(integral, rect, 3), 39.0; got != want {
			t.Fatalf("getRectSum(%v) = %g, want %g", rect, got, want)
		}
	})
}
