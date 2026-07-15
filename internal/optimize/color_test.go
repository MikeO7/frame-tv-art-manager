package optimize

import (
	"math"
	"testing"
)

func TestRGBToLabStandardsVectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		r, g, b   uint8
		wantL     float64
		wantA     float64
		wantB     float64
		tolerance float64
	}{
		{name: "black", wantL: 0, wantA: 0, wantB: 0, tolerance: 0.01},
		{name: "white", r: 255, g: 255, b: 255, wantL: 100, wantA: 0, wantB: 0, tolerance: 0.02},
		{name: "sRGB red in D50 Lab", r: 255, wantL: 54.29, wantA: 80.81, wantB: 69.89, tolerance: 0.12},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			l, a, b := rgbToLab(test.r, test.g, test.b)
			if math.Abs(l-test.wantL) > test.tolerance || math.Abs(a-test.wantA) > test.tolerance || math.Abs(b-test.wantB) > test.tolerance {
				t.Fatalf("rgbToLab(%d,%d,%d) = (%.4f, %.4f, %.4f), want (%.2f, %.2f, %.2f)",
					test.r, test.g, test.b, l, a, b, test.wantL, test.wantA, test.wantB)
			}
		})
	}
}

func TestCIEDE2000SharmaReferencePairs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		left, right labColor
		want        float64
	}{
		{left: labColor{50, 2.6772, -79.7751}, right: labColor{50, 0, -82.7485}, want: 2.0425},
		{left: labColor{50, 3.1571, -77.2803}, right: labColor{50, 0, -82.7485}, want: 2.8615},
		{left: labColor{50, 2.8361, -74.0200}, right: labColor{50, 0, -82.7485}, want: 3.4412},
		{left: labColor{50, -1.3802, -84.2814}, right: labColor{50, 0, -82.7485}, want: 1.0000},
	}
	for _, test := range tests {
		if got := ciede2000(test.left, test.right); math.Abs(got-test.want) > 0.0001 {
			t.Errorf("ciede2000(%+v, %+v) = %.6f, want %.4f", test.left, test.right, got, test.want)
		}
	}
}
