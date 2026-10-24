package sync

import "time"

func isWithinAutoOffWindowAt(autoOffTime string, graceHours float64, timezone string, now time.Time) bool {
	clock, err := time.Parse("15:04", autoOffTime)
	if err != nil {
		return false
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return false
	}
	now = now.In(location)
	off := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	grace := time.Duration(graceHours * float64(time.Hour))
	if !now.Before(off) && now.Before(off.Add(grace)) {
		return true
	}
	yesterday := off.AddDate(0, 0, -1)
	return !now.Before(yesterday) && now.Before(yesterday.Add(grace))
}
