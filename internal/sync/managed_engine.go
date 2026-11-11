package sync

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

// managedEngine owns the authoritative artwork collection used by both Sync
// Cycles and operator imports. Keeping construction here prevents the store and
// the engine from being configured with different roots or collection limits.
type managedEngine struct {
	cfg        *config.Config
	logger     *slog.Logger
	engine     *convergenceEngine
	collection artworkCollection
	themes     *themes.Store
	limits     *transformGate
}

// NewManagedEngine constructs the complete synchronization boundary from one
// canonical configuration.
func NewManagedEngine(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	healthStatus *health.Status,
) (*managedEngine, error) {
	if cfg == nil {
		return nil, errors.New("managed sync engine configuration is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	store, err := collectionpkg.New(collectionpkg.Config{
		Root:           cfg.ArtworkDir,
		MaxItems:       cfg.MaxArtworkImages,
		MaxImportBytes: int64(cfg.MaxLocalFileSizeMB) << 20,
		Logger:         logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct authoritative collection: %w", err)
	}
	themeStore, err := themes.NewStore(cfg.ArtworkDir, cfg.DryRun)
	if err != nil {
		return nil, fmt.Errorf("construct themed collections: %w", err)
	}
	limits := newTransformGate()
	artwork, err := newLocalCollection(cfg, logger, healthStatus, store, limits)
	if err != nil {
		return nil, fmt.Errorf("construct artwork collection preparation: %w", err)
	}
	engine, err := newConvergenceEngine(ctx, cfg, logger, healthStatus, artwork)
	if err != nil {
		return nil, fmt.Errorf("construct cached reconciliation engine: %w", err)
	}
	engine.themes = themeStore
	return &managedEngine{
		cfg: cfg, logger: logger, engine: engine, collection: artwork, themes: themeStore, limits: limits,
	}, nil
}

// Themes is the themed collections store whose rules every Sync Cycle applies.
func (engine *managedEngine) Themes() *themes.Store {
	return engine.themes
}

// RequestSync asks for an early Sync Cycle. Repeated requests collapse into
// one pending cycle and never run concurrently with another cycle. It reports
// whether this call queued a new request.
func (engine *managedEngine) RequestSync() bool {
	return engine.engine.requestCycle()
}

// Placements maps each artwork digest to the TVs whose latest observed
// inventory held it.
func (engine *managedEngine) Placements() map[string][]string {
	return engine.engine.placementsByDigest()
}

// RenderPreview renders item in memory as the TV will show it, sharing the
// transform admission limit with collection optimization. It writes nothing.
func (engine *managedEngine) RenderPreview(ctx context.Context, item collectionpkg.Item) (*image.RGBA, error) {
	cfg := engine.cfg.OptimizeOptions()
	input := optimize.PreviewInput{
		Path: item.Path,
		Optimized: item.Derivative != "" && item.TransformKey == optimize.TransformKey(cfg) &&
			item.Width == cfg.MaxWidth && item.Height == cfg.MaxHeight,
	}
	var rendered *image.RGBA
	err := engine.limits.run(ctx, true, func(runCtx context.Context) error {
		var renderErr error
		rendered, renderErr = optimize.RenderPreview(runCtx, input, cfg, engine.logger)
		return renderErr
	})
	if err != nil {
		return nil, fmt.Errorf("render preview of %s: %w", item.Name, err)
	}
	return rendered, nil
}

// Prepare returns a verified snapshot while sharing mutation authority with
// cycle preparation and imports.
func (engine *managedEngine) Prepare(
	ctx context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	return engine.collection.Prepare(ctx, request)
}

// Import transactionally publishes artwork through the engine's collection.
func (engine *managedEngine) Import(
	ctx context.Context,
	request collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	return engine.collection.Import(ctx, request)
}

// RunLoop executes Sync Cycles until ctx is canceled.
func (engine *managedEngine) RunLoop(ctx context.Context) error {
	return engine.engine.RunLoop(ctx)
}

// Close releases every cached per-TV adapter. It is safe to call repeatedly.
func (engine *managedEngine) Close(ctx context.Context) error {
	return engine.engine.Close(ctx)
}
