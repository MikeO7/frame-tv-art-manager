package sync

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	statusUnreachable        = "unreachable"
	statusUnsupported        = "unsupported"
)

func newConvergenceRuntime(
	cfg *config.Config,
	address string,
	logger *slog.Logger,
) (*convergenceRuntime, error) {
	if logger == nil {
		logger = slog.Default()
	}
	adapterConfig, err := samsungConfigForTV(cfg, address)
	if err != nil {
		return nil, err
	}
	service, err := reconcile.New(reconcile.Config{
		StateDirectory: cfg.TokenDir, LegacyMappingDirectory: cfg.TokenDir,
	}, reconcile.Dependencies{Logger: logger.With("tv", address)},
		reconcile.WithMutationPacing(cfg.UploadDelay),
		reconcile.WithMutationAttempts(max(cfg.UploadAttempts, 1)),
	)
	if err != nil {
		return nil, fmt.Errorf("construct reconciler for TV %s: %w", address, err)
	}
	adapter, err := samsung.NewAdapter(adapterConfig, samsung.Dependencies{Logger: logger.With("tv", address)})
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
	summary := convergenceSummary(runtime.address, result, snapshot)
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
	}
	if samsungErr == nil {
		return statusError, false
	}
	switch samsungErr.Kind {
	case samsung.ErrorKindUnauthorized:
		return "authorization required", false
	case samsung.ErrorKindStorageFull:
		return "storage full", true
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

//nolint:gocyclo // explicit result-state projection keeps operator status exhaustive and local
func convergenceSummary(address string, result reconcile.Result, snapshot collectionpkg.Snapshot) TVSyncResult {
	observation := result.Observation
	summary := TVSyncResult{
		IP: address, Model: observation.TV.Model,
		ArtMode:     observation.ArtMode == samsung.ArtModeOn,
		TotalImages: len(observation.Inventory.ContentIDs),
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
	projectFreeSpace(&summary, result, snapshot)
	return summary
}

func projectFreeSpace(summary *TVSyncResult, result reconcile.Result, snapshot collectionpkg.Snapshot) {
	storage := result.Observation.Storage
	inventory := result.Observation.Inventory
	if !storage.Known || storage.TotalBytes <= 0 || !inventory.Known {
		return
	}
	bytesByDigest := make(map[string]int64, len(snapshot.Items))
	for _, item := range snapshot.Items {
		bytesByDigest[hex.EncodeToString(item.Digest[:])] = item.Size
	}
	byContentID := make(map[string]reconcile.Binding, len(result.State.Bindings))
	for _, binding := range result.State.Bindings {
		if _, duplicate := byContentID[binding.ContentID]; duplicate {
			return
		}
		byContentID[binding.ContentID] = binding
	}
	var usedBytes int64
	for _, contentID := range inventory.ContentIDs {
		binding, exists := byContentID[contentID]
		if !exists {
			return
		}
		artworkBytes := binding.ArtworkBytes
		if artworkBytes == 0 {
			artworkBytes = bytesByDigest[binding.Digest]
		}
		if artworkBytes <= 0 || artworkBytes > storage.TotalBytes-usedBytes {
			return
		}
		usedBytes += artworkBytes
	}
	freeBytes := storage.TotalBytes - usedBytes
	summary.StorageKnown = true
	summary.FreeSpaceBytes = freeBytes
	summary.FreeSpacePercent = float64(freeBytes) * 100 / float64(storage.TotalBytes)
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

func samsungConfigForTV(cfg *config.Config, address string) (samsung.Config, error) {
	if cfg == nil {
		return samsung.Config{}, errors.New("samsung runtime configuration is required")
	}
	address = strings.TrimSpace(address)
	parsedAddress := net.ParseIP(address)
	if parsedAddress == nil {
		return samsung.Config{}, fmt.Errorf("samsung TV address %q is invalid", address)
	}
	address = parsedAddress.String()
	mac, err := configuredMAC(cfg)
	if err != nil {
		return samsung.Config{}, err
	}
	backoffBase := time.Duration(cfg.SyncIntervalMin) * time.Minute
	if backoffBase <= 0 {
		return samsung.Config{}, errors.New("sync interval must be positive")
	}
	backoffBase = min(backoffBase, time.Hour)
	safeAddress := strings.NewReplacer(".", "_", ":", "_").Replace(address)
	return samsung.Config{
		Address: address, MAC: mac, ClientName: cfg.ClientName,
		TokenPath: filepath.Join(cfg.TokenDir, "tv_"+safeAddress+".txt"),
		VerifyTLS: cfg.VerifyTLS, QuietGate: cfg.EnableRESTGate,
		ConnectTimeout: cfg.ConnectionTimeout, RequestTimeout: cfg.APITimeout,
		GateTimeout: cfg.GateTimeout, BackoffBase: backoffBase, BackoffMaximum: time.Hour,
	}, nil
}

func configuredMAC(cfg *config.Config) (net.HardwareAddr, error) {
	if strings.TrimSpace(cfg.TVMAC) == "" || len(cfg.TVIPs) != 1 {
		return nil, nil
	}
	mac, err := net.ParseMAC(cfg.TVMAC)
	if err != nil {
		return nil, fmt.Errorf("parse TV MAC: %w", err)
	}
	if len(mac) != 6 {
		return nil, errors.New("TV MAC must contain six bytes")
	}
	return mac, nil
}
