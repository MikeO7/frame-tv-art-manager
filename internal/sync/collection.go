package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

type preparedCollection struct {
	downloaded int
	optimized  int
	warnings   []string
	snapshot   collectionpkg.Snapshot
}

// artworkCollection is the shared mutation authority used by Sync Cycles and
// operator imports. The private cycle method keeps source and transform
// orchestration inside this package while the embedded Store remains the
// narrow interface used by the HTTP upload module.
type artworkCollection interface {
	collectionpkg.Store
	prepareCycle(context.Context) (preparedCollection, error)
}

// maxArrivalPasses bounds how often one cycle re-optimizes for artwork that
// was imported while it worked. Later arrivals wait for the next cycle.
const maxArrivalPasses = 3

// localCollection runs one cycle preparation at a time. The store serializes
// its own operations, so imports and inventory reads are not held behind a
// cycle: a cycle spends minutes downloading and optimizing, and an upload or
// an assistant request must not wait that long. A cycle publishes against the
// snapshot it rendered from, which keeps artwork imported meanwhile.
type localCollection struct {
	cycle chan struct{}
	// arrivals counts imports committed from outside the cycle, so a cycle
	// can tell that artwork it has not rendered joined the collection.
	arrivals atomic.Uint64
	cfg      *config.Config
	logger   *slog.Logger
	health   *health.Status
	loader   sources.SourceLoader
	limits   *transformGate
	store    collectionpkg.Store
}

// newLocalCollection constructs the single collection mutation authority. The
// transform gate is passed in so callers can share it with other image work.
func newLocalCollection(
	cfg *config.Config,
	logger *slog.Logger,
	healthStatus *health.Status,
	store collectionpkg.Store,
	limits *transformGate,
) (*localCollection, error) {
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
		cycle: make(chan struct{}, 1),
		cfg:   cfg, logger: logger, health: healthStatus,
		loader: sources.NewLoader(cfg, logger, store),
		limits: limits, store: store,
	}, nil
}

func (c *localCollection) Prepare(
	ctx context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	request.DryRun = request.DryRun || c.cfg.DryRun
	return c.prepareInventory(ctx, request)
}

func (c *localCollection) Import(
	ctx context.Context,
	request collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	request.DryRun = request.DryRun || c.cfg.DryRun
	snapshot, err := c.store.Import(ctx, request)
	c.noteArrival(err, request.DryRun)
	return snapshot, err
}

func (c *localCollection) ImportBatch(
	ctx context.Context,
	requests []collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	dryRun := c.cfg.DryRun
	for index := range requests {
		requests[index].DryRun = requests[index].DryRun || c.cfg.DryRun
		dryRun = dryRun || requests[index].DryRun
	}
	snapshot, err := c.store.ImportBatch(ctx, requests)
	c.noteArrival(err, dryRun)
	return snapshot, err
}

func (c *localCollection) Apply(
	ctx context.Context,
	request collectionpkg.ApplyRequest,
) (collectionpkg.Snapshot, error) {
	request.DryRun = request.DryRun || c.cfg.DryRun
	snapshot, err := c.store.Apply(ctx, request)
	c.noteArrival(err, request.DryRun)
	return snapshot, err
}

// noteArrival records a committed change made outside cycle preparation.
func (c *localCollection) noteArrival(err error, dryRun bool) {
	if err == nil && !dryRun {
		c.arrivals.Add(1)
	}
}

func (c *localCollection) prepareCycle(ctx context.Context) (preparedCollection, error) {
	if err := c.acquire(ctx); err != nil {
		return preparedCollection{}, err
	}
	defer c.release()
	result, err := c.prepareLocked(ctx)
	if err != nil {
		return result, err
	}
	localImages := len(result.snapshot.Items)
	var warning string
	result.snapshot, warning = limitSnapshot(result.snapshot, c.cfg.MaxArtworkImages)
	if warning != "" {
		c.logger.Warn("artwork folder exceeds image limit; syncing a subset",
			"local_images", localImages, "limit", c.cfg.MaxArtworkImages)
		result.warnings = append(result.warnings, warning)
	}
	return result, nil
}

func (c *localCollection) acquire(ctx context.Context) error {
	if err := acquireGate(ctx, c.cycle); err != nil {
		return fmt.Errorf("wait for artwork collection cycle: %w", err)
	}
	return nil
}

func (c *localCollection) release() {
	<-c.cycle
}

