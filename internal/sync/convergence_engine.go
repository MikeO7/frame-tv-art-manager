package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
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
	collection artworkCollection
	runtimes   []*convergenceRuntime
	cycleGate  chan struct{}
	// themes is nil when themed collections are not wired; every item is then
	// selected. lastThemes is touched only by RunOnce under cycleGate.
	themes     *themes.Store
	lastThemes *themes.File
	// lastDesired is the collection the last sync cycle sent to the TVs.
	// RunDisplay reuses it between cycles; it is touched only under cycleGate.
	lastDesired *collectionpkg.Snapshot
	// wake coalesces early Sync Cycle requests into one pending cycle.
	wake chan struct{}

	mu       sync.Mutex
	cycleNum int
	// nextTick is when RunLoop's sync timer fires next; it is zero until the
	// timer starts.
	nextTick   time.Time
	displayNum int
	placements map[string][]string
	closeOnce  sync.Once
	closeErr   error
}

func newConvergenceEngine(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	healthStatus *health.Status,
	artwork artworkCollection,
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
		wake: make(chan struct{}, 1), placements: make(map[string][]string),
	}
	// config.LoadEnviron guarantees TVIPs are canonical, unique IP addresses.
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
	// RunOnce records and logs its own failures; only cancellation stops the loop.
	_ = engine.RunOnce(ctx)
	if ctx.Err() == nil {
		interval := engineSyncInterval(engine.cfg)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		started := time.Now()
		engine.setNextTick(started.Add(interval))
		// Pictures change on their own schedule, so a lighter timer changes
		// them between sync cycles.
		var display <-chan time.Time
		if every := displayInterval(engine.cfg); every > 0 {
			displayTicker := time.NewTicker(every)
			defer displayTicker.Stop()
			display = displayTicker.C
		}
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case tick := <-ticker.C:
				engine.setNextTick(tickAfter(started, interval, tick))
				_ = engine.RunOnce(ctx)
			case <-display:
				_ = engine.RunDisplay(ctx)
			case <-engine.wake:
				// A requested cycle is extra: the timer keeps its schedule.
				engine.logger.Info("starting requested sync cycle")
				_ = engine.RunOnce(ctx)
			}
		}
	}
	engine.logger.Info("shutting down sync loop", "reason", ctx.Err())
	return ctx.Err()
}

// tickAfter returns when a timer started at started and firing every interval
// fires next after the tick it delivered at tick. A timer keeps its original
// schedule however late a tick is received, so the answer stays on it.
func tickAfter(started time.Time, interval time.Duration, tick time.Time) time.Time {
	elapsed := max(tick.Sub(started), 0)
	return started.Add((elapsed/interval + 1) * interval)
}

func (engine *convergenceEngine) setNextTick(next time.Time) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.nextTick = next
}

// nextSyncAt reports when the next timed sync cycle starts, as seen at now.
// Before the timer exists (the startup cycle) it starts one interval from
// now; a tick that came due during a long cycle is served at once.
func (engine *convergenceEngine) nextSyncAt(now time.Time) time.Time {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.nextTick.IsZero() {
		return now.Add(engineSyncInterval(engine.cfg))
	}
	if engine.nextTick.Before(now) {
		return now
	}
	return engine.nextTick
}

// summaryLocation is the clock the cycle summary is written in.
func (engine *convergenceEngine) summaryLocation(logger *slog.Logger) *time.Location {
	location, err := time.LoadLocation(engine.cfg.Timezone)
	if err != nil {
		logger.Warn("cycle summary uses UTC; LOCATION_TIMEZONE could not be loaded", "error", err)
		return time.UTC
	}
	return location
}

// displayInterval is how often RunLoop changes the picture on its own timer.
// It is zero when pictures do not change, or when they change exactly as often
// as the collection syncs, because every sync cycle changes a due picture.
func displayInterval(cfg *config.Config) time.Duration {
	interval := cfg.ShowInterval()
	if interval <= 0 || interval == engineSyncInterval(cfg) {
		return 0
	}
	return interval
}

