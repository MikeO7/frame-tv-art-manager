package sync

import (
	"testing"
	"time"
)

func TestIsWithinAutoOffWindowAt(t *testing.T) {
	t.Parallel()
	at := func(hour, minute int) time.Time { return time.Date(2026, 1, 12, hour, minute, 0, 0, time.UTC) }
	tests := []struct {
		name     string
		offTime  string
		grace    float64
		timezone string
		now      time.Time
		want     bool
	}{
		{name: "empty time", grace: 2, timezone: "UTC", now: at(23, 30)},
		{name: "missing colon", offTime: "2300", grace: 2, timezone: "UTC", now: at(23, 30)},
		{name: "non-numeric hour", offTime: "xx:00", grace: 2, timezone: "UTC", now: at(23, 30)},
		{name: "non-numeric minute", offTime: "23:xx", grace: 2, timezone: "UTC", now: at(23, 30)},
		{name: "unknown timezone", offTime: "23:00", grace: 2, timezone: "Invalid/Zone", now: at(23, 30)},
		{name: "filesystem timezone path", offTime: "07:00", grace: 0.25, timezone: "/this/path/does-not-exist", now: at(7, 10)},
		{name: "inside today's window", offTime: "10:00", grace: 2, timezone: "UTC", now: at(10, 30), want: true},
		{name: "window start is inclusive", offTime: "10:00", grace: 2, timezone: "UTC", now: at(10, 0), want: true},
		{name: "window end is exclusive", offTime: "10:00", grace: 2, timezone: "UTC", now: at(12, 0)},
		{name: "before today's window", offTime: "10:00", grace: 2, timezone: "UTC", now: at(9, 59)},
		{name: "yesterday's window carries past midnight", offTime: "23:00", grace: 2, timezone: "UTC", now: at(0, 30), want: true},
		{name: "outside half-hour grace", offTime: "11:00", grace: 0.5, timezone: "UTC", now: at(12, 0)},
		{name: "inside quarter-hour grace", offTime: "07:00", grace: 0.25, timezone: "UTC", now: at(7, 10), want: true},
		{name: "quarter-hour grace end is exclusive", offTime: "07:00", grace: 0.25, timezone: "UTC", now: at(7, 15)},
		{name: "evaluated in configured timezone", offTime: "10:00", grace: 1, timezone: "Etc/GMT-2", now: at(8, 30), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isWithinAutoOffWindowAt(test.offTime, test.grace, test.timezone, test.now); got != test.want {
				t.Fatalf("isWithinAutoOffWindowAt(%q, %v, %q, %s) = %t, want %t",
					test.offTime, test.grace, test.timezone, test.now.Format(time.RFC3339), got, test.want)
			}
		})
	}
}
