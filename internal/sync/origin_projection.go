package sync

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

type metadataProjection struct {
	items map[string]collectionpkg.ItemMetadata
	// originals resolves every digest a derivative may be rendered from: an
	// item's kept originals and each item's own current bytes.
	originals map[[sha256.Size]byte]collectionpkg.Original
}

func newMetadataProjection(snapshot collectionpkg.Snapshot) *metadataProjection {
	items := make(map[string]collectionpkg.ItemMetadata, len(snapshot.Items))
	originals := make(map[[sha256.Size]byte]collectionpkg.Original)
	for _, item := range snapshot.Items {
		items[item.Name] = collectionpkg.ItemMetadata{
			Key: item.Key, Origin: item.Origin, SourceKeys: append([]string(nil), item.SourceKeys...),
			TransformKey: item.TransformKey, Derivative: item.Derivative,
			Originals: append([]collectionpkg.Original(nil), item.Originals...),
		}
		originals[item.Digest] = collectionpkg.Original{Digest: item.Digest, Type: item.Type}
		for _, original := range item.Originals {
			originals[original.Digest] = original
		}
	}
	return &metadataProjection{items: items, originals: originals}
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
		originals, err := projection.resolveOriginals(derivative)
		if err != nil {
			return err
		}
		output.Originals = originals
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

// resolveOriginals maps the digests a derivative was rendered from to kept
// originals. An unknown digest means the stage reported lineage the
// collection cannot keep, so publication is refused.
func (projection *metadataProjection) resolveOriginals(derivative optimize.Derivative) ([]collectionpkg.Original, error) {
	originals := make([]collectionpkg.Original, 0, len(derivative.Originals))
	for _, digest := range derivative.Originals {
		original, known := projection.originals[digest]
		if !known {
			return nil, fmt.Errorf("optimized derivative %q original %x is unknown", derivative.Name, digest)
		}
		originals = append(originals, original)
	}
	return originals, nil
}

func unionSourceKeys(items []collectionpkg.ItemMetadata) []string {
	seen := make(map[string]struct{})
	for _, item := range items {
		for _, key := range item.SourceKeys {
			seen[key] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}
