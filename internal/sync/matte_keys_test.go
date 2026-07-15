package sync

import (
	"testing"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestResolveMatteKeysTracksStableArtworkKeyAcrossRename(t *testing.T) {
	t.Parallel()
	snapshot := collectionpkg.Snapshot{Items: []collectionpkg.Item{
		{Name: "vacation--0123456789abcdef.jpg", Key: "vacation.jpg"},
	}}
	resolved := resolveMatteKeys(snapshot, &config.MatteConfig{
		DefaultMatte: "none",
		Overrides:    map[string]string{"vacation.jpg": "shadowbox_warm"},
	})
	if resolved.DefaultMatte != "none" ||
		resolved.Overrides["vacation--0123456789abcdef.jpg"] != "shadowbox_warm" {
		t.Fatalf("resolved matte config = %+v", resolved)
	}
}
