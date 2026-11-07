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

// autoOnBoundaryAt returns the scheduled power-on time whose wake window
// contains now: from autoOnTime until graceHours later, in timezone. The
// boundary identifies one wake, so a TV is woken at most once per day.
func autoOnBoundaryAt(autoOnTime string, graceHours float64, timezone string, now time.Time) (time.Time, bool) {
	clock, err := time.Parse("15:04", autoOnTime)
	if err != nil {
		return time.Time{}, false
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, false
	}
	now = now.In(location)
	grace := time.Duration(graceHours * float64(time.Hour))
	today := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	for _, boundary := range []time.Time{today, today.AddDate(0, 0, -1)} {
		if !now.Before(boundary) && now.Before(boundary.Add(grace)) {
			return boundary, true
		}
	}
	return time.Time{}, false
}
