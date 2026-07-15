package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestTVFailureLogCarriesCompleteSamsungDiagnostics(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil)).With("cycle", 7)
	retryAt := time.Date(2026, time.July, 15, 12, 30, 0, 0, time.UTC)
	cause := errors.New("connection refused by television")
	logTVOutcome(context.Background(), logger, TVSyncResult{
		IP: "192.0.2.10", Model: "Frame", Status: statusBackoff, TotalImages: 4,
	}, &samsung.Error{
		Kind: samsung.ErrorKindBackoff, Operation: "observe", RequestID: "request-17", Code: 503,
		Retryable: true, Outcome: samsung.OutcomeNotAttempted, RetryAt: retryAt,
		ConsecutiveFailures: 3, Cause: cause,
	})

	entry := decodeStructuredLog(t, output.Bytes())
	want := map[string]any{
		"level": "WARN", "msg": "TV reconciliation deferred by backoff", "cycle": float64(7),
		"tv": "192.0.2.10", "model": "Frame", "status": statusBackoff,
		"total_images": float64(4), "error_kind": "backoff", "operation": "observe",
		"retryable": true, "outcome": "not_attempted", "request_id": "request-17",
		"code": float64(503), "retry_at": retryAt.Format(time.RFC3339),
		"consecutive_failures": float64(3),
	}
	for key, expected := range want {
		if entry[key] != expected {
			t.Errorf("TV log %s = %#v, want %#v; entry=%#v", key, entry[key], expected, entry)
		}
	}
	if got, ok := entry["error"].(string); !ok || !strings.Contains(got, cause.Error()) {
		t.Fatalf("TV log error = %#v, want complete cause", entry["error"])
	}
}

func TestTVOutcomeLogClassifiesReconciliationAndGracefulStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctx     context.Context
		summary TVSyncResult
		err     error
		level   string
		message string
		kind    string
	}{
		{name: "complete", ctx: context.Background(), summary: TVSyncResult{Status: "ok"}, level: "INFO", message: "TV reconciliation completed"},
		{name: "known skip", ctx: context.Background(), summary: TVSyncResult{Status: statusSkippedNotArtMode}, level: "INFO", message: "TV reconciliation skipped"},
		{name: "recovery", ctx: context.Background(), summary: TVSyncResult{Status: statusRecoveryRequired}, err: reconcile.ErrRecoveryRequired, level: "ERROR", message: messageTVReconciliationFailed, kind: "recovery_required"},
		{name: "persistence", ctx: context.Background(), summary: TVSyncResult{Status: statusPersistenceUnknown}, err: reconcile.ErrPersistenceUnknown, level: "ERROR", message: messageTVReconciliationFailed, kind: "persistence_unknown"},
		{name: "unsupported", ctx: context.Background(), summary: TVSyncResult{Status: statusUnsupported}, err: reconcile.ErrUnsupportedIntent, level: "ERROR", message: messageTVReconciliationFailed, kind: statusUnsupported},
		{name: "generic", ctx: context.Background(), summary: TVSyncResult{Status: "protocol error"}, err: errors.New("bad response"), level: "ERROR", message: messageTVReconciliationFailed, kind: "protocol_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logTVOutcome(test.ctx, slog.New(slog.NewJSONHandler(&output, nil)), test.summary, test.err)
			entry := decodeStructuredLog(t, output.Bytes())
			if entry["level"] != test.level || entry["msg"] != test.message {
				t.Fatalf("outcome log = %#v, want level=%s message=%q", entry, test.level, test.message)
			}
			if test.kind != "" && entry["error_kind"] != test.kind {
				t.Fatalf("outcome error_kind = %#v, want %q; entry=%#v", entry["error_kind"], test.kind, entry)
			}
		})
	}
}

func TestTVCancellationLogIsInformational(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	logTVOutcome(ctx, slog.New(slog.NewJSONHandler(&output, nil)), TVSyncResult{Status: statusCanceled}, &samsung.Error{
		Kind: samsung.ErrorKindCanceled, Operation: "observe", Outcome: samsung.OutcomeNotAttempted,
		Cause: context.Canceled,
	})
	entry := decodeStructuredLog(t, output.Bytes())
	if entry["level"] != "INFO" || entry["msg"] != "TV reconciliation canceled" || entry["error_kind"] != "canceled" {
		t.Fatalf("cancellation log = %#v", entry)
	}
}

func decodeStructuredLog(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("decode structured log: %v; output=%q", err, data)
	}
	return entry
}
