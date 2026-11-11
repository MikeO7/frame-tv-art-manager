package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

// readManifestRecords returns the validated committed items keyed by name.
func readManifestRecords(ctx context.Context, root string) (map[string]Item, error) {
	committed, err := loadManifest(ctx, root)
	if err != nil || !committed.exists {
		return map[string]Item{}, err
	}
	return committed.records, nil
}

func readManifest(ctx context.Context, root string) (manifest, bool, error) {
	committed, err := loadManifest(ctx, root)
	return committed.value, committed.exists, err
}

// committedManifest is the manifest on disk in its stored form and as
// validated items keyed by name.
type committedManifest struct {
	value   manifest
	records map[string]Item
	exists  bool
}

// loadManifest reads the committed manifest and validates it once.
func loadManifest(ctx context.Context, root string) (committedManifest, error) {
	path := filepath.Join(root, controlDirectory, manifestName)
	data, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{
		MaxBytes: maxCollectionControlBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return committedManifest{}, nil
	}
	if err != nil {
		return committedManifest{}, fmt.Errorf("read collection manifest: %w", err)
	}
	var value manifest
	if err := json.Unmarshal(data, &value); err != nil {
		return committedManifest{}, fmt.Errorf("decode collection manifest: %w", err)
	}
	records, err := validateManifestItems(value)
	if err != nil {
		return committedManifest{}, err
	}
	return committedManifest{value: value, records: records, exists: true}, nil
}
