package themes

import (
	"strings"
	"testing"
	"time"
)

func TestActivationActiveAt(t *testing.T) {
	t.Parallel()
	christmas := Activation{Mode: ModeWindow, Start: "12-01", End: "01-06"}
	summer := Activation{Mode: ModeWindow, Start: "06-01", End: "08-31"}
	leapStart := Activation{Mode: ModeWindow, Start: "02-29", End: "03-10"}
	leapEnd := Activation{Mode: ModeWindow, Start: "02-20", End: "02-29"}
	leapOnly := Activation{Mode: ModeWindow, Start: "02-29", End: "02-29"}
	tests := []struct {
		name       string
		activation Activation
		date       string
		want       bool
	}{
		{"always", Activation{Mode: ModeAlways}, "2026-07-04", true},
		{"off", Activation{Mode: ModeOff}, "2026-12-25", false},
		{"unknown mode never active", Activation{Mode: "sometimes"}, "2026-12-25", false},
		{"invalid window never active", Activation{Mode: ModeWindow, Start: "12-01"}, "2026-12-25", false},
		{"window start is inclusive", summer, "2026-06-01", true},
		{"window end is inclusive", summer, "2026-08-31", true},
		{"day before window", summer, "2026-05-31", false},
		{"day after window", summer, "2026-09-01", false},
		{"wrapping window start", christmas, "2026-12-01", true},
		{"wrapping window new year's eve", christmas, "2026-12-31", true},
		{"wrapping window new year's day", christmas, "2027-01-01", true},
		{"wrapping window end", christmas, "2027-01-06", true},
		{"after wrapping window", christmas, "2027-01-07", false},
		{"before wrapping window", christmas, "2026-11-30", false},
		{"wrapping window midsummer", christmas, "2026-07-15", false},
		{"single day", Activation{Mode: ModeWindow, Start: "10-31", End: "10-31"}, "2026-10-31", true},
		{"single day next day", Activation{Mode: ModeWindow, Start: "10-31", End: "10-31"}, "2026-11-01", false},
		{"leap start in leap year", leapStart, "2028-02-29", true},
		{"leap start day before in leap year", leapStart, "2028-02-28", false},
		{"leap start begins March 1 in common year", leapStart, "2027-03-01", true},
		{"leap start excludes Feb 28 in common year", leapStart, "2027-02-28", false},
		{"leap end in leap year", leapEnd, "2028-02-29", true},
		{"leap end ends Feb 28 in common year", leapEnd, "2027-02-28", true},
		{"leap end excludes March 1 in common year", leapEnd, "2027-03-01", false},
		{"leap-day-only window in leap year", leapOnly, "2028-02-29", true},
		{"leap-day-only window never fires in common year Feb 28", leapOnly, "2027-02-28", false},
		{"leap-day-only window never fires in common year Mar 1", leapOnly, "2027-03-01", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			date, err := time.Parse("2006-01-02", test.date)
			if err != nil {
				t.Fatal(err)
			}
			if got := test.activation.ActiveAt(date.Add(12 * time.Hour)); got != test.want {
				t.Fatalf("%+v.ActiveAt(%s) = %t, want %t", test.activation, test.date, got, test.want)
			}
		})
	}
}

func TestActivationUsesTheClockOfTheSuppliedLocation(t *testing.T) {
	t.Parallel()
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	christmas := Activation{Mode: ModeWindow, Start: "12-01", End: "01-06"}
	// 03:00 UTC on December 1 is still the evening of November 30 in Denver.
	instant := time.Date(2026, time.December, 1, 3, 0, 0, 0, time.UTC)
	if !christmas.ActiveAt(instant) {
		t.Fatal("window is not active on December 1 UTC")
	}
	if christmas.ActiveAt(instant.In(denver)) {
		t.Fatal("window is active on November 30 in Denver")
	}
	// 05:00 UTC on January 7 is still January 6 in Denver.
	end := time.Date(2027, time.January, 7, 5, 0, 0, 0, time.UTC)
	if christmas.ActiveAt(end) || !christmas.ActiveAt(end.In(denver)) {
		t.Fatal("window end did not follow the supplied location")
	}
}

func TestActivationValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		activation Activation
		want       string
	}{
		{"always", Activation{Mode: ModeAlways}, ""},
		{"off", Activation{Mode: ModeOff}, ""},
		{"window", Activation{Mode: ModeWindow, Start: "12-01", End: "01-06"}, ""},
		{"leap day bound", Activation{Mode: ModeWindow, Start: "02-29", End: "03-01"}, ""},
		{"unknown mode", Activation{Mode: "weekly"}, "must be always, off, or window"},
		{"empty mode", Activation{}, "must be always, off, or window"},
		{"always with dates", Activation{Mode: ModeAlways, Start: "01-01"}, "takes no start or end"},
		{"window without end", Activation{Mode: ModeWindow, Start: "12-01"}, "needs both start and end"},
		{"nonexistent day", Activation{Mode: ModeWindow, Start: "02-30", End: "03-01"}, "window start"},
		{"April 31", Activation{Mode: ModeWindow, Start: "04-01", End: "04-31"}, "window end"},
		{"month 13", Activation{Mode: ModeWindow, Start: "13-01", End: "01-02"}, "MM-DD"},
		{"unpadded", Activation{Mode: ModeWindow, Start: "1-6", End: "01-07"}, "MM-DD"},
		{"with year", Activation{Mode: ModeWindow, Start: "2026-12-01", End: "01-06"}, "MM-DD"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.activation.Validate()
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestActivationDescribe(t *testing.T) {
	t.Parallel()
	for activation, want := range map[Activation]string{
		{Mode: ModeAlways}: "always shown",
		{Mode: ModeOff}:    "off (hidden)",
		{Mode: ModeWindow, Start: "12-01", End: "01-06"}: "shown every year from 12-01 through 01-06",
	} {
		if got := activation.Describe(); got != want {
			t.Fatalf("Describe(%+v) = %q, want %q", activation, got, want)
		}
	}
}
