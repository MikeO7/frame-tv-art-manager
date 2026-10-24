package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/resources"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

type preparedCollection struct {
	downloaded int
	optimized  int
	warnings   []string
	snapshot   collectionpkg.Snapshot
}

// ArtworkCollection is the shared mutation authority used by Sync Cycles and
// operator imports. The private cycle method keeps source and transform
// orchestration inside this package while the embedded Store remains the
// narrow interface used by the HTTP upload module.
type ArtworkCollection interface {
	collectionpkg.Store
	prepareCycle(context.Context) (preparedCollection, error)
}

type localCollection struct {
	mutation chan struct{}
	gateOnce sync.Once
	cfg      *config.Config
	logger   *slog.Logger
	health   *health.Status
	loader   sources.SourceLoader
	limits   *resources.Controller
	store    collectionpkg.Store
}

// newArtworkCollection constructs the single collection mutation authority.
func newArtworkCollection(
	cfg *config.Config,
	logger *slog.Logger,
	healthStatus *health.Status,
	store collectionpkg.Store,
) (ArtworkCollection, error) {
	if cfg == nil {
		return nil, errors.New("artwork collection configuration is required")
	}
	if store == nil {
		return nil, errors.New("authoritative collection store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &localCollection{
		mutation: make(chan struct{}, 1),
		cfg:      cfg, logger: logger, health: healthStatus,
		loader: sources.NewLoader(cfg, logger, store),
		limits: resources.NewDefaultController(), store: store,
	}, nil
}

func (c *localCollection) Prepare(
	ctx context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	request.DryRun = request.DryRun || c.cfg.DryRun
	if err := c.acquire(ctx); err != nil {
		return collectionpkg.Snapshot{}, err
	}
	defer c.release()
	return c.prepareInventory(ctx, request)
}

func (c *localCollection) Import(
	ctx context.Context,
	request collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	request.DryRun = request.DryRun || c.cfg.DryRun
	if err := c.acquire(ctx); err != nil {
		return collectionpkg.Snapshot{}, err
	}
	defer c.release()
	return c.store.Import(ctx, request)
}

func (c *localCollection) ImportBatch(
	ctx context.Context,
	requests []collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	for index := range requests {
		requests[index].DryRun = requests[index].DryRun || c.cfg.DryRun
	}
	if err := c.acquire(ctx); err != nil {
		return collectionpkg.Snapshot{}, err
	}
	defer c.release()
	return c.store.ImportBatch(ctx, requests)
}

func (c *localCollection) Apply(
	ctx context.Context,
	request collectionpkg.ApplyRequest,
) (collectionpkg.Snapshot, error) {
	request.DryRun = request.DryRun || c.cfg.DryRun
	if err := c.acquire(ctx); err != nil {
		return collectionpkg.Snapshot{}, err
	}
	defer c.release()
	return c.store.Apply(ctx, request)
}

func (c *localCollection) prepareCycle(ctx context.Context) (preparedCollection, error) {
	if err := c.acquire(ctx); err != nil {
		return preparedCollection{}, err
	}
	defer c.release()
	return c.prepareLocked(ctx)
}

func (c *localCollection) acquire(ctx context.Context) error {
	c.gateOnce.Do(func() {
		if c.mutation == nil {
			c.mutation = make(chan struct{}, 1)
		}
	})
	if err := acquireGate(ctx, c.mutation); err != nil {
		return fmt.Errorf("wait for artwork collection mutation: %w", err)
	}
	return nil
}

func (c *localCollection) release() {
	<-c.mutation
}

func (c *localCollection) prepareLocked(ctx context.Context) (preparedCollection, error) {
	var result preparedCollection
	if c.cfg.DryRun {
		result.warnings = append(result.warnings, "Dry-run skipped source downloads and local artwork mutations")
		snapshot, err := c.prepareAuthoritativeSnapshot(ctx)
		result.snapshot = snapshot
		return result, err
	}
	baseline, err := c.prepareAuthoritativeSnapshot(ctx)
	if err != nil {
		return result, err
	}
	if c.health != nil {
		c.health.SetStage("downloading sources")
	}
	result.downloaded, err = c.loader.Sync(ctx, baseline)
	if err != nil {
		return result, fmt.Errorf("synchronize artwork sources: %w", err)
	}
	if result.downloaded > 0 {
		baseline, err = c.prepareAuthoritativeSnapshot(ctx)
		if err != nil {
			return result, err
		}
	}
	metadata := newMetadataProjection(baseline)
	stageRequest := optimize.StageRequest{
		Inputs: stageInputs(baseline), Config: c.cfg.OptimizeOptions(), Logger: c.logger,
	}
	requiresStage, err := optimize.RequiresStage(ctx, stageRequest)
	if err != nil {
		return result, fmt.Errorf("preflight artwork optimization: %w", err)
	}
	if !requiresStage {
		result.snapshot = baseline
		return result, nil
	}

	if c.health != nil {
		c.health.SetStage("optimizing artwork")
	}
	stage, err := c.optimize(ctx, stageRequest)
	if err != nil {
		return result, fmt.Errorf("optimize artwork collection: %w", err)
	}
	result.optimized = stage.Optimized
	snapshot, err := c.publishStage(ctx, stage, metadata)
	if err != nil {
		return result, err
	}
	if err := c.validateAuthoritativeSnapshot(snapshot); err != nil {
		return result, err
	}
	result.snapshot = snapshot
	return result, nil
}

func (c *localCollection) publishStage(
	ctx context.Context,
	stage *optimize.Stage,
	metadata *metadataProjection,
) (collectionpkg.Snapshot, error) {
	if err := metadata.apply(stage.Derivatives); err != nil {
		return collectionpkg.Snapshot{}, errors.Join(
			fmt.Errorf("project optimized artwork metadata: %w", err), stage.Close(),
		)
	}
	snapshot, applyErr := c.store.Apply(ctx, collectionpkg.ApplyRequest{
		Directory: stage.Directory,
		Metadata:  metadata.items,
	})
	if err := errors.Join(applyErr, stage.Close()); err != nil {
		return collectionpkg.Snapshot{}, fmt.Errorf("publish optimized artwork collection: %w", err)
	}
	return snapshot, nil
}
