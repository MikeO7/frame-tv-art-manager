package sync

import (
	"fmt"
	"sort"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

type metadataProjection struct {
	items map[string]collectionpkg.ItemMetadata
}

func newMetadataProjection(snapshot collectionpkg.Snapshot) *metadataProjection {
	items := make(map[string]collectionpkg.ItemMetadata, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items[item.Name] = collectionpkg.ItemMetadata{
			Key: item.Key, Origin: item.Origin, SourceKeys: append([]string(nil), item.SourceKeys...),
			TransformKey: item.TransformKey, Derivative: item.Derivative,
		}
	}
	return &metadataProjection{items: items}
}

func (projection *metadataProjection) apply(derivatives []optimize.Derivative) error {
	for _, derivative := range derivatives {
		if derivative.Name == "" || len(derivative.Inputs) == 0 {
			return fmt.Errorf("optimized derivative has incomplete lineage")
		}
		inputs := make([]collectionpkg.ItemMetadata, 0, len(derivative.Inputs))
		for _, name := range derivative.Inputs {
			metadata, exists := projection.items[name]
			if !exists {
				return fmt.Errorf("optimized derivative input %q is unknown", name)
			}
			inputs = append(inputs, metadata)
			delete(projection.items, name)
		}
		output := inputs[0]
		output.TransformKey = derivative.TransformKey
		switch derivative.Kind {
		case optimize.DerivativeOptimized:
			output.Derivative = collectionpkg.DerivativeOptimized
		case optimize.DerivativeCollage:
			// A new multi-input collage gets a new logical identity. Re-encoding
			// an existing collage preserves its key so matte overrides and other
			// operator references survive engine-owned filename changes.
			if len(inputs) > 1 {
				output.Key = derivative.Name
				output.Origin = collectionpkg.Origin{
					Key: "derived:" + derivative.Name, Class: collectionpkg.OriginDerived,
				}
			}
			output.SourceKeys = unionSourceKeys(inputs)
			output.Derivative = collectionpkg.DerivativeCollage
		default:
			return fmt.Errorf("optimized derivative %q has unknown kind %q", derivative.Name, derivative.Kind)
		}
		projection.items[derivative.Name] = output
	}
	return nil
}

func unionSourceKeys(items []collectionpkg.ItemMetadata) []string {
	seen := make(map[string]struct{})
	for _, item := range items {
		for _, key := range item.SourceKeys {
			seen[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (projection *metadataProjection) snapshot() map[string]collectionpkg.ItemMetadata {
	result := make(map[string]collectionpkg.ItemMetadata, len(projection.items))
	for name, metadata := range projection.items {
		metadata.SourceKeys = append([]string(nil), metadata.SourceKeys...)
		result[name] = metadata
	}
	return result
}
