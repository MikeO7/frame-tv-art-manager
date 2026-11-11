package sync

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogCycleSummary_RendersStatusesAndWarnings(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	logCycleSummary(logger, cycleSummary{
		CycleNum:    7,
		StartTime:   time.Date(2026, 1, 12, 8, 0, 0, 0, time.UTC),
		NextSync:    time.Date(2026, 1, 12, 8, 5, 0, 0, time.UTC),
		Location:    time.UTC,
		TotalLocal:  3,
		FromSources: 1,
		Optimized:   2,
		FailedTVs:   1,
		TVs: []tvSyncResult{
			{IP: "192.168.0.1", Model: "Mock", Status: "ok", ArtMode: true},
			{IP: "192.168.0.2", Status: statusBackoff, Model: "mocked"},
			{IP: "192.168.0.3", Status: "error", ErrorMessage: strings.Repeat("x", 80)},
		},
		Warnings: []string{
			"short warning",
			strings.Repeat("longwarning", 10),
		},
	})

	out := buf.String()
	if !strings.Contains(out, "Sync Cycle #7") {
		t.Fatalf("expected cycle header, got %q", out)
	}
	if !strings.Contains(out, "Status:     ✔ Art Mode") {
		t.Fatalf("expected OK status render, got %q", out)
	}
	if !strings.Contains(out, "Status:     ⏸ Backing off (unreachable)") {
		t.Fatalf("expected backoff status render, got %q", out)
	}
	if !strings.Contains(out, "Status:     ✘ error") {
		t.Fatalf("expected error status render, got %q", out)
	}
	if !strings.Contains(out, "⚠ Warnings during this cycle:") {
		t.Fatalf("expected warnings section, got %q", out)
	}
	if !strings.Contains(out, "1 from sources") || strings.Contains(out, "from URLs") {
		t.Fatalf("expected provider downloads labeled as sources, got %q", out)
	}
	for _, field := range []string{
		"event=sync_cycle_summary", "cycle=7", "local_artwork=3", "tv_count=3", "failed_tvs=1", "warnings=2",
	} {
		if !strings.Contains(out, field) {
			t.Errorf("summary log missing %q: %s", field, out)
		}
	}
}

func TestLogCycleSummaryUsesTheConfiguredLocationAndTheGivenNextSync(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	// 01:30 UTC on 8 November is 18:30 the evening before in Denver.
	nextSync := time.Date(2026, 11, 8, 1, 30, 0, 0, time.UTC)
	nextRotation := time.Date(2026, 11, 9, 2, 15, 0, 0, time.UTC)
	var buf bytes.Buffer
	logCycleSummary(slog.New(slog.NewJSONHandler(&buf, nil)), cycleSummary{
		CycleNum: 3, StartTime: time.Now(), NextSync: nextSync, Location: denver,
		TVs: []tvSyncResult{{IP: "192.0.2.10", Status: "ok", Rotating: true, ArtworkWaiting: 2, NextRotation: nextRotation}},
	})
	entry := decodeStructuredLog(t, buf.Bytes())
	message, _ := entry["msg"].(string)
	if !strings.Contains(message, "Next:   18:30:00") {
		t.Fatalf("summary = %q, want the next sync in LOCATION_TIMEZONE", message)
	}
	if !strings.Contains(message, "next Nov 8 19:15") {
		t.Fatalf("summary = %q, want the next rotation in LOCATION_TIMEZONE", message)
	}
	if !strings.Contains(message, time.Now().In(denver).Format("2006-01-02 15:04")) {
		t.Fatalf("summary = %q, want the header dated in LOCATION_TIMEZONE", message)
	}
	nextSyncAt, _ := entry["next_sync_at"].(string)
	logged, err := time.Parse(time.RFC3339Nano, nextSyncAt)
	if err != nil || !logged.Equal(nextSync) {
		t.Fatalf("next_sync_at = %v (%v), want %v", entry["next_sync_at"], err, nextSync)
	}
}
