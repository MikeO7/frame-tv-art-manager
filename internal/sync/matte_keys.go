package sync

import (
	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// resolveMatteKeys preserves matte overrides across engine-owned filename
// changes. Current filenames remain accepted, while an item's stable artwork
// key is translated to the current filename required by reconciliation.
func resolveMatteKeys(snapshot collectionpkg.Snapshot, mattes *config.MatteConfig) *config.MatteConfig {
	if mattes == nil {
		return nil
	}
	currentNames := make(map[string]struct{}, len(snapshot.Items))
	keyToName := make(map[string]string, len(snapshot.Items))
	for _, item := range snapshot.Items {
		currentNames[item.Name] = struct{}{}
		keyToName[item.Key] = item.Name
	}
	resolved := &config.MatteConfig{
		DefaultMatte: mattes.DefaultMatte,
		Overrides:    make(map[string]string, len(mattes.Overrides)),
	}
	for key, matte := range mattes.Overrides {
		if _, current := currentNames[key]; current {
			continue
		}
		name := key
		if mapped, exists := keyToName[key]; exists {
			name = mapped
		}
		resolved.Overrides[name] = matte
	}
	// An explicit current filename wins if the file also has a stable-key
	// override. This second pass makes the precedence independent of map order.
	for name, matte := range mattes.Overrides {
		if _, current := currentNames[name]; current {
			resolved.Overrides[name] = matte
		}
	}
	return resolved
}
