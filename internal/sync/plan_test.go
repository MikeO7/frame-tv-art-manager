package sync

import (
	"testing"
	"time"
)

func TestPlan_IsWithinAutoOffWindow_Errors(t *testing.T) {
	now := time.Now()

	// Case 1: Empty time format
	if isWithinAutoOffWindowAt("", 2.0, "UTC", now) {
		t.Errorf("expected false for empty autoOffTime")
	}

	// Case 2: Missing colon
	if isWithinAutoOffWindowAt("2300", 2.0, "UTC", now) {
		t.Errorf("expected false for missing colon in time")
	}

	// Case 3: Non-numeric hour
	if isWithinAutoOffWindowAt("xx:00", 2.0, "UTC", now) {
		t.Errorf("expected false for non-numeric hour")
	}

	// Case 4: Non-numeric minute
	if isWithinAutoOffWindowAt("23:xx", 2.0, "UTC", now) {
		t.Errorf("expected false for non-numeric minute")
	}

	// Case 5: Invalid timezone
	if isWithinAutoOffWindowAt("23:00", 2.0, "Invalid/Zone", now) {
		t.Errorf("expected false for invalid timezone")
	}
}

func TestPlan_FormatGraceDisplay(t *testing.T) {
	if formatGraceDisplay(2.0) != "2" {
		t.Errorf("expected '2', got %q", formatGraceDisplay(2.0))
	}
	if formatGraceDisplay(2.5) != "2.5" {
		t.Errorf("expected '2.5', got %q", formatGraceDisplay(2.5))
	}
}
