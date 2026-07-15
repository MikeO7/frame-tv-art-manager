package sync

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const messageTVReconciliationFailed = "TV reconciliation failed"

func (engine *convergenceEngine) logFailedCycle(
	ctx context.Context,
	logger *slog.Logger,
	startedAt time.Time,
	err error,
	failedTVs int,
) {
	if err == nil {
		return
	}
	attrs := []slog.Attr{
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
		slog.Int("failed_tvs", failedTVs),
		slog.Any("error", err),
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		logger.LogAttrs(ctx, slog.LevelInfo, "sync cycle canceled", attrs...)
		return
	}
	logger.LogAttrs(ctx, slog.LevelError, "sync cycle failed", attrs...)
}

func logTVOutcome(ctx context.Context, logger *slog.Logger, summary TVSyncResult, err error) {
	attrs := tvOutcomeAttrs(summary)
	if err == nil {
		message := "TV reconciliation completed"
		if summary.Status != "ok" {
			message = "TV reconciliation skipped"
		}
		logger.LogAttrs(ctx, slog.LevelInfo, message, attrs...)
		return
	}

	attrs = append(attrs, slog.Bool("storage_full", summary.StorageFull), slog.Any("error", err))
	var samsungErr *samsung.Error
	if !errors.As(err, &samsungErr) {
		attrs = append(attrs, slog.String("error_kind", reconciliationFailureKind(summary.Status, err)))
		logger.LogAttrs(ctx, slog.LevelError, messageTVReconciliationFailed, attrs...)
		return
	}
	attrs = appendSamsungErrorAttrs(attrs, samsungErr)
	level, message := samsungErrorLogEvent(ctx, samsungErr)
	logger.LogAttrs(ctx, level, message, attrs...)
}

func tvOutcomeAttrs(summary TVSyncResult) []slog.Attr {
	return []slog.Attr{
		slog.String("tv", summary.IP),
		slog.String("model", summary.Model),
		slog.String("status", summary.Status),
		slog.Bool("art_mode", summary.ArtMode),
		slog.Int("uploaded", summary.Uploaded),
		slog.Int("deleted", summary.Deleted),
		slog.Int("total_images", summary.TotalImages),
	}
}

func appendSamsungErrorAttrs(attrs []slog.Attr, err *samsung.Error) []slog.Attr {
	attrs = append(attrs,
		slog.String("error_kind", err.Kind.String()),
		slog.String("operation", err.Operation),
		slog.Bool("retryable", err.Retryable),
		slog.String("outcome", err.Outcome.String()),
	)
	if err.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", err.RequestID))
	}
	if err.Code != 0 {
		attrs = append(attrs, slog.Int("code", err.Code))
	}
	if !err.RetryAt.IsZero() {
		attrs = append(attrs, slog.Time("retry_at", err.RetryAt))
	}
	if err.ConsecutiveFailures > 0 {
		attrs = append(attrs, slog.Int("consecutive_failures", err.ConsecutiveFailures))
	}
	return attrs
}

func samsungErrorLogEvent(ctx context.Context, err *samsung.Error) (slog.Level, string) {
	switch {
	case err.Kind == samsung.ErrorKindBackoff:
		return slog.LevelWarn, "TV reconciliation deferred by backoff"
	case err.Kind == samsung.ErrorKindCanceled && ctx.Err() != nil:
		return slog.LevelInfo, "TV reconciliation canceled"
	default:
		return slog.LevelError, messageTVReconciliationFailed
	}
}

func reconciliationFailureKind(status string, err error) string {
	switch {
	case errors.Is(err, reconcile.ErrRecoveryRequired):
		return "recovery_required"
	case errors.Is(err, reconcile.ErrPersistenceUnknown):
		return "persistence_unknown"
	case errors.Is(err, reconcile.ErrUnsupportedIntent):
		return statusUnsupported
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return statusCanceled
	case status != "":
		return strings.ReplaceAll(status, " ", "_")
	default:
		return "internal"
	}
}

func countFailedTVs(summaries []TVSyncResult) int {
	failed := 0
	for _, summary := range summaries {
		if summary.ErrorMessage != "" {
			failed++
		}
	}
	return failed
}
