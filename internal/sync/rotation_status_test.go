package sync

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestRotationStatusReachesSummaryLogsAndHealth(t *testing.T) {
	next := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)
	summary := convergenceSummary("192.0.2.10", reconcile.Result{
		Status: reconcile.StatusComplete,
		Observation: samsung.Observation{
			TV:        samsung.TVIdentity{Address: "192.0.2.10", Model: "Frame", Known: true},
			ArtMode:   samsung.ArtModeOn,
			Inventory: samsung.Inventory{Known: true, ContentIDs: []string{"one", "two"}},
		},
		Rotation: reconcile.RotationStatus{Active: true, Waiting: 40, NextAt: next},
	})
	if summary.Status != "ok" || !summary.Rotating || summary.ArtworkWaiting != 40 || !summary.NextRotation.Equal(next) {
		t.Fatalf("convergenceSummary() = %+v, want rotating status", summary)
	}

	var output bytes.Buffer
	logTVOutcome(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)), summary, nil)
	entry := decodeStructuredLog(t, output.Bytes())
	if entry["rotating"] != true || entry["artwork_waiting"] != float64(40) || entry["next_rotation"] == nil {
		t.Fatalf("outcome log = %#v, want rotation fields", entry)
	}

	var rendered bytes.Buffer
	LogCycleSummary(slog.New(slog.NewTextHandler(&rendered, nil)), CycleSummary{
		CycleNum: 1, StartTime: time.Now(), SyncIntervalMin: 30, TVs: []TVSyncResult{summary},
	})
	if !strings.Contains(rendered.String(), "Rotation:   40 waiting") {
		t.Fatalf("cycle summary = %q, want rotation line", rendered.String())
	}
	if lines := tvSummaryLines(TVSyncResult{Status: "ok", Rotating: true, ArtworkWaiting: 3}); lines[4] != "    Rotation:   3 waiting" {
		t.Fatalf("tvSummaryLines() = %q, want rotation line without next time", lines)
	}

	status := health.NewStatus()
	engine := &convergenceEngine{health: status}
	engine.publishTVHealth(summary)
	tv := status.TVStatuses["192.0.2.10"]
	if !tv.Rotating || tv.ArtworkWaiting != 40 || tv.NextRotation != "2026-11-07T12:00:00Z" {
		t.Fatalf("health TV status = %+v, want rotation details", tv)
	}
}
