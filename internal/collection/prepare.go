package collection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
)

func (s *store) prepare(ctx context.Context, request PrepareRequest) (Snapshot, error) {
	if err := s.recover(ctx, request.DryRun); err != nil {
		return Snapshot{}, err
	}
	current, manifestExists, err := readManifest(ctx, s.root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read committed manifest: %w", err)
	}
	items, warnings, err := scanPrepare(ctx, s.root, s.limits(), request.Origins)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inventory collection: %w", err)
	}
	changes := inventoryChanges(current, items)
	projected := buildSnapshot(s.root, items, changes, warnings, request.DryRun)
	if err := s.validatePreparedSnapshot(projected); err != nil {
		return Snapshot{}, err
	}
	if request.DryRun {
		return projected, nil
	}
	if err := ensureLayout(s.root); err != nil {
		return Snapshot{}, fmt.Errorf("prepare collection layout: %w", err)
	}
	next := newManifest(items)
	if manifestExists && manifestsEqual(current, next) {
		return projected, nil
	}
	return s.commitPrepared(ctx, items, next, changes)
}

func (s *store) validatePreparedSnapshot(snapshot Snapshot) error {
	if s.maxItems > 0 && len(snapshot.Items) > s.maxItems {
		return fmt.Errorf("collection item limit %d exceeded by prepared inventory of %d items", s.maxItems, len(snapshot.Items))
	}
	if err := ValidateSnapshot(s.root, snapshot); err != nil {
		return fmt.Errorf("validate prepared collection snapshot: %w", err)
	}
	return nil
}

func (s *store) commitPrepared(ctx context.Context, items []Item, next manifest, changes []Change) (Snapshot, error) {
	if err := commitManifest(ctx, s.root, next); err != nil {
		return Snapshot{}, fmt.Errorf("commit collection inventory: %w", err)
	}
	committed, committedWarnings, err := scanPrepare(ctx, s.root, s.limits(), nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify committed collection: %w", err)
	}
	if err := verifyExpected(items, committed); err != nil {
		return Snapshot{}, fmt.Errorf("verify committed manifest: %w", err)
	}
	verified := buildSnapshot(s.root, committed, changes, committedWarnings, false)
	if err := s.validatePreparedSnapshot(verified); err != nil {
		return Snapshot{}, fmt.Errorf("verify committed collection snapshot: %w", err)
	}
	return verified, nil
}

// scanPrepare inventories the collection like scan, but excludes unreadable
// artwork with a warning instead of failing, and applies origin overrides.
func scanPrepare(
	ctx context.Context,
	root string,
	limits inventoryLimits,
	overrides map[string]Origin,
) ([]Item, []string, error) {
	records, err := readManifestRecords(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	entries, exists, err := readRoot(root)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return []Item{}, []string{}, nil
	}
	items := make([]Item, 0, len(entries))
	warnings := make([]string, 0)
	for _, entry := range entries {
		item, warning, err := scanPrepareEntry(ctx, root, entry, limits, records[entry.Name()])
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if item.Name != "" {
			item, err := prepareItemOrigin(withRecordedMetadata(item, records), overrides)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, item)
		}
	}
	sortItems(items)
	sort.Strings(warnings)
	return items, warnings, nil
}

func prepareItemOrigin(item Item, overrides map[string]Origin) (Item, error) {
	origin, overridden := overrides[item.Name]
	if !overridden {
		return item, nil
	}
	item.Origin = origin
	if origin.Class == OriginSource {
		item.SourceKeys = appendSourceKey(item.SourceKeys, origin.Key)
	}
	if err := validateSnapshotOrigin(item); err != nil {
		return Item{}, fmt.Errorf("override artwork %s origin: %w", item.Name, err)
	}
	return item, nil
}

// scanPrepareEntry returns an included item, an exclusion warning, or a hard
// error. Skipped control files and unsupported names return neither.
func scanPrepareEntry(
	ctx context.Context,
	root string,
	entry os.DirEntry,
	limits inventoryLimits,
	record Item,
) (Item, string, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, "", fmt.Errorf("scan collection: %w", err)
	}
	if !isArtworkName(entry.Name()) {
		return Item{}, "", nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return Item{}, "", fmt.Errorf("artwork %s is a symlink", entry.Name())
	}
	item, err := inspectItem(ctx, root, entry, limits, record)
	if err == nil {
		return item, "", nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Item{}, "", err
	}
	return Item{}, fmt.Sprintf("excluded artwork %s: %v", entry.Name(), err), nil
}

func inventoryChanges(current manifest, items []Item) []Change {
	committed := make(map[string]manifestItem, len(current.Items))
	for _, item := range current.Items {
		committed[item.Name] = item
	}
	observed := make(map[string]Item, len(items))
	changes := make([]Change, 0)
	for _, item := range items {
		observed[item.Name] = item
		old, exists := committed[item.Name]
		if !exists || old.Digest != fmt.Sprintf("%x", item.Digest) {
			changes = append(changes, Change{Kind: ChangeAdopted, Name: item.Name})
		}
	}
	for _, item := range current.Items {
		observedItem, exists := observed[item.Name]
		if !exists || item.Digest != fmt.Sprintf("%x", observedItem.Digest) {
			changes = append(changes, Change{Kind: ChangeMissing, Name: item.Name})
		}
	}
	sort.Slice(changes, func(left, right int) bool {
		if changes[left].Name != changes[right].Name {
			return changes[left].Name < changes[right].Name
		}
		return changes[left].Kind < changes[right].Kind
	})
	return changes
}

func manifestsEqual(left, right manifest) bool {
	return left.Version == right.Version && left.Generation == right.Generation &&
		slices.EqualFunc(left.Items, right.Items, func(leftItem, rightItem manifestItem) bool {
			return leftItem.Name == rightItem.Name && leftItem.Digest == rightItem.Digest && leftItem.Type == rightItem.Type &&
				leftItem.Width == rightItem.Width && leftItem.Height == rightItem.Height &&
				leftItem.VisualHash == rightItem.VisualHash &&
				leftItem.OriginKey == rightItem.OriginKey && leftItem.Class == rightItem.Class &&
				leftItem.Key == rightItem.Key && leftItem.TransformKey == rightItem.TransformKey &&
				leftItem.Derivative == rightItem.Derivative && slices.Equal(leftItem.SourceKeys, rightItem.SourceKeys)
		})
}
