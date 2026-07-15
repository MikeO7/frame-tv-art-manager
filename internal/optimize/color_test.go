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
		{left: labColor{50, -1.1848, -84.8006}, right: labColor{50, 0, -82.7485}, want: 1.0000},
		{left: labColor{50, -0.9009, -85.5211}, right: labColor{50, 0, -82.7485}, want: 1.0000},
		{left: labColor{50, 0, 0}, right: labColor{50, -1, 2}, want: 2.3669},
		{left: labColor{50, -1, 2}, right: labColor{50, 0, 0}, want: 2.3669},
		{left: labColor{50, 2.49, -0.001}, right: labColor{50, -2.49, 0.0009}, want: 7.1792},
		{left: labColor{50, 2.49, -0.001}, right: labColor{50, -2.49, 0.0010}, want: 7.1792},
		{left: labColor{50, 2.49, -0.001}, right: labColor{50, -2.49, 0.0011}, want: 7.2195},
		{left: labColor{50, 2.49, -0.001}, right: labColor{50, -2.49, 0.0012}, want: 7.2195},
		{left: labColor{50, -0.001, 2.49}, right: labColor{50, 0.0009, -2.49}, want: 4.8045},
		{left: labColor{50, -0.001, 2.49}, right: labColor{50, 0.0010, -2.49}, want: 4.8045},
		{left: labColor{50, -0.001, 2.49}, right: labColor{50, 0.0011, -2.49}, want: 4.7461},
		{left: labColor{50, 2.5, 0}, right: labColor{50, 0, -2.5}, want: 4.3065},
		{left: labColor{50, 2.5, 0}, right: labColor{73, 25, -18}, want: 27.1492},
		{left: labColor{50, 2.5, 0}, right: labColor{61, -5, 29}, want: 22.8977},
		{left: labColor{50, 2.5, 0}, right: labColor{56, -27, -3}, want: 31.9030},
		{left: labColor{50, 2.5, 0}, right: labColor{58, 24, 15}, want: 19.4535},
		{left: labColor{50, 2.5, 0}, right: labColor{50, 3.1736, 0.5854}, want: 1.0000},
		{left: labColor{50, 2.5, 0}, right: labColor{50, 3.2972, 0}, want: 1.0000},
		{left: labColor{50, 2.5, 0}, right: labColor{50, 1.8634, 0.5757}, want: 1.0000},
		{left: labColor{50, 2.5, 0}, right: labColor{50, 3.2592, 0.3350}, want: 1.0000},
		{left: labColor{60.2574, -34.0099, 36.2677}, right: labColor{60.4626, -34.1751, 39.4387}, want: 1.2644},
		{left: labColor{63.0109, -31.0961, -5.8663}, right: labColor{62.8187, -29.7946, -4.0864}, want: 1.2630},
		{left: labColor{61.2901, 3.7196, -5.3901}, right: labColor{61.4292, 2.2480, -4.9620}, want: 1.8731},
		{left: labColor{35.0831, -44.1164, 3.7933}, right: labColor{35.0232, -40.0716, 1.5901}, want: 1.8645},
		{left: labColor{22.7233, 20.0904, -46.6940}, right: labColor{23.0331, 14.9730, -42.5619}, want: 2.0373},
		{left: labColor{36.4612, 47.8580, 18.3852}, right: labColor{36.2715, 50.5065, 21.2231}, want: 1.4146},
		{left: labColor{90.8027, -2.0831, 1.4410}, right: labColor{91.1528, -1.6435, 0.0447}, want: 1.4441},
		{left: labColor{90.9257, -0.5406, -0.9208}, right: labColor{88.6381, -0.8985, -0.7239}, want: 1.5381},
		{left: labColor{6.7747, -0.2908, -2.4247}, right: labColor{5.8714, -0.0985, -2.2286}, want: 0.6377},
		{left: labColor{2.0776, 0.0795, -1.1350}, right: labColor{0.9033, -0.0636, -0.5514}, want: 0.9082},
	}
	for _, test := range tests {
		if got := ciede2000(test.left, test.right); math.Abs(got-test.want) > 0.0001 {
			t.Errorf("ciede2000(%+v, %+v) = %.6f, want %.4f", test.left, test.right, got, test.want)
		}
	}
}
