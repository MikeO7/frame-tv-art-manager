package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
)

const maximumConcurrentTVs = 4

var engineSyncInterval = func(cfg *config.Config) time.Duration { //nolint:gochecknoglobals // loop timing test seam
	return time.Duration(cfg.SyncIntervalMin) * time.Minute
}

// convergenceEngine owns the process-lifetime reconciliation runtime for each
// configured TV and the one authoritative artwork collection.
type convergenceEngine struct {
	cfg        *config.Config
	logger     *slog.Logger
	health     *health.Status
	collection ArtworkCollection
	runtimes   []*convergenceRuntime
	cycleGate  chan struct{}

	mu        sync.Mutex
	cycleNum  int
	closeOnce sync.Once
	closeErr  error
}

func newConvergenceEngine(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	healthStatus *health.Status,
	artwork ArtworkCollection,
) (*convergenceEngine, error) {
	if cfg == nil {
		return nil, errors.New("convergence engine configuration is required")
	}
	if artwork == nil {
		return nil, errors.New("convergence engine collection is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	engine := &convergenceEngine{
		cfg: cfg, logger: logger, health: healthStatus, collection: artwork,
		cycleGate: make(chan struct{}, 1), runtimes: make([]*convergenceRuntime, 0, len(cfg.TVIPs)),
	}
	seenAddresses := make(map[string]struct{}, len(cfg.TVIPs))
	for _, address := range cfg.TVIPs {
		parsed := net.ParseIP(strings.TrimSpace(address))
		if parsed == nil {
			return nil, fmt.Errorf("invalid TV address %q", address)
		}
		canonical := parsed.String()
		if _, duplicate := seenAddresses[canonical]; duplicate {
			return nil, fmt.Errorf("duplicate TV address %q", canonical)
		}
		seenAddresses[canonical] = struct{}{}
	}
	for _, address := range cfg.TVIPs {
		runtime, err := newConvergenceRuntime(cfg, address, logger)
		if err != nil {
			closeErr := engine.closeRuntimes(ctx)
			return nil, errors.Join(err, closeErr)
		}
		engine.runtimes = append(engine.runtimes, runtime)
	}
	if len(engine.runtimes) == 0 {
		return nil, errors.New("convergence engine requires at least one TV")
	}
	return engine, nil
}

func (engine *convergenceEngine) RunLoop(ctx context.Context) error {
	engine.logger.Info("starting sync loop",
		"tvs", len(engine.runtimes), "interval_min", engine.cfg.SyncIntervalMin,
		"artwork_dir", engine.cfg.ArtworkDir,
	)
	if err := engine.RunOnce(ctx); err != nil {
		engine.logger.Error("sync cycle failed", "error", err)
	}
	ticker := time.NewTicker(engineSyncInterval(engine.cfg))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			engine.logger.Info("shutting down sync loop")
			return ctx.Err()
		case <-ticker.C:
			if err := engine.RunOnce(ctx); err != nil {
				engine.logger.Error("sync cycle failed", "error", err)
			}
		}
	}
}

func (engine *convergenceEngine) RunOnce(ctx context.Context) (err error) {
	if err := acquireCycle(ctx, engine.cycleGate); err != nil {
		return err
	}
	defer func() { <-engine.cycleGate }()

	cycleNum := engine.nextCycle()
	cycleLog := engine.logger.With("cycle", cycleNum)
	startedAt := time.Now()
	var tvErrors []error
	defer func() { engine.finalizeCycle(err, tvErrors) }()

	cycleLog.Info("starting sync cycle", "tvs", len(engine.runtimes))
	prepared, err := engine.collection.prepareCycle(ctx)
	if err != nil {
		return err
	}
	cycleLog.Info("local artwork ready", "total", len(prepared.snapshot.Items), "optimized", prepared.optimized)

	mattes, err := config.ReadMatteConfig(ctx, engine.cfg.ArtworkDir)
	if err != nil {
		return fmt.Errorf("read artwork matte policy: %w", err)
	}
	policy, err := convergencePolicy(engine.cfg, mattes, startedAt, cycleLog)
	if err != nil {
		return fmt.Errorf("build reconciliation policy: %w", err)
	}
	if engine.health != nil {
		engine.health.SetStage("syncing TVs")
	}
	summaries, tvErrors := engine.syncAll(ctx, cycleNum, prepared.snapshot, policy)
	if len(tvErrors) == 1 && errors.Is(tvErrors[0], ctx.Err()) {
		return ctx.Err()
	}
	LogCycleSummary(engine.logger, CycleSummary{
		CycleNum: cycleNum, StartTime: startedAt, SyncIntervalMin: engine.cfg.SyncIntervalMin,
		TotalLocal: len(prepared.snapshot.Items), FromSources: prepared.downloaded,
		Optimized: prepared.optimized, TVs: summaries, Warnings: prepared.warnings,
	})
	if len(tvErrors) > 0 {
		return errors.Join(tvErrors...)
	}
	return nil
}

