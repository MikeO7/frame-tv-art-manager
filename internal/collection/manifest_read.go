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
	value, exists, err := readManifest(ctx, root)
	if err != nil || !exists {
		return map[string]Item{}, err
	}
	return validateManifestItems(value)
}

func readManifest(ctx context.Context, root string) (manifest, bool, error) {
	path := filepath.Join(root, controlDirectory, manifestName)
	data, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{
		MaxBytes: maxCollectionControlBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return manifest{}, false, nil
	}
	if err != nil {
		return manifest{}, false, fmt.Errorf("read collection manifest: %w", err)
	}
	var value manifest
	if err := json.Unmarshal(data, &value); err != nil {
		return manifest{}, false, fmt.Errorf("decode collection manifest: %w", err)
	}
	if _, err := validateManifestItems(value); err != nil {
		return manifest{}, false, err
	}
	return value, true, nil
}
