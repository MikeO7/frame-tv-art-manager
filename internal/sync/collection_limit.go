package sync

import (
	"fmt"
	"sort"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// limitSnapshot returns the view of snapshot that a Sync Cycle reconciles when
// MAX_ARTWORK_IMAGES is smaller than the local collection. The local folder is
// never trimmed: a larger collection is the operator's choice, so the cycle
// syncs a deterministic subset (the first maxItems images by file name) and
// reports the rest. Items beyond the limit behave exactly like images that are
// not in the folder, so TV-side removal still follows REMOVE_UNKNOWN_IMAGES.
func limitSnapshot(snapshot collectionpkg.Snapshot, maxItems int) (collectionpkg.Snapshot, string) {
	if maxItems <= 0 || len(snapshot.Items) <= maxItems {
		return snapshot, ""
	}
	items := append([]collectionpkg.Item(nil), snapshot.Items...)
	sort.SliceStable(items, func(left, right int) bool { return items[left].Name < items[right].Name })
	kept := items[:maxItems:maxItems]
	keptNames := make(map[string]struct{}, len(kept))
	for _, item := range kept {
		keptNames[item.Name] = struct{}{}
	}
	changes := make([]collectionpkg.Change, 0, len(snapshot.Changes))
	for _, change := range snapshot.Changes {
		if _, ok := keptNames[change.Name]; ok || change.Kind == collectionpkg.ChangeMissing {
			changes = append(changes, change)
		}
	}
	limited := collectionpkg.Snapshot{
		Generation: collectionpkg.SnapshotGeneration(kept),
		Items:      kept,
		Changes:    changes,
		Warnings:   append([]string(nil), snapshot.Warnings...),
		DryRun:     snapshot.DryRun,
	}
	warning := fmt.Sprintf(
		"Artwork folder holds %d images, more than MAX_ARTWORK_IMAGES=%d; syncing the first %d by file name (from %s to %s)",
		len(snapshot.Items), maxItems, maxItems, kept[0].Name, kept[len(kept)-1].Name,
	)
	return limited, warning
}
