package collection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

type manifestOrigin struct {
	digest       [sha256.Size]byte
	key          string
	origin       Origin
	sourceKeys   []string
	transformKey string
	derivative   DerivativeKind
}

func readManifestOrigins(ctx context.Context, root string) (map[string]manifestOrigin, error) {
	value, exists, err := readManifest(ctx, root)
	if err != nil || !exists {
		return map[string]manifestOrigin{}, err
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
	if value.Version != 1 && value.Version != 2 {
		return manifest{}, false, fmt.Errorf("unsupported collection manifest version %d", value.Version)
	}
	if _, err := validateManifestItems(value); err != nil {
		return manifest{}, false, err
	}
	return value, true, nil
}
