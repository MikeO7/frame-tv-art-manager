package brightness

import (
	"bytes"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

func TestJulianCenturyJanuaryAndJune(t *testing.T) {
	j2000 := time.Date(2000, time.January, 1, 12, 0, 0, 0, time.UTC)
	if got := julianCentury(j2000); math.Abs(got) > 1e-12 {
		t.Fatalf("julianCentury(J2000) = %v, want 0", got)
	}
	if january, june := julianCentury(time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)),
		julianCentury(time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC)); january >= june {
		t.Fatalf("January century %v should precede June %v", january, june)
	}
}

func TestSunElevationMatchesNOAAGoldenVector(t *testing.T) {
	t.Parallel()

	// NOAA General Solar Position Calculations, independently evaluated for
	// Denver at this UTC instant: equation of time -5.86 minutes and geometric
	// solar elevation 26.03 degrees.
	got := sunElevation(39.7392, -104.9903, time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC))
	if math.Abs(got-26.03) > 0.1 {
		t.Fatalf("sunElevation() = %.4f degrees, want 26.03 (+/- 0.1)", got)
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

func TestCalculateAtRequiresCompleteLocation(t *testing.T) {
	t.Parallel()
	lat, lon := 40.0, -70.0
	tests := []struct {
		name     string
		location *SolarLocation
		disabled bool
	}{
		{name: "nil location", disabled: true},
		{name: "missing coordinates", location: &SolarLocation{Timezone: "UTC"}, disabled: true},
		{name: "missing longitude", location: &SolarLocation{Latitude: &lat, Timezone: "UTC"}, disabled: true},
		{name: "invalid timezone", location: &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "Invalid/Zone"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := CalculateAt(test.location, 2, 10, time.Time{})
			if err == nil || result != nil || errors.Is(err, ErrSolarDisabled) != test.disabled {
				t.Fatalf("CalculateAt() = %v, %v; want nil result, disabled=%t", result, err, test.disabled)
			}
		})
	}
}

func TestGetTargetValueAtPrefersSolarThenManual(t *testing.T) {
	t.Parallel()
	lat, lon := 40.7128, -74.0060
	manual := 5
	now := time.Date(2025, time.June, 21, 17, 0, 0, 0, time.UTC)
	solar := &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "UTC"}
	solarWant, err := CalculateAt(solar, 2, 10, now)
	if err != nil {
		t.Fatalf("CalculateAt() error = %v", err)
	}
	tests := []struct {
		name     string
		options  TargetOptions
		want     *int
		wantLog  string
		wantWarn bool
	}{
		{name: "solar wins over manual", options: TargetOptions{Location: solar, Min: 2, Max: 10, Manual: &manual, Now: now}, want: solarWant, wantLog: "solar brightness"},
		{name: "manual without location", options: TargetOptions{Manual: &manual, Now: now}, want: &manual, wantLog: "manual brightness"},
		{
			name: "invalid timezone falls back to manual",
			options: TargetOptions{
				Location: &SolarLocation{Latitude: &lat, Longitude: &lon, Timezone: "Invalid/Zone"},
				Min:      2, Max: 10, Manual: &manual, Now: now,
			},
			want: &manual, wantLog: "manual brightness", wantWarn: true,
		},
		{name: "incomplete location is silently disabled", options: TargetOptions{Location: &SolarLocation{Latitude: &lat}, Now: now}},
		{name: "unset", options: TargetOptions{Now: now}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			got := GetTargetValueAt(test.options, slog.New(slog.NewTextHandler(&logs, nil)))
			if (got == nil) != (test.want == nil) || (got != nil && *got != *test.want) {
				t.Fatalf("GetTargetValueAt() = %v, want %v", got, test.want)
			}
			if !strings.Contains(logs.String(), test.wantLog) {
				t.Fatalf("logs = %q, want %q", logs.String(), test.wantLog)
			}
			if warned := strings.Contains(logs.String(), "level=WARN"); warned != test.wantWarn {
				t.Fatalf("warning logged = %t, want %t: %s", warned, test.wantWarn, logs.String())
			}
			if nilLogger := GetTargetValueAt(test.options, nil); (nilLogger == nil) != (got == nil) {
				t.Fatalf("nil logger changed the result: %v vs %v", nilLogger, got)
			}
		})
	}
}
