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
}

func (s *store) prepareInventory(ctx context.Context, overrides map[string]Origin) (preparedInventory, error) {
	current, exists, err := readManifest(ctx, s.root)
	if err != nil {
		return preparedInventory{}, fmt.Errorf("read committed manifest: %w", err)
	}
	limits := inventoryLimits{
		maxBytes: s.maxImportBytes, maxPixels: s.maxPixels,
	}
	items, warnings, err := scanPrepare(ctx, s.root, limits, overrides)
	if err != nil {
		return preparedInventory{}, fmt.Errorf("inventory collection: %w", err)
	}
	return preparedInventory{
		items: items, current: current, manifestExists: exists, warnings: warnings,
	}, nil
}
