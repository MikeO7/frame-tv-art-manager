package sync

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestEngine_syncAllTVs_CancelledContext(t *testing.T) {
	engine := NewEngine(&config.Config{
		TVIPs:      []string{"1.2.3.4"},
		SyncIntervalMin: 1,
	}, slog.Default(), nil)
	engine.newClient = func(string, *config.Config, *slog.Logger) TVTransport {
		t.Fatal("should not instantiate clients when context is cancelled")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, errs := engine.syncAllTVs(ctx, map[string]struct{}{}, nil, slog.Default())
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("syncAllTVs() errors = %v", errs)
	}
}

func TestEngine_handleSyncError_AppendsSummary(t *testing.T) {
	engine := NewEngine(&config.Config{}, slog.Default(), nil)
	syncErrors := []error{}
	summaries := []TVSyncResult{}

	testErr := errors.New("boom")
	engine.handleSyncError("1.2.3.4", testErr, &syncErrors, &summaries)
	if len(syncErrors) != 1 || len(summaries) != 1 {
		t.Fatalf("expected one error and one summary, got %d errors %d summaries", len(syncErrors), len(summaries))
	}
	if summaries[0].Status != "failed" || !errors.Is(syncErrors[0], testErr) {
		t.Fatalf("unexpected summary/error = %#v / %v", summaries[0], syncErrors[0])
	}
}
