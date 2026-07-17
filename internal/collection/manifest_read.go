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

type manifestRecord struct {
	item Item
}

func readManifestRecords(ctx context.Context, root string) (map[string]manifestRecord, error) {
	value, exists, err := readManifest(ctx, root)
	if err != nil || !exists {
		return map[string]manifestRecord{}, err
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
	if value.Version != 1 && value.Version != 2 && value.Version != 3 {
		return manifest{}, false, fmt.Errorf("unsupported collection manifest version %d", value.Version)
	}
	if _, err := validateManifestItems(value); err != nil {
		return manifest{}, false, err
	}
	return value, true, nil
}
