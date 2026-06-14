package sync

import (
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestPlan_DetermineSelectedID_Shuffle(t *testing.T) {
	mappingData := map[string]string{
		"a.jpg": "id-a",
		"b.jpg": "id-b",
	}

	shuffleSettings := &samsung.SlideshowStatus{
		Type:  ssTypeShuffle,
		Value: "15",
	}

	// Should select one of the IDs from the mapping
	selected := determineSelectedID(mappingData, nil, nil, shuffleSettings)
	if selected != "id-a" && selected != "id-b" {
		t.Errorf("expected to select either id-a or id-b, got %q", selected)
	}

	// Empty mapping should return empty string
	selectedEmpty := determineSelectedID(nil, nil, nil, shuffleSettings)
	if selectedEmpty != "" {
		t.Errorf("expected empty selection, got %q", selectedEmpty)
	}
}

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
