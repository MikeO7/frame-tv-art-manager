package sync

import (
	"strings"
	"sync"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

type originProjection struct {
	mu      sync.Mutex
	origins map[string]collectionpkg.Origin
}

func newOriginProjection(snapshot collectionpkg.Snapshot) *originProjection {
	origins := make(map[string]collectionpkg.Origin, len(snapshot.Items))
	for _, item := range snapshot.Items {
		origins[item.Name] = item.Origin
	}
	return &originProjection{origins: origins}
}

func (projection *originProjection) observeRename(oldName, newName string) error {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	origin, exists := projection.origins[oldName]
	delete(projection.origins, oldName)
	if newName == "" {
		return nil
	}
	if !exists || origin.Class != collectionpkg.OriginSource || strings.HasPrefix(newName, "collage_") {
		origin = collectionpkg.Origin{Key: "operator:" + newName, Class: collectionpkg.OriginOperator}
	}
	projection.origins[newName] = origin
	return nil
}

func (projection *originProjection) snapshot() map[string]collectionpkg.Origin {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	result := make(map[string]collectionpkg.Origin, len(projection.origins))
	for name, origin := range projection.origins {
		result[name] = origin
	}
	return result
}
