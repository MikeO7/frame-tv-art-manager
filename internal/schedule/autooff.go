// Package schedule provides time-window utilities for the auto-off feature.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IsWithinAutoOffWindow returns true if the current time falls within the
// auto-off window: [offTime, offTime + graceHours). Handles midnight wrap.
//
// autoOffTime is a 24-hour string like "22:00". If empty, returns false.
// graceHours is how many hours after offTime to keep the window open.
// tz is an IANA timezone string (e.g. "America/Denver").
func IsWithinAutoOffWindow(autoOffTime string, graceHours float64, tz string) bool {
	return IsWithinAutoOffWindowAt(autoOffTime, graceHours, tz, time.Now())
}

// IsWithinAutoOffWindowAt is the testable version of IsWithinAutoOffWindow
// that accepts an explicit "now" time.
func IsWithinAutoOffWindowAt(autoOffTime string, graceHours float64, tz string, now time.Time) bool {
	if autoOffTime == "" {
		return false
	}

	parts := strings.SplitN(autoOffTime, ":", 2)
	if len(parts) != 2 {
		return false
	}

	offHour, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	offMinute, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}

	now = now.In(loc)
	graceDuration := time.Duration(graceHours * float64(time.Hour))

	// Build today's off time.
	todayOff := time.Date(now.Year(), now.Month(), now.Day(), offHour, offMinute, 0, 0, loc)
	todayGraceEnd := todayOff.Add(graceDuration)

	// Check today's window.
	if !now.Before(todayOff) && now.Before(todayGraceEnd) {
		return true
	}

	// Check yesterday's window (handles midnight wrap).
	yesterdayOff := todayOff.AddDate(0, 0, -1)
	yesterdayGraceEnd := yesterdayOff.Add(graceDuration)
	if !now.Before(yesterdayOff) && now.Before(yesterdayGraceEnd) {
		return true
	}

	return false
}

// FormatGraceDisplay returns a human-readable string for the grace period,
// using integer format when the value is a whole number.
func FormatGraceDisplay(hours float64) string {
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%d", int(hours))
	}
	return fmt.Sprintf("%.1f", hours)
}
