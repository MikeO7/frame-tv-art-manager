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

	LogCycleSummary(logger, CycleSummary{
		CycleNum:        7,
		StartTime:       time.Date(2026, 1, 12, 8, 0, 0, 0, time.UTC),
		SyncIntervalMin: 5,
		TotalLocal:      3,
		FromSources:     1,
		Optimized:       2,
		TVs: []TVSyncResult{
			{
				IP: "192.168.0.1", Model: "Mock", Status: "ok", ArtMode: true,
				StorageKnown: true, FreeSpaceBytes: 12_000_000_000, FreeSpacePercent: 75,
			},
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
	if !strings.Contains(out, "Free space: 12.0 GB (75.0%)") {
		t.Fatalf("expected free-space number and percentage, got %q", out)
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
}