func (c *localCollection) prepareLocked(ctx context.Context) (preparedCollection, error) {
	var result preparedCollection
	if c.cfg.DryRun {
		result.warnings = append(result.warnings, "Dry-run skipped source downloads and local artwork mutations")
		snapshot, err := c.prepareAuthoritativeSnapshot(ctx)
		result.snapshot = snapshot
		return result, err
	}
	// The arrival count is read before each snapshot, so an import that lands
	// after the snapshot is always noticed.
	seen := c.arrivals.Load()
	baseline, err := c.prepareAuthoritativeSnapshot(ctx)
	if err != nil {
		return result, err
	}
	if c.health != nil {
		c.health.SetStage("downloading sources")
	}
	result.downloaded, err = c.loader.Sync(ctx, baseline)
	if err != nil && ctx.Err() != nil {
		return result, fmt.Errorf("synchronize artwork sources: %w", err)
	}
	// A provider or network outage must not stop TVs from syncing the art
	// already committed: the failure is reported and the last-known-good
	// collection is used.
	sourceWarning := ""
	if err != nil {
		c.logger.Warn("artwork sources failed; syncing the existing collection", "error", err)
		sourceWarning = "artwork sources failed: " + err.Error()
		result.warnings = append(result.warnings, sourceWarning)
	}
	if c.health != nil {
		c.health.SetSourceWarning(sourceWarning)
	}
	refresh := result.downloaded > 0
	for pass := 1; ; pass++ {
		if arrived := c.arrivals.Load(); refresh || arrived != seen {
			seen = arrived
			if baseline, err = c.prepareAuthoritativeSnapshot(ctx); err != nil {
				return result, err
			}
		}
		refresh = false
		snapshot, optimized, err := c.optimizeAndPublish(ctx, baseline)
		result.optimized += optimized
		if err != nil {
			return result, err
		}
		result.snapshot = snapshot
		// Artwork imported while this pass rendered was kept but not
		// optimized. Another pass renders it now, so TVs are not sent the raw
		// picture this cycle and its replacement the next.
		if c.arrivals.Load() == seen || pass == maxArrivalPasses {
			return result, nil
		}
	}
}

// optimizeAndPublish renders the derivatives baseline lacks and publishes
// them, returning the resulting snapshot and how many pictures it rendered.
// A collection that needs nothing is returned as it is.
func (c *localCollection) optimizeAndPublish(
	ctx context.Context,
	baseline collectionpkg.Snapshot,
) (collectionpkg.Snapshot, int, error) {
	metadata := newMetadataProjection(baseline)
	stageRequest := optimize.StageRequest{
		Inputs: stageInputs(c.cfg.ArtworkDir, baseline), Config: c.cfg.OptimizeOptions(), Logger: c.logger,
	}
	requiresStage, err := optimize.RequiresStage(ctx, stageRequest)
	if err != nil {
		return collectionpkg.Snapshot{}, 0, fmt.Errorf("preflight artwork optimization: %w", err)
	}
	if !requiresStage {
		return baseline, 0, nil
	}
	if c.health != nil {
		c.health.SetStage("optimizing artwork")
	}
	stage, err := c.optimize(ctx, stageRequest)
	if err != nil {
		return collectionpkg.Snapshot{}, 0, fmt.Errorf("optimize artwork collection: %w", err)
	}
	snapshot, err := c.publishStage(ctx, stage, metadata, baseline)
	if err != nil {
		return collectionpkg.Snapshot{}, stage.Optimized, err
	}
	if err := c.validateAuthoritativeSnapshot(snapshot); err != nil {
		return collectionpkg.Snapshot{}, stage.Optimized, err
	}
	return snapshot, stage.Optimized, nil
}

func (c *localCollection) publishStage(
	ctx context.Context,
	stage *optimize.Stage,
	metadata *metadataProjection,
	baseline collectionpkg.Snapshot,
) (collectionpkg.Snapshot, error) {
	if err := metadata.apply(stage.Derivatives); err != nil {
		return collectionpkg.Snapshot{}, errors.Join(
			fmt.Errorf("project optimized artwork metadata: %w", err), stage.Close(),
		)
	}
	snapshot, applyErr := c.store.Apply(ctx, collectionpkg.ApplyRequest{
		Directory: stage.Directory,
		Metadata:  metadata.items,
		Baseline:  &baseline,
	})
	if err := errors.Join(applyErr, stage.Close()); err != nil {
		return collectionpkg.Snapshot{}, fmt.Errorf("publish optimized artwork collection: %w", err)
	}
	return snapshot, nil
}
