package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// Engine orchestrates artwork synchronization across all configured TVs.
type Engine struct {
	cfg        *config.Config
	logger     *slog.Logger
	health     *health.Status
	collection *localCollection
	cycleNum   int
	newClient  func(ip string, cfg *config.Config, logger *slog.Logger) TVTransport

	mu      sync.Mutex
	clients map[string]TVTransport
}

var engineSyncInterval = func(cfg *config.Config) time.Duration { //nolint:gochecknoglobals // fault-injection seam for loop timing tests
	return time.Duration(cfg.SyncIntervalMin) * time.Minute
}

func defaultNewClient(ip string, cfg *config.Config, logger *slog.Logger) TVTransport {
	return samsung.NewClient(ip, cfg.TVConnectOptions(), logger)
}

// NewEngine creates a sync engine wired with the artwork catalog, source
// loader, and health tracker derived from cfg. The returned Engine is ready
// to run synchronization via RunOnce or RunLoop.
func NewEngine(cfg *config.Config, logger *slog.Logger, healthStatus *health.Status) *Engine {
	return &Engine{
		cfg: cfg, logger: logger, health: healthStatus,
		collection: newLocalCollection(cfg, logger, healthStatus), newClient: defaultNewClient,
	}
}

func (e *Engine) getClient(ip string) TVTransport {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.clients == nil {
		e.clients = make(map[string]TVTransport)
	}

	if client, ok := e.clients[ip]; ok {
		return client
	}

	client := e.newClient(ip, e.cfg, e.logger)
	e.clients[ip] = client
	return client
}

// RunLoop executes RunOnce on a repeating interval until the context is cancelled.
func (e *Engine) RunLoop(ctx context.Context) error {
	e.logger.Info("starting sync loop",
		"tvs", len(e.cfg.TVIPs),
		"interval_min", e.cfg.SyncIntervalMin,
		"artwork_dir", e.cfg.ArtworkDir,
	)

	if err := e.RunOnce(ctx); err != nil {
		e.logger.Error("sync cycle failed", "error", err)
	}

	ticker := time.NewTicker(engineSyncInterval(e.cfg))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("shutting down sync loop")
			return ctx.Err()
		case <-ticker.C:
			if err := e.RunOnce(ctx); err != nil {
				e.logger.Error("sync cycle failed", "error", err)
			}
		}
	}
}

// RunOnce performs a single sync cycle: download sources, optimize the local
// catalog, then reconcile every configured TV concurrently. It records the
// cycle outcome to health tracking and returns the joined per-TV errors, if any.
func (e *Engine) RunOnce(ctx context.Context) (err error) {
	var syncErrors []error
	defer func() { e.finalizeCycle(err, syncErrors) }()

	e.cycleNum++
	cycleLog := e.logger.With("cycle", e.cycleNum)

	startTime := time.Now()
	cycleLog.Info("starting sync cycle",
		"tvs", len(e.cfg.TVIPs),
	)

	collection, err := e.collection.prepare(ctx)
	if err != nil {
		return err
	}
	localFiles := collection.files
	cycleWarnings := append([]string(nil), collection.warnings...)
	cycleLog.Info("local artwork ready", "total", len(localFiles), "optimized", collection.optimized)

	if e.health != nil {
		e.health.SetStage("syncing TVs")
	}

	matteConfig := config.LoadMatteConfig(e.cfg.ArtworkDir)

	tvSummaries, tvSyncErrors := e.syncAllTVs(ctx, localFiles, matteConfig, cycleLog)
	if len(tvSyncErrors) == 1 && errors.Is(tvSyncErrors[0], ctx.Err()) {
		return ctx.Err()
	}
	syncErrors = make([]error, 0, len(tvSyncErrors))
	syncErrors = append(syncErrors, tvSyncErrors...)

	LogCycleSummary(e.logger, CycleSummary{
		CycleNum:        e.cycleNum,
		StartTime:       startTime,
		SyncIntervalMin: e.cfg.SyncIntervalMin,
		TotalLocal:      len(localFiles),
		FromSources:     collection.downloaded,
		Optimized:       collection.optimized,
		TVs:             tvSummaries,
		Warnings:        cycleWarnings,
	})

	if len(syncErrors) > 0 {
		return errors.Join(syncErrors...)
	}
	return nil
}

