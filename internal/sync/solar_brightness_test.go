package sync

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
			name:      "near zenith reaches max",
			elevation: 89,
			min:       2,
			max:       10,
			want:      10,
		},
		{
			name:      "low elevation returns low brightness",
			elevation: 5,
			min:       2,
			max:       10,
			want:      4, // low sun = high air mass = low irradiance
		},
		{
			name:      "mid elevation returns mid brightness",
			elevation: 45,
			min:       2,
			max:       10,
			want:      9, // moderate sun
		},
		{
			name:      "custom range 0-50",
			elevation: 60,
			min:       0,
			max:       50,
			want:      48, // 96% of zenith irradiance
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := brightnessFromElevation(tc.elevation, tc.min, tc.max)

			if got != tc.want {
				t.Errorf("BrightnessFromElevation(%v, %d, %d) = %d, want %d",
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
		location *solarLocation
	}{
		{name: "nil location"},
		{name: "missing coordinates", location: &solarLocation{}},
		{name: "missing longitude", location: &solarLocation{Latitude: &lat}},
		{name: "missing latitude", location: &solarLocation{Longitude: &lon}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := solarBrightnessAt(test.location, 2, 10, time.Time{})
			if result != nil || !errors.Is(err, errSolarDisabled) {
				t.Fatalf("solarBrightnessAt() = %v, %v; want nil result and errSolarDisabled", result, err)
			}
		})
	}
}

func TestGetTargetValueAtPrefersSolarThenManual(t *testing.T) {
	t.Parallel()
	lat, lon := 40.7128, -74.0060
	manual := 5
	now := time.Date(2025, time.June, 21, 17, 0, 0, 0, time.UTC)
	solar := &solarLocation{Latitude: &lat, Longitude: &lon}
	solarWant, err := solarBrightnessAt(solar, 2, 10, now)
	if err != nil {
		t.Fatalf("CalculateAt() error = %v", err)
	}
	tests := []struct {
		name    string
		options brightnessTarget
		want    *int
		wantLog string
	}{
		{name: "solar wins over manual", options: brightnessTarget{Location: solar, Min: 2, Max: 10, Manual: &manual, Now: now}, want: solarWant, wantLog: "solar brightness"},
		{name: "manual without location", options: brightnessTarget{Manual: &manual, Now: now}, want: &manual, wantLog: "manual brightness"},
		{name: "incomplete location is silently disabled", options: brightnessTarget{Location: &solarLocation{Latitude: &lat}, Now: now}},
		{name: "unset", options: brightnessTarget{Now: now}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			got := targetBrightnessAt(test.options, slog.New(slog.NewTextHandler(&logs, nil)))
			if (got == nil) != (test.want == nil) || (got != nil && *got != *test.want) {
				t.Fatalf("GetTargetValueAt() = %v, want %v", got, test.want)
			}
			if !strings.Contains(logs.String(), test.wantLog) {
				t.Fatalf("logs = %q, want %q", logs.String(), test.wantLog)
			}
			if nilLogger := targetBrightnessAt(test.options, nil); (nilLogger == nil) != (got == nil) {
				t.Fatalf("nil logger changed the result: %v vs %v", nilLogger, got)
			}
		})
	}
}
