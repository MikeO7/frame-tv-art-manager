package sync

import (
	"reflect"
	"strings"
	"testing"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func TestMetadataProjectionPreservesIdentityAndCombinesSourceLineage(t *testing.T) {
	t.Parallel()
	projection := newMetadataProjection(collectionpkg.Snapshot{Items: []collectionpkg.Item{
		{Name: "one.jpg", Key: "one.jpg", Origin: collectionpkg.Origin{Key: "source:one", Class: collectionpkg.OriginSource}, SourceKeys: []string{"source:one"}},
		{Name: "two.jpg", Key: "two.jpg", Origin: collectionpkg.Origin{Key: "source:two", Class: collectionpkg.OriginSource}, SourceKeys: []string{"source:two"}},
		{Name: "operator.jpg", Key: "original.jpg", Origin: collectionpkg.Origin{Key: "operator:operator.jpg", Class: collectionpkg.OriginOperator}},
		{Name: "old-collage.jpg", Key: "collage-key.jpg", Origin: collectionpkg.Origin{Key: "derived:collage-key.jpg", Class: collectionpkg.OriginDerived}, SourceKeys: []string{"source:one", "source:two"}, Derivative: collectionpkg.DerivativeCollage},
	}})
	err := projection.apply([]optimize.Derivative{
		{Name: "optimized.jpg", Inputs: []string{"operator.jpg"}, TransformKey: "transform", Kind: "optimized"},
		{Name: "collage.jpg", Inputs: []string{"two.jpg", "one.jpg"}, TransformKey: "transform", Kind: "collage"},
		{Name: "new-collage.jpg", Inputs: []string{"old-collage.jpg"}, TransformKey: "new-transform", Kind: "collage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := projection.snapshot()
	optimized := items["optimized.jpg"]
	if optimized.Key != "original.jpg" || optimized.Origin.Class != collectionpkg.OriginOperator || optimized.Derivative != collectionpkg.DerivativeOptimized {
		t.Fatalf("optimized metadata = %+v", optimized)
	}
	collage := items["collage.jpg"]
	if collage.Origin.Class != collectionpkg.OriginDerived || collage.Derivative != collectionpkg.DerivativeCollage ||
		!reflect.DeepEqual(collage.SourceKeys, []string{"source:one", "source:two"}) {
		t.Fatalf("collage metadata = %+v", collage)
	}
	reencoded := items["new-collage.jpg"]
	if reencoded.Key != "collage-key.jpg" || reencoded.Origin.Key != "derived:collage-key.jpg" ||
		reencoded.Derivative != collectionpkg.DerivativeCollage {
		t.Fatalf("re-encoded collage metadata = %+v", reencoded)
	}
}

func TestMetadataProjectionRejectsInvalidLineage(t *testing.T) {
	t.Parallel()
	newProjection := func() *metadataProjection {
		return newMetadataProjection(collectionpkg.Snapshot{Items: []collectionpkg.Item{{
			Name: "input.jpg", Key: "input.jpg",
			Origin: collectionpkg.Origin{Key: "operator:input.jpg", Class: collectionpkg.OriginOperator},
		}}})
	}
	for _, test := range []struct {
		name       string
		derivative optimize.Derivative
		want       string
	}{
		{name: "empty output", derivative: optimize.Derivative{Inputs: []string{"input.jpg"}}, want: "incomplete"},
		{name: "empty inputs", derivative: optimize.Derivative{Name: "output.jpg"}, want: "incomplete"},
		{name: "unknown input", derivative: optimize.Derivative{Name: "output.jpg", Inputs: []string{"missing.jpg"}, Kind: "optimized"}, want: "unknown"},
		{name: "unknown kind", derivative: optimize.Derivative{Name: "output.jpg", Inputs: []string{"input.jpg"}, Kind: "mystery"}, want: "unknown kind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := newProjection().apply([]optimize.Derivative{test.derivative})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("apply() error = %v, want %q", err, test.want)
			}
		})
	}
}