func (engine *convergenceEngine) RunOnce(ctx context.Context) (err error) {
	if err := acquireGate(ctx, engine.cycleGate); err != nil {
		return fmt.Errorf("wait for sync cycle: %w", err)
	}
	defer func() { <-engine.cycleGate }()

	cycleNum := engine.nextCycle()
	cycleLog := engine.logger.With("cycle", cycleNum)
	startedAt := time.Now()
	var tvErrors []error
	failedTVs := 0
	defer func() {
		engine.finalizeCycle(err)
		engine.logFailedCycle(ctx, cycleLog, startedAt, err, failedTVs)
	}()

	cycleLog.Info("starting sync cycle", "tvs", len(engine.runtimes))
	prepared, err := engine.collection.prepareCycle(ctx)
	if err != nil {
		return fmt.Errorf("prepare artwork collection: %w", err)
	}
	desired, err := engine.selectThemedArtwork(ctx, prepared.snapshot, startedAt, cycleLog)
	if err != nil {
		return err
	}

	mattes, err := config.ReadMatteConfig(ctx, engine.cfg.ArtworkDir)
	if err != nil {
		return fmt.Errorf("read artwork matte policy: %w", err)
	}
	mattes = resolveMatteKeys(prepared.snapshot, mattes)
	policy, err := convergencePolicy(engine.cfg, mattes, startedAt, cycleLog)
	if err != nil {
		return fmt.Errorf("build reconciliation policy: %w", err)
	}
	if engine.health != nil {
		engine.health.SetStage("syncing TVs")
	}
	engine.lastDesired = &desired.snapshot
	summaries, tvErrors := engine.syncAll(ctx, fmt.Sprintf("cycle-%d", cycleNum), desired.snapshot, policy)
	failedTVs = countFailedTVs(summaries)
	if len(tvErrors) == 1 && errors.Is(tvErrors[0], ctx.Err()) {
		return ctx.Err()
	}
	logCycleSummary(engine.logger, cycleSummary{
		CycleNum: cycleNum, StartTime: startedAt,
		NextSync: engine.nextSyncAt(time.Now()), Location: engine.summaryLocation(cycleLog),
		TotalLocal: len(prepared.snapshot.Items), FromSources: prepared.downloaded,
		Optimized: prepared.optimized, FailedTVs: failedTVs, TVs: summaries, Warnings: prepared.warnings,
	})
	if len(tvErrors) > 0 {
		return errors.Join(fmt.Errorf("sync TVs: %w", errors.Join(tvErrors...)), desired.degraded)
	}
	return desired.degraded
}

// RunDisplay changes the picture on every TV between sync cycles. It reuses
// the collection the last cycle synced, so it never downloads or prepares
// art; the TV's reconciler decides whether a change is due.
func (engine *convergenceEngine) RunDisplay(ctx context.Context) error {
	if err := acquireGate(ctx, engine.cycleGate); err != nil {
		return fmt.Errorf("wait for sync cycle: %w", err)
	}
	defer func() { <-engine.cycleGate }()
	if engine.lastDesired == nil {
		return nil
	}
	snapshot := *engine.lastDesired
	engine.mu.Lock()
	engine.displayNum++
	label := fmt.Sprintf("display-%d", engine.displayNum)
	engine.mu.Unlock()

	mattes, err := config.ReadMatteConfig(ctx, engine.cfg.ArtworkDir)
	if err != nil {
		engine.logger.Warn("picture change skipped", "cycle", label, "error", err)
		return fmt.Errorf("read artwork matte policy: %w", err)
	}
	logger := engine.logger.With("cycle", label)
	policy, err := convergencePolicy(engine.cfg, resolveMatteKeys(snapshot, mattes), time.Now(), logger)
	if err != nil {
		logger.Warn("picture change skipped", "error", err)
		return fmt.Errorf("build reconciliation policy: %w", err)
	}
	_, tvErrors := engine.syncAll(ctx, label, snapshot, policy)
	if len(tvErrors) > 0 {
		return fmt.Errorf("change pictures: %w", errors.Join(tvErrors...))
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
	label string,
	snapshot collectionpkg.Snapshot,
	policy reconcile.Policy,
) ([]tvSyncResult, []error) {
	type outcome struct {
		summary tvSyncResult
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
			cycleID := label + "-" + tv.address
			summary, runErr := tv.run(ctx, cycleID, snapshot, policy, engine.cfg.DryRun)
			results <- outcome{summary: summary, err: runErr}
		}(runtime)
	}
	summaries := make([]tvSyncResult, 0, launched)
	var errs []error
	for range launched {
		result := <-results
		summaries = append(summaries, result.summary)
		engine.publishTVHealth(result.summary)
		engine.recordPlacements(result.summary)
		logTVOutcome(ctx, engine.logger.With("cycle", label), result.summary, result.err)
		if result.err != nil {
			errs = append(errs, fmt.Errorf("tv %s: %w", result.summary.IP, result.err))
		}
	}
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	slices.SortFunc(summaries, func(left, right tvSyncResult) int {
		return strings.Compare(left.IP, right.IP)
	})
	return summaries, errs
}

func (engine *convergenceEngine) publishTVHealth(summary tvSyncResult) {
	if engine.health == nil {
		return
	}
	lastSeen := ""
	if summary.Model != "" {
		lastSeen = time.Now().Format(time.RFC3339)
	}
	nextRotation := ""
	if !summary.NextRotation.IsZero() {
		nextRotation = summary.NextRotation.UTC().Format(time.RFC3339)
	}
	engine.health.SetTVStatus(summary.IP, health.TVStatus{
		IP: summary.IP, LastSeen: lastSeen, ImageCount: summary.TotalImages,
		ArtMode: summary.ArtMode, Status: summary.Status, LastErrorMessage: summary.ErrorMessage,
		Rotating: summary.Rotating, ArtworkWaiting: summary.ArtworkWaiting, NextRotation: nextRotation,
	})
}

func (engine *convergenceEngine) finalizeCycle(cycleErr error) {
	if engine.health == nil {
		return
	}
	engine.health.RecordSync(cycleErr == nil, cycleErr)
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
