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

func TestPlan_IsWithinAutoOffWindow_WindowCoverage(t *testing.T) {
	now := time.Date(2026, 1, 12, 10, 30, 0, 0, time.UTC)
	if !isWithinAutoOffWindowAt("10:00", 2.0, "UTC", now) {
		t.Fatal("expected today window to match")
	}

	yesterdayWindow := time.Date(2026, 1, 12, 0, 30, 0, 0, time.UTC)
	if !isWithinAutoOffWindowAt("23:00", 2.0, "UTC", yesterdayWindow) {
		t.Fatal("expected yesterday-window carry-over match")
	}

	outside := time.Date(2026, 1, 12, 12, 0, 0, 0, time.UTC)
	if isWithinAutoOffWindowAt("11:00", 0.5, "UTC", outside) {
		t.Fatal("expected outside window to be false")
	}
}

func TestPlan_IsWithinAutoOffWindow_FloatGrace(t *testing.T) {
	now := time.Date(2026, 1, 12, 7, 15, 0, 0, time.UTC)
	if isWithinAutoOffWindowAt("07:00", 0.25, "UTC", now) {
		t.Fatal("expected outside quarter-hour grace to be false")
	}

	now = time.Date(2026, 1, 12, 7, 10, 0, 0, time.UTC)
	if !isWithinAutoOffWindowAt("07:00", 0.25, "UTC", now) {
		t.Fatal("expected inside quarter-hour grace to be true")
	}

	zoneLocation := "/this/path/does-not-exist"
	if got := isWithinAutoOffWindowAt("07:00", 0.25, zoneLocation, now); got {
		t.Fatalf("expected invalid timezone %q to be false", zoneLocation)
	}

	if got := formatGraceDisplay(1.75); got != "1.8" {
		t.Fatalf("formatGraceDisplay(1.75) = %q", got)
	}
}
