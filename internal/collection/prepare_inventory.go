package collection

import (
	"context"
	"fmt"
)

type preparedInventory struct {
	items          []Item
	current        manifest
	manifestExists bool
	warnings       []string
	advisories     []string
}

func (s *store) prepareInventory(ctx context.Context, overrides map[string]Origin) (preparedInventory, error) {
	current, exists, err := readManifest(ctx, s.root)
	if err != nil {
		return preparedInventory{}, fmt.Errorf("read committed manifest: %w", err)
	}
	limits := inventoryLimits{
		maxBytes: s.maxImportBytes, maxPixels: s.maxPixels, computeVisualHash: s.perceptualDuplicates,
	}
	items, warnings, err := scanPrepare(ctx, s.root, limits, overrides)
	if err != nil {
		return preparedInventory{}, fmt.Errorf("inventory collection: %w", err)
	}
	advisories, err := s.perceptualAdvisories(ctx, items)
	if err != nil {
		return preparedInventory{}, fmt.Errorf("detect perceptual duplicates: %w", err)
	}
	return preparedInventory{
		items: items, current: current, manifestExists: exists, warnings: warnings, advisories: advisories,
	}, nil
}
