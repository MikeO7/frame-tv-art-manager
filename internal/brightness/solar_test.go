package brightness

import (
	"testing"
	"time"
)

func TestBrightnessFromElevation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		elevation float64
		min       int
		max       int
		want      int
	}{
		{
			name:      "below horizon returns min",
			elevation: -10,
			min:       2,
			max:       10,
			want:      2,
		},
		{
			name:      "at horizon returns min",
			elevation: 0,
			min:       2,
			max:       10,
			want:      2,
		},
		{
			name:      "near zenith returns near max",
			elevation: 89,
			min:       2,
			max:       10,
			want:      7, // ~0.7^(1.0^0.678) = 0.7 → 2+int(8*0.7) = 7
		},
		{
			name:      "low elevation returns low brightness",
			elevation: 5,
			min:       2,
			max:       10,
			want:      3, // low sun = high air mass = low irradiance
		},
		{
			name:      "mid elevation returns mid brightness",
			elevation: 45,
			min:       2,
			max:       10,
			want:      7, // moderate sun
		},
		{
			name:      "custom range 0-50",
			elevation: 60,
			min:       0,
			max:       50,
			want:      33, // approximately 0.7^(airmass^0.678) * 50
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BrightnessFromElevation(tc.elevation, tc.min, tc.max)

			// Allow ±1 for floating-point rounding in brightness mapping.
			if got < tc.want-1 || got > tc.want+1 {
				t.Errorf("BrightnessFromElevation(%v, %d, %d) = %d, want ~%d (±1)",
					tc.elevation, tc.min, tc.max, got, tc.want)
			}
		})
	}
}

func TestSunElevation_KnownValues(t *testing.T) {
	t.Parallel()

	// Denver, CO at solar noon on summer solstice — sun should be high.
	denverLat := 39.7392
	denverLon := -104.9903
	// June 21, 2024 ~19:00 UTC ≈ 13:00 MDT (near solar noon).
	summerNoon := time.Date(2024, 6, 21, 19, 0, 0, 0, time.UTC)

	elev := SunElevation(denverLat, denverLon, summerNoon)
	if elev < 50 || elev > 80 {
		t.Errorf("Denver summer noon elevation = %.1f°, expected 50-80°", elev)
	}

	// Same location at midnight — sun should be well below horizon.
	midnight := time.Date(2024, 6, 22, 7, 0, 0, 0, time.UTC) // 01:00 MDT
	elevNight := SunElevation(denverLat, denverLon, midnight)
	if elevNight > 0 {
		t.Errorf("Denver midnight elevation = %.1f°, expected negative", elevNight)
	}
}

func TestCalculate_NilCoords(t *testing.T) {
	t.Parallel()

	result, err := Calculate(nil, nil, "UTC", 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for nil coordinates, got %d", *result)
	}
}
