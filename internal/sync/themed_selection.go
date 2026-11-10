package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

// themedDesired is the desired artwork for one cycle. degraded is non-nil when
// the cycle ran on the last good selection rules and must be reported failed.
type themedDesired struct {
	snapshot collectionpkg.Snapshot
	degraded error
}

// selectThemedArtwork narrows the verified snapshot to the artwork themed
// collections select for the cycle's calendar day in LOCATION_TIMEZONE.
//
// A collections file that cannot be read never widens or empties the desired
// set on its own: the last selection rules read successfully in this process
// stay in force and the returned degraded error marks the cycle unhealthy.
// Without such rules the cycle fails before any TV work.
func (engine *convergenceEngine) selectThemedArtwork(
	ctx context.Context,
	snapshot collectionpkg.Snapshot,
	now time.Time,
	logger *slog.Logger,
) (themedDesired, error) {
	if engine.themes == nil {
		return themedDesired{snapshot: snapshot}, nil
	}
	var degraded error
	file, err := engine.themes.Load(ctx)
	switch {
	case err == nil:
		engine.lastThemes = &file
	case ctx.Err() != nil:
		return themedDesired{}, fmt.Errorf("read themed collections: %w", ctx.Err())
	case engine.lastThemes != nil:
		file = *engine.lastThemes
		degraded = fmt.Errorf("themed collections file is unreadable; keeping the last good selection rules: %w", err)
		logger.Error("themed collections file is unreadable; keeping the last good selection rules",
			"path", engine.themes.Path(), "error", err)
	default:
		return themedDesired{}, fmt.Errorf("read themed collections before any TV work: %w", err)
	}
	if len(file.Collections) == 0 {
		return themedDesired{snapshot: snapshot, degraded: degraded}, nil
	}
	location, err := time.LoadLocation(engine.cfg.Timezone)
	if err != nil {
		return themedDesired{}, fmt.Errorf("load LOCATION_TIMEZONE for themed collections: %w", err)
	}
	selection := themes.Select(file, snapshot.Items, now.In(location))
	logger.Info("themed collections applied",
		"active", selection.Active, "exclusive", selection.Exclusive,
		"selected", len(selection.Items), "hidden", selection.Hidden,
	)
	filtered := snapshot
	filtered.Items = selection.Items
	filtered.Generation = collectionpkg.SnapshotGeneration(selection.Items)
	filtered.Changes = append([]collectionpkg.Change(nil), snapshot.Changes...)
	filtered.Warnings = append([]string(nil), snapshot.Warnings...)
	if err := collectionpkg.ValidateSnapshot(engine.cfg.ArtworkDir, filtered); err != nil {
		return themedDesired{}, errors.Join(fmt.Errorf("validate themed selection: %w", err), degraded)
	}
	return themedDesired{snapshot: filtered, degraded: degraded}, nil
}

// requestCycle asks RunLoop for an early Sync Cycle. Requests made while one
// is already pending collapse into it, and cycles never overlap because the
// loop runs them one at a time. It reports whether a new request was queued.
func (engine *convergenceEngine) requestCycle() bool {
	select {
	case engine.wake <- struct{}{}:
		return true
	default:
		return false
	}
}

// recordPlacements remembers which Owned Artwork the latest observation of a
// TV confirmed. An unobserved TV keeps its previous placements.
func (engine *convergenceEngine) recordPlacements(summary tvSyncResult) {
	if summary.OnTV == nil {
		return
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.placements == nil {
		engine.placements = make(map[string][]string)
	}
	engine.placements[summary.IP] = slices.Clone(summary.OnTV)
}

// placementsByDigest maps each artwork digest to the TVs last confirmed to
// hold it.
func (engine *convergenceEngine) placementsByDigest() map[string][]string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	result := make(map[string][]string)
	for _, address := range slices.Sorted(maps.Keys(engine.placements)) {
		for _, digest := range engine.placements[address] {
			result[digest] = append(result[digest], address)
		}
	}
	return result
}
