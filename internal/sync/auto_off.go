package sync

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func isWithinAutoOffWindowAt(autoOffTime string, graceHours float64, timezone string, now time.Time) bool {
	parts := strings.SplitN(autoOffTime, ":", 2)
	if len(parts) != 2 {
		return false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return false
	}
	now = now.In(location)
	off := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, location)
	grace := time.Duration(graceHours * float64(time.Hour))
	if !now.Before(off) && now.Before(off.Add(grace)) {
		return true
	}
	yesterday := off.AddDate(0, 0, -1)
	return !now.Before(yesterday) && now.Before(yesterday.Add(grace))
}

func formatGraceDisplay(hours float64) string {
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%d", int(hours))
	}
	return fmt.Sprintf("%.1f", hours)
}
