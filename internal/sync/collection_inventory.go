package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func (c *localCollection) prepareAuthoritativeSnapshot(
	ctx context.Context,
	origins map[string]collectionpkg.Origin,
) (collectionpkg.Snapshot, error) {
	snapshot, err := c.prepareInventory(ctx, collectionpkg.PrepareRequest{
		Origins: origins,
		DryRun:  c.cfg.DryRun,
	})
	if err != nil {
		return collectionpkg.Snapshot{}, fmt.Errorf("prepare authoritative collection snapshot: %w", err)
	}
	if err := c.validateAuthoritativeSnapshot(snapshot); err != nil {
		return collectionpkg.Snapshot{}, err
	}
	return snapshot, nil
}

func (c *localCollection) prepareInventory(
	ctx context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	startedAt := time.Now()
	c.logger.Info("inventorying local artwork")
	snapshot, err := c.store.Prepare(ctx, request)
	if err != nil {
		c.logger.Error("local artwork inventory failed",
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		return collectionpkg.Snapshot{}, err
	}
	c.logger.Info("local artwork inventory complete",
		"total", len(snapshot.Items),
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
	return snapshot, nil
}

func (c *localCollection) validateAuthoritativeSnapshot(snapshot collectionpkg.Snapshot) error {
	if len(snapshot.Warnings) > 0 {
		return fmt.Errorf(
			"authoritative collection snapshot is incomplete: %s",
			strings.Join(snapshot.Warnings, "; "),
		)
	}
	if err := collectionpkg.ValidateSnapshot(c.cfg.ArtworkDir, snapshot); err != nil {
		return fmt.Errorf("authoritative collection snapshot is invalid: %w", err)
	}
	return nil
}
