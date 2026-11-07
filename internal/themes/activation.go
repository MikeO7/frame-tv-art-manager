package themes

import (
	"errors"
	"fmt"
	"time"
)

// monthDay is a calendar day independent of year, ordered month first.
type monthDay struct {
	month time.Month
	day   int
}

func (value monthDay) before(other monthDay) bool {
	return value.month < other.month || (value.month == other.month && value.day < other.day)
}

// parseMonthDay accepts MM-DD for any day that exists in a leap year, so
// 02-29 is a valid boundary.
func parseMonthDay(value string) (monthDay, error) {
	parsed, err := time.Parse(monthDayLayout, value)
	if err != nil || parsed.Format(monthDayLayout) != value {
		return monthDay{}, fmt.Errorf("date %q must be MM-DD, for example 12-01", value)
	}
	check := time.Date(leapYearForMonthDays, parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC)
	if check.Month() != parsed.Month() || check.Day() != parsed.Day() {
		return monthDay{}, fmt.Errorf("date %q does not exist", value)
	}
	return monthDay{month: parsed.Month(), day: parsed.Day()}, nil
}

// Validate reports whether the activation rule is complete and well formed.
func (activation Activation) Validate() error {
	switch activation.Mode {
	case ModeAlways, ModeOff:
		if activation.Start != "" || activation.End != "" {
			return fmt.Errorf("activation mode %q takes no start or end date", activation.Mode)
		}
		return nil
	case ModeWindow:
		if activation.Start == "" || activation.End == "" {
			return errors.New("a window activation needs both start and end dates (MM-DD)")
		}
		if _, err := parseMonthDay(activation.Start); err != nil {
			return fmt.Errorf("window start: %w", err)
		}
		if _, err := parseMonthDay(activation.End); err != nil {
			return fmt.Errorf("window end: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("activation mode %q must be always, off, or window", activation.Mode)
	}
}

// ActiveAt reports whether the rule is active on the calendar day of now in
// now's location. Window bounds are inclusive. A window whose start falls
// after its end wraps the year end, so 12-01..01-06 covers December 1
// through January 6. A 02-29 bound compares by month and day: in a non-leap
// year a window starting 02-29 begins on March 1 and one ending 02-29 ends on
// February 28. An invalid rule is never active.
func (activation Activation) ActiveAt(now time.Time) bool {
	switch activation.Mode {
	case ModeAlways:
		return true
	case ModeWindow:
		start, startErr := parseMonthDay(activation.Start)
		end, endErr := parseMonthDay(activation.End)
		if startErr != nil || endErr != nil {
			return false
		}
		today := monthDay{month: now.Month(), day: now.Day()}
		if !end.before(start) {
			return !today.before(start) && !end.before(today)
		}
		return !today.before(start) || !end.before(today)
	default:
		return false
	}
}

// Describe returns a short human description of the rule.
func (activation Activation) Describe() string {
	switch activation.Mode {
	case ModeAlways:
		return "always shown"
	case ModeWindow:
		return fmt.Sprintf("shown every year from %s through %s", activation.Start, activation.End)
	default:
		return "off (hidden)"
	}
}
