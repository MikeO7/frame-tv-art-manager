package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
)

// ManagedEngine owns the authoritative artwork collection used by both Sync
// Cycles and operator imports. Keeping construction here prevents the store and
// the engine from being configured with different roots or collection limits.
type ManagedEngine struct {
	engine     *convergenceEngine
	collection ArtworkCollection
}

// NewManagedEngine constructs the complete synchronization boundary from one
// canonical configuration.
func NewManagedEngine(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	healthStatus *health.Status,
) (*ManagedEngine, error) {
	if cfg == nil {
		return nil, errors.New("managed sync engine configuration is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	store, err := collectionpkg.New(collectionpkg.Config{
		Root:                        cfg.ArtworkDir,
		MaxItems:                    cfg.MaxArtworkImages,
		MaxImportBytes:              int64(cfg.MaxDownloadSizeMB) << 20,
		PerceptualDuplicates:        cfg.PerceptualDuplicates,
		PerceptualDuplicateDistance: cfg.PerceptualDuplicateDistance,
	})
	if err != nil {
		return nil, fmt.Errorf("construct authoritative collection: %w", err)
	}
	artwork, err := newArtworkCollection(cfg, logger, healthStatus, store)
	if err != nil {
		return nil, fmt.Errorf("construct artwork collection preparation: %w", err)
	}
	engine, err := newConvergenceEngine(ctx, cfg, logger, healthStatus, artwork)
	if err != nil {
		return nil, fmt.Errorf("construct cached reconciliation engine: %w", err)
	}
	return &ManagedEngine{
		engine:     engine,
		collection: artwork,
	}, nil
}

// Prepare returns a verified snapshot while sharing mutation authority with
// cycle preparation and imports.
func (engine *ManagedEngine) Prepare(
	ctx context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	return engine.collection.Prepare(ctx, request)
}

// Import transactionally publishes artwork through the engine's collection.
func (engine *ManagedEngine) Import(
	ctx context.Context,
	request collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	return engine.collection.Import(ctx, request)
}

// RunLoop executes Sync Cycles until ctx is canceled.
func (engine *ManagedEngine) RunLoop(ctx context.Context) error {
	return engine.engine.RunLoop(ctx)
}

// Close releases every cached per-TV adapter. It is safe to call repeatedly.
func (engine *ManagedEngine) Close(ctx context.Context) error {
	return engine.engine.Close(ctx)
}
