//nolint:goconst
package schedule

import (
	"testing"
	"time"
)

const time2200 = "22:00"

func TestIsWithinAutoOffWindow(t *testing.T) {
	t.Parallel()

	utc := "UTC"

	tests := []struct {
		name    string
		offTime string
		grace   float64
		tz      string
		now     time.Time
		want    bool
	}{
		{
			name:    "empty off time",
			offTime: "",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "within window",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "at exact off time",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 0, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "at exact grace end",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
			want:    false, // exclusive end
		},
		{
			name:    "before window",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 21, 59, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "after grace period",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 0, 30, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "midnight wrap — in yesterday's window",
			offTime: "23:00",
			grace:   3,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 1, 0, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "midnight wrap — past yesterday's grace",
			offTime: "23:00",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 1, 30, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "fractional grace hours",
			offTime: time2200,
			grace:   1.5,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 23, 20, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "invalid timezone returns false",
			offTime: time2200,
			grace:   2,
			tz:      "Invalid/Zone",
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IsWithinAutoOffWindowAt(tc.offTime, tc.grace, tc.tz, tc.now)
			if got != tc.want {
				t.Errorf("IsWithinAutoOffWindowAt(%q, %.1f, %q, %v) = %v, want %v",
					tc.offTime, tc.grace, tc.tz, tc.now, got, tc.want)
			}
		})
	}
}

func TestIsWithinAutoOffWindow_Live(t *testing.T) {
	// Call the live version just for coverage.
	// We can't easily assert the result since it depends on the current time,
	// but we can pass an empty offTime to guarantee a false result.
	if IsWithinAutoOffWindow("", 2, "UTC") != false {
		t.Error("expected false for empty offTime")
	}
}

func TestFormatGraceDisplay(t *testing.T) {
	tests := []struct {
		hours float64
		want  string
	}{
		{2.0, "2"},
		{1.5, "1.5"},
		{0.0, "0"},
		{12.0, "12"},
		{0.1, "0.1"},
	}

	for _, tc := range tests {
		got := FormatGraceDisplay(tc.hours)
		if got != tc.want {
			t.Errorf("FormatGraceDisplay(%.1f) = %q, want %q", tc.hours, got, tc.want)
		}
	}
}
