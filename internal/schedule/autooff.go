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
// Parameters:
//   - autoOffTime: A 24-hour time string indicating when the TV should start attempting to power off.
//     If empty (""), the function immediately returns false, disabling the feature.
//   - graceHours: The duration (in hours) the power-off window remains open after autoOffTime.
//   - tz: An IANA timezone string determining the local context.
//
// Example Usage:
//
//	// Window opens at 10 PM and closes at 12 AM (midnight)
//	IsWithinAutoOffWindow("22:00", 2.0, "America/New_York")
//
//	// Window opens at 11:30 PM and closes at 1:00 AM the next day (handles wrap)
//	IsWithinAutoOffWindow("23:30", 1.5, "Europe/London")
func IsWithinAutoOffWindow(autoOffTime string, graceHours float64, tz string) bool {
	return isWithinAutoOffWindowAt(autoOffTime, graceHours, tz, time.Now())
}

// isWithinAutoOffWindowAt is the testable version of IsWithinAutoOffWindow
// that accepts an explicit "now" time.
func isWithinAutoOffWindowAt(autoOffTime string, graceHours float64, tz string, now time.Time) bool {
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
// using integer format when the value is a whole number to keep the UI clean,
// and allowing one decimal place for fractional hours.
//
// Parameters:
//   - hours: The float64 value representing the grace period duration in hours.
//
// Example Usage:
//
//	FormatGraceDisplay(2.0)  // Returns "2"
//	FormatGraceDisplay(2.5)  // Returns "2.5"
func FormatGraceDisplay(hours float64) string {
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%d", int(hours))
	}
	return fmt.Sprintf("%.1f", hours)
}
