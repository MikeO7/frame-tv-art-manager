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
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

// Engine orchestrates artwork synchronization across all configured TVs.
type Engine struct {
	cfg       *config.Config
	logger    *slog.Logger
	health    *health.Status
	srcLoader sources.SourceLoader
	catalog   *sources.ArtworkCatalog
	reporter  *CycleReporter
	cycleNum  int
	newClient func(ip string, cfg *config.Config, logger *slog.Logger) TVTransport

	mu      sync.Mutex
	clients map[string]TVTransport
}

func defaultNewClient(ip string, cfg *config.Config, logger *slog.Logger) TVTransport {
	return samsung.NewClient(ip, cfg.TVConnectOptions(), logger)
}

// NewEngine creates a sync engine with the given configuration.
func NewEngine(cfg *config.Config, logger *slog.Logger, healthStatus *health.Status) *Engine {
	catalog := sources.NewArtworkCatalog(cfg.ArtworkDir, logger)
	return &Engine{
		cfg:       cfg,
		logger:    logger,
		health:    healthStatus,
		srcLoader: sources.NewLoader(cfg, logger, catalog),
		catalog:   catalog,
		reporter:  NewCycleReporter(cfg, logger),
		newClient: defaultNewClient,
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

	ticker := time.NewTicker(time.Duration(e.cfg.SyncIntervalMin) * time.Minute)
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

// RunOnce performs a single sync cycle for all configured TVs.
//
//nolint:gocognit,nestif,gocyclo,funlen // complexity justified for this path
func (e *Engine) RunOnce(ctx context.Context) (err error) {
	var syncErrors []error
	var cycleWarnings []string
	defer func() {
		if e.health != nil {
			var finalErr error
			if err != nil {
				finalErr = err
			} else if len(syncErrors) > 0 {
				finalErr = errors.Join(syncErrors...)
			}
			e.health.RecordSync(finalErr == nil, finalErr)
			e.health.SetStage("idle")
		}

		runtime.GC()
		debug.FreeOSMemory()
	}()

	e.cycleNum++
	e.reporter.SetCycleNum(e.cycleNum)
	cycleLog := e.logger.With("cycle", e.cycleNum)

	startTime := time.Now()
	cycleLog.Info("starting sync cycle",
		"tvs", len(e.cfg.TVIPs),
	)

	srcDownloaded, cycleWarnings := e.downloadSources(cycleLog)

	optCfg := e.cfg.OptimizeOptions()
	optimized, optErr := e.catalog.Optimize(optCfg, func(oldName, newName string) {
		for _, ip := range e.cfg.TVIPs {
			m, err := LoadMapping(e.cfg.TokenDir, ip)
			if err != nil {
				continue
			}
			if m.Rename(oldName, newName) {
				if err := m.Save(); err != nil {
					e.logger.Warn("failed to save migrated mapping", "tv", ip, "error", err)
				}
			}
		}
	})
	if optErr != nil {
		return optErr
	}

	localFiles, err := e.catalog.SupportedFiles()
	if err != nil {
		return err
	}
	cycleLog.Info("local artwork ready", "total", len(localFiles), "optimized", optimized)

	if e.health != nil {
		e.health.SetStage("syncing TVs")
	}
	tvSummaries := make([]TVSyncResult, 0, len(e.cfg.TVIPs))
	var summariesMu sync.Mutex
	var wg sync.WaitGroup

	matteConfig := LoadMatteConfig(e.cfg.ArtworkDir)

	for _, ip := range e.cfg.TVIPs {
		select {
		case <-ctx.Done():
			e.logger.Info("sync cycle cancelled due to shutdown")
			return ctx.Err()
		default:
		}

		wg.Add(1)
		go func(tvIP string) {
			defer wg.Done()

			summary, err := e.syncTV(ctx, tvIP, localFiles, matteConfig, cycleLog)

			summariesMu.Lock()
			defer summariesMu.Unlock()

			if err != nil {
				e.logger.Error("TV sync failed", "tv", tvIP, "error", err)
				syncErrors = append(syncErrors, fmt.Errorf("tv %s: %w", tvIP, err))
				if e.health != nil {
					e.health.SetTVStatus(tvIP, health.TVStatus{
						IP:     tvIP,
						Status: "unreachable",
					})
				}
				tvSummaries = append(tvSummaries, TVSyncResult{
					IP:           tvIP,
					Status:       "failed",
					ErrorMessage: err.Error(),
				})
			} else {
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
			}
		}(ip)
	}

	wg.Wait()

	e.reporter.PrintSummary(startTime, len(localFiles), srcDownloaded, optimized, tvSummaries, cycleWarnings)

	if len(syncErrors) > 0 {
		return errors.Join(syncErrors...)
	}
	return nil
}

func (e *Engine) syncTV(
	ctx context.Context,
	ip string,
	localFiles map[string]struct{},
	matteConfig *MatteConfig,
	cycleLog *slog.Logger,
) (TVSyncResult, error) {
	client := e.getClient(ip)

	session, err := NewTVSyncSession(ip, e.cfg, matteConfig, cycleLog)
	if err != nil {
		return TVSyncResult{IP: ip, Status: statusError}, err
	}

	return session.Reconcile(ctx, client, localFiles)
}

func (e *Engine) downloadSources(cycleLog *slog.Logger) (int, []string) {
	if e.health != nil {
		e.health.SetStage("downloading sources")
	}
	srcDownloaded, srcErr := e.srcLoader.Sync()
	var cycleWarnings []string
	if srcErr != nil {
		cycleLog.Warn("source download error", "error", srcErr)
		cycleWarnings = append(cycleWarnings, fmt.Sprintf("Source download issue: %v", srcErr))
	}
	return srcDownloaded, cycleWarnings
}