func (engine *convergenceEngine) nextCycle() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.cycleNum++
	return engine.cycleNum
}

func (engine *convergenceEngine) syncAll(
	ctx context.Context,
	cycleNum int,
	snapshot collectionpkg.Snapshot,
	policy reconcile.Policy,
) ([]TVSyncResult, []error) {
	type outcome struct {
		summary TVSyncResult
		err     error
	}
	results := make(chan outcome, len(engine.runtimes))
	workers := make(chan struct{}, min(len(engine.runtimes), maximumConcurrentTVs))
	var launched int
	for _, runtime := range engine.runtimes {
		if !acquireTVWorker(ctx, workers) {
			break
		}
		launched++
		go func(tv *convergenceRuntime) {
			defer func() { <-workers }()
			cycleID := fmt.Sprintf("cycle-%d-%s", cycleNum, tv.address)
			summary, runErr := tv.run(ctx, cycleID, snapshot, policy, engine.cfg.DryRun)
			results <- outcome{summary: summary, err: runErr}
		}(runtime)
	}
	summaries := make([]TVSyncResult, 0, launched)
	var errs []error
	for range launched {
		result := <-results
		summaries = append(summaries, result.summary)
		engine.publishTVHealth(result.summary)
		if result.err != nil {
			errs = append(errs, fmt.Errorf("tv %s: %w", result.summary.IP, result.err))
		}
	}
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	slices.SortFunc(summaries, func(left, right TVSyncResult) int {
		return strings.Compare(left.IP, right.IP)
	})
	return summaries, errs
}

func (engine *convergenceEngine) publishTVHealth(summary TVSyncResult) {
	if engine.health == nil {
		return
	}
	lastSeen := ""
	if summary.Model != "" {
		lastSeen = time.Now().Format(time.RFC3339)
	}
	engine.health.SetTVStatus(summary.IP, health.TVStatus{
		IP: summary.IP, LastSeen: lastSeen, ImageCount: summary.TotalImages,
		ArtMode: summary.ArtMode, Status: summary.Status, LastErrorMessage: summary.ErrorMessage,
	})
}

func (engine *convergenceEngine) finalizeCycle(cycleErr error, tvErrors []error) {
	if engine.health == nil {
		return
	}
	finalErr := cycleErr
	if finalErr == nil && len(tvErrors) > 0 {
		finalErr = errors.Join(tvErrors...)
	}
	engine.health.RecordSync(finalErr == nil, finalErr)
	engine.health.SetStage("idle")
}

func (engine *convergenceEngine) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close convergence engine context is required")
	}
	engine.closeOnce.Do(func() {
		engine.closeErr = engine.closeRuntimes(ctx)
	})
	return engine.closeErr
}

func (engine *convergenceEngine) closeRuntimes(ctx context.Context) error {
	results := make(chan error, len(engine.runtimes))
	workers := make(chan struct{}, min(len(engine.runtimes), maximumConcurrentTVs))
	launched := 0
	for _, runtime := range engine.runtimes {
		if !acquireTVWorker(ctx, workers) {
			break
		}
		launched++
		go func(tv *convergenceRuntime) {
			defer func() { <-workers }()
			results <- tv.close(ctx)
		}(runtime)
	}
	errs := make([]error, 0, launched+1)
	for range launched {
		if err := <-results; err != nil {
			errs = append(errs, err)
		}
	}
	if launched != len(engine.runtimes) && ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
}

func acquireCycle(ctx context.Context, gate chan struct{}) error {
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return fmt.Errorf("wait for sync cycle: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for sync cycle: %w", ctx.Err())
	}
}

func acquireTVWorker(ctx context.Context, workers chan<- struct{}) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case workers <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
