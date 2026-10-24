package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type convergenceRuntime struct {
	address    string
	adapter    samsung.Adapter
	reconciler reconcile.Service
}

const (
	statusCanceled           = "canceled"
	statusNotApplied         = "not applied"
	statusPersistenceUnknown = "persistence unknown"
	statusRecoveryRequired   = "recovery required"
	statusStorageFull        = "storage full"
	statusUnreachable        = "unreachable"
	statusUnsupported        = "unsupported"
)

func newConvergenceRuntime(
	cfg *config.Config,
	address string,
	logger *slog.Logger,
) (*convergenceRuntime, error) {
	service, err := reconcile.New(reconcile.Config{
		StateDirectory: cfg.TokenDir, LegacyMappingDirectory: cfg.TokenDir,
	}, reconcile.Dependencies{Logger: logger.With("tv", address)},
		reconcile.WithMutationPacing(cfg.UploadDelay),
		reconcile.WithMutationAttempts(max(cfg.UploadAttempts, 1)),
	)
	if err != nil {
		return nil, fmt.Errorf("construct reconciler for TV %s: %w", address, err)
	}
	adapter, err := samsung.NewAdapter(samsungConfigForTV(cfg, address), samsung.Dependencies{Logger: logger.With("tv", address)})
	if err != nil {
		return nil, fmt.Errorf("construct Samsung adapter for TV %s: %w", address, err)
	}
	return &convergenceRuntime{address: address, adapter: adapter, reconciler: service}, nil
}

func (runtime *convergenceRuntime) run(
	ctx context.Context,
	cycleID string,
	snapshot collectionpkg.Snapshot,
	policy reconcile.Policy,
	dryRun bool,
) (TVSyncResult, error) {
	result, err := runtime.reconciler.Run(ctx, reconcile.Request{
		CycleID: cycleID, TV: runtime.adapter, Snapshot: snapshot, Policy: policy, DryRun: dryRun,
	})
	summary := convergenceSummary(runtime.address, result)
	var samsungErr *samsung.Error
	if errors.As(err, &samsungErr) && samsungErr.Kind == samsung.ErrorKindBackoff {
		summary.Status = statusBackoff
		summary.ErrorMessage = err.Error()
		return summary, err
	}
	if err != nil {
		summary.Status, summary.StorageFull = convergenceErrorStatus(result.Status, samsungErr)
		summary.ErrorMessage = err.Error()
		return summary, err
	}
	return summary, nil
}

func convergenceErrorStatus(status reconcile.Status, samsungErr *samsung.Error) (string, bool) {
	switch status {
	case reconcile.StatusRecoveryRequired:
		return statusRecoveryRequired, false
	case reconcile.StatusPersistenceUnknown:
		return statusPersistenceUnknown, false
	case reconcile.StatusUnsupported:
		return statusUnsupported, false
	case reconcile.StatusStorageFull:
		return statusStorageFull, true
	}
	if samsungErr == nil {
		return statusError, false
	}
	switch samsungErr.Kind {
	case samsung.ErrorKindUnauthorized:
		return "authorization required", false
	case samsung.ErrorKindStorageFull:
		return statusStorageFull, true
	case samsung.ErrorKindOutcomeUnknown:
		return statusRecoveryRequired, false
	case samsung.ErrorKindUnreachable, samsung.ErrorKindTimeout:
		return statusUnreachable, false
	case samsung.ErrorKindCanceled:
		return statusCanceled, false
	default:
		return statusError, false
	}
}

func (runtime *convergenceRuntime) close(ctx context.Context) error {
	if runtime == nil || runtime.adapter == nil {
		return nil
	}
	if err := runtime.adapter.Close(ctx); err != nil {
		return fmt.Errorf("close Samsung adapter for TV %s: %w", runtime.address, err)
	}
	return nil
}

func convergenceSummary(address string, result reconcile.Result) TVSyncResult {
	observation := result.Observation
	summary := TVSyncResult{
		IP: address, Model: observation.TV.Model,
		ArtMode:     observation.ArtMode == samsung.ArtModeOn,
		TotalImages: len(observation.Inventory.ContentIDs),
		Rotating:    result.Rotation.Active, ArtworkWaiting: result.Rotation.Waiting,
		NextRotation: result.Rotation.NextAt,
	}
	for _, command := range result.AppliedCommands {
		switch command {
		case reconcile.CommandUpload:
			summary.Uploaded++
		case reconcile.CommandDeleteOwned, reconcile.CommandDeleteUnknown:
			summary.Deleted++
		}
	}
	switch result.Status {
	case reconcile.StatusComplete:
		summary.Status = "ok"
	case reconcile.StatusIncompleteDryRun:
		summary.Status = "dry-run"
	case reconcile.StatusKnownSkip:
		summary.Status = convergenceSkipStatus(observation.Disposition)
	case reconcile.StatusUnsupported:
		summary.Status = statusUnsupported
	case reconcile.StatusRecoveryRequired:
		summary.Status = statusRecoveryRequired
	case reconcile.StatusPersistenceUnknown:
		summary.Status = statusPersistenceUnknown
	case reconcile.StatusNotApplied:
		summary.Status = statusNotApplied
	case reconcile.StatusStorageFull:
		summary.Status = statusStorageFull
		summary.StorageFull = true
	default:
		summary.Status = statusError
	}
	if observation.Brightness.Known {
		summary.Brightness = fmt.Sprintf("%d", observation.Brightness.Value)
	}
	if observation.Slideshow.Known {
		setting := observation.Slideshow.Setting
		if setting.Interval == 0 {
			summary.Slideshow = "off"
		} else {
			summary.Slideshow = fmt.Sprintf("%s every %d min", setting.Kind, setting.Interval)
		}
	}
	return summary
}

func convergenceSkipStatus(disposition samsung.Disposition) string {
	switch disposition {
	case samsung.DispositionBlockedBackoff:
		return statusBackoff
	case samsung.DispositionBlockedNotArtMode:
		return statusSkippedNotArtMode
	case samsung.DispositionBlockedPowerOff:
		return "skipped (powered off)"
	case samsung.DispositionBlockedQuietGate:
		return "skipped (quiet gate)"
	default:
		return "skipped (unsafe TV state)"
	}
}

// samsungConfigForTV relies on config.LoadEnviron for canonical addresses, a
// positive sync interval, and a TVMAC that is set only for a single TV.
func samsungConfigForTV(cfg *config.Config, address string) samsung.Config {
	safeAddress := strings.NewReplacer(".", "_", ":", "_").Replace(address)
	return samsung.Config{
		Address: address, MAC: cfg.TVMAC, ClientName: cfg.ClientName,
		TokenPath: filepath.Join(cfg.TokenDir, "tv_"+safeAddress+".txt"),
		VerifyTLS: cfg.VerifyTLS, QuietGate: cfg.EnableRESTGate,
		ConnectTimeout: cfg.ConnectionTimeout, RequestTimeout: cfg.APITimeout,
		GateTimeout: cfg.GateTimeout, BackoffBase: min(time.Duration(cfg.SyncIntervalMin)*time.Minute, time.Hour),
		BackoffMaximum: time.Hour,
	}
}
