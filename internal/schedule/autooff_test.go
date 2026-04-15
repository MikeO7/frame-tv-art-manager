package schedule

import (
	"testing"
	"time"
)

func TestIsWithinAutoOffWindow(t *testing.T) {
	t.Parallel()

	utc := "UTC"

	tests := []struct {
		name      string
		offTime   string
		grace     float64
		tz        string
		now       time.Time
		want      bool
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
			offTime: "22:00",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "at exact off time",
			offTime: "22:00",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 0, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "at exact grace end",
			offTime: "22:00",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
			want:    false, // exclusive end
		},
		{
			name:    "before window",
			offTime: "22:00",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 21, 59, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "after grace period",
			offTime: "22:00",
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
			offTime: "22:00",
			grace:   1.5,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 23, 20, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "invalid timezone returns false",
			offTime: "22:00",
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