// finalizeCycle records the cycle's health outcome and reclaims memory. It runs
// deferred from RunOnce; cycleErr is the direct return error and syncErrors are
// the accumulated per-TV failures, either of which marks the cycle unhealthy.
func (e *Engine) finalizeCycle(cycleErr error, syncErrors []error) {
	if e.health != nil {
		finalErr := cycleErr
		if finalErr == nil && len(syncErrors) > 0 {
			finalErr = errors.Join(syncErrors...)
		}
		e.health.RecordSync(finalErr == nil, finalErr)
		e.health.SetStage("idle")
	}

	runtime.GC()
	debug.FreeOSMemory()
}

func (e *Engine) syncTV(
	ctx context.Context,
	ip string,
	localFiles map[string]struct{},
	matteConfig *config.MatteConfig,
	cycleLog *slog.Logger,
) (TVSyncResult, error) {
	client := e.getClient(ip)

	reconciler, err := NewTVReconciler(ip, e.cfg, matteConfig, cycleLog)
	if err != nil {
		return TVSyncResult{IP: ip, Status: statusError}, err
	}

	return reconciler.Reconcile(ctx, client, localFiles)
}

// syncAllTVs reconciles all configured TVs concurrently, collecting per-TV
// summaries and errors. It stops launching new work once the context is
// cancelled and appends the cancellation cause to the returned errors.
func (e *Engine) syncAllTVs(
	ctx context.Context,
	localFiles map[string]struct{},
	matteConfig *config.MatteConfig,
	cycleLog *slog.Logger,
) ([]TVSyncResult, []error) {
	var syncErrors []error
	tvSummaries := make([]TVSyncResult, 0, len(e.cfg.TVIPs))
	var summariesMu sync.Mutex
	var wg sync.WaitGroup

	for _, ip := range e.cfg.TVIPs {
		if ctx.Err() != nil {
			e.logger.Info("sync cycle cancelled due to shutdown")
			break
		}

		wg.Add(1)
		go func(tvIP string) {
			defer wg.Done()
			summary, err := e.syncTV(ctx, tvIP, localFiles, matteConfig, cycleLog)

			summariesMu.Lock()
			defer summariesMu.Unlock()

			if err != nil {
				e.handleSyncError(tvIP, err, &syncErrors, &tvSummaries)
				return
			}

			if summary.Status == statusBackoff {
				tvSummaries = append(tvSummaries, summary)
				return
			}

			if e.health != nil {
				e.health.SetTVStatus(tvIP, health.TVStatus{
					IP:         tvIP,
					LastSeen:   time.Now().Format(time.RFC3339),
					ImageCount: summary.TotalImages,
					ArtMode:    summary.ArtMode,
					Status:     "ok",
				})
			}
			tvSummaries = append(tvSummaries, summary)
		}(ip)
	}

	wg.Wait()

	if ctx.Err() != nil {
		syncErrors = append(syncErrors, ctx.Err())
	}

	return tvSummaries, syncErrors
}

func (e *Engine) handleSyncError(tvIP string, err error, syncErrors *[]error, tvSummaries *[]TVSyncResult) {
	e.logger.Error("TV sync failed", "tv", tvIP, "error", err)
	*syncErrors = append(*syncErrors, fmt.Errorf("tv %s: %w", tvIP, err))
	if e.health != nil {
		e.health.SetTVStatus(tvIP, health.TVStatus{IP: tvIP, Status: "unreachable"})
	}
	*tvSummaries = append(*tvSummaries, TVSyncResult{IP: tvIP, Status: statusFailed, ErrorMessage: err.Error()})
}
