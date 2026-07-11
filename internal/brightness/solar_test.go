package brightness

import (
	"errors"
	"log/slog"
	"testing"
	"time"
)

func TestJulianCenturyJanuaryAndJune(t *testing.T) {
	j2000 := time.Date(2000, time.January, 1, 12, 0, 0, 0, time.UTC)
	if got := julianCentury(j2000); got < -0.001 || got > 0.001 {
		t.Fatalf("julianCentury(J2000) = %v, want near 0", got)
	}
	if january, june := julianCentury(time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)),
		julianCentury(time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC)); january >= june {
		t.Fatalf("January century %v should precede June %v", january, june)
	}
}

func TestGetTargetValueLoggingPaths(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	manual := 4
	if got := GetTargetValue(nil, 2, 10, &manual, logger); got == nil || *got != manual {
		t.Fatalf("manual brightness = %v", got)
	}
	lat, lon := 40.0, -105.0
	valid := &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "UTC"}
	if got := GetTargetValue(valid, 2, 10, nil, logger); got == nil {
		t.Fatal("expected logged solar brightness")
	}
	invalid := &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "invalid"}
	if got := GetTargetValue(invalid, 2, 10, nil, logger); got != nil {
		t.Fatalf("invalid timezone returned %v", *got)
	}
	if got := trySolarBrightness(&SolarLocation{Latitude: &lat}, 2, 10, logger); got != nil {
		t.Fatalf("incomplete location returned %v", *got)
	}
}

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
			got := brightnessFromElevation(tc.elevation, tc.min, tc.max)

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

	elev := sunElevation(denverLat, denverLon, summerNoon)
	if elev < 50 || elev > 80 {
		t.Errorf("Denver summer noon elevation = %.1f°, expected 50-80°", elev)
	}

	// Same location at midnight — sun should be well below horizon.
	midnight := time.Date(2024, 6, 22, 7, 0, 0, 0, time.UTC) // 01:00 MDT
	elevNight := sunElevation(denverLat, denverLon, midnight)
	if elevNight > 0 {
		t.Errorf("Denver midnight elevation = %.1f°, expected negative", elevNight)
	}
}

func TestCalculate_NilCoords(t *testing.T) {
	t.Parallel()

	result, err := Calculate(nil, nil, "UTC", 2, 10)
	if err == nil {
		t.Fatal("expected error ErrSolarDisabled, got nil")
	}
	if !errors.Is(err, ErrSolarDisabled) {
		t.Fatalf("expected ErrSolarDisabled, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for nil coordinates, got %d", *result)
	}
}

func TestCalculate_InvalidTimezone(t *testing.T) {
	lat := 40.0
	lon := -70.0
	_, err := Calculate(&lat, &lon, "Invalid/Zone", 2, 10)
	if err == nil {
		t.Error("expected error for invalid timezone")
	}
}

func TestGetTargetValue(t *testing.T) {
	t.Parallel()

	lat := 40.7128
	lon := -74.0060
	manual := 5

	t.Run("solar enabled", func(t *testing.T) {
		loc := &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "UTC"}
		val := GetTargetValue(loc, 2, 10, nil, nil)
		if val == nil {
			t.Fatal("expected solar brightness value")
		}
		if *val < 2 || *val > 10 {
			t.Errorf("brightness %d out of range", *val)
		}
	})

	t.Run("manual fallback", func(t *testing.T) {
		val := GetTargetValue(nil, 0, 0, &manual, nil)
		if val == nil || *val != 5 {
			t.Errorf("expected manual brightness 5, got %v", val)
		}
	})

	t.Run("solar invalid timezone falls back to manual", func(t *testing.T) {
		loc := &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "Invalid/Zone"}
		val := GetTargetValue(loc, 2, 10, &manual, nil)
		if val == nil || *val != 5 {
			t.Errorf("expected manual fallback, got %v", val)
		}
	})

	t.Run("nil when unset", func(t *testing.T) {
		if val := GetTargetValue(nil, 0, 0, nil, nil); val != nil {
			t.Errorf("expected nil, got %d", *val)
		}
	})
}
