package sync

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// isWithinAutoOffWindow returns true if the current time falls within the
// auto-off window: [offTime, offTime + graceHours). Handles midnight wrap.
func isWithinAutoOffWindow(autoOffTime string, graceHours float64, tz string) bool {
	return isWithinAutoOffWindowAt(autoOffTime, graceHours, tz, time.Now())
}

// isWithinAutoOffWindowAt accepts an explicit "now" time.
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

// formatGraceDisplay returns a human-readable string for the grace period.
func formatGraceDisplay(hours float64) string {
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%d", int(hours))
	}
	return fmt.Sprintf("%.1f", hours)
}
