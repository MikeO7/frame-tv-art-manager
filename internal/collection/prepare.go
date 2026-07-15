package collection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func (s *store) prepare(ctx context.Context, request PrepareRequest) (Snapshot, error) {
	if err := s.beginPrepare(ctx, request.DryRun); err != nil {
		return Snapshot{}, err
	}
	inventory, err := s.prepareInventory(ctx, request.Origins)
	if err != nil {
		return Snapshot{}, err
	}
	changes := inventoryChanges(inventory.current, inventory.items)
	projected := buildSnapshotWithNotices(s.root, inventory.items, changes, inventory.warnings, inventory.advisories, request.DryRun)
	if err := s.validatePreparedSnapshot(projected); err != nil {
		return Snapshot{}, err
	}
	if request.DryRun {
		return projected, nil
	}
	return s.commitPrepared(ctx, inventory, changes)
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

func (s *store) beginPrepare(ctx context.Context, dryRun bool) error {
	if err := validateExistingLayout(s.root); err != nil {
		return fmt.Errorf("validate collection layout: %w", err)
	}
	if dryRun {
		return rejectActiveTransaction(ctx, s.root)
	}
	if err := recoverTransaction(ctx, s.root); err != nil {
		return fmt.Errorf("recover collection: %w", err)
	}
	return nil
}

func (s *store) commitPrepared(ctx context.Context, inventory preparedInventory, changes []Change) (Snapshot, error) {
	projected := buildSnapshotWithNotices(s.root, inventory.items, changes, inventory.warnings, inventory.advisories, false)
	if err := s.validatePreparedSnapshot(projected); err != nil {
		return Snapshot{}, err
	}
	if err := ensureLayout(s.root); err != nil {
		return Snapshot{}, fmt.Errorf("prepare collection layout: %w", err)
	}
	next := newManifest(inventory.items)
	manifestChanged := !inventory.manifestExists || !manifestsEqual(inventory.current, next)
	if !manifestChanged {
		return projected, nil
	}
	if err := commitManifest(ctx, s.root, next); err != nil {
		return Snapshot{}, fmt.Errorf("commit collection inventory: %w", err)
	}
	limits := inventoryLimits{
		maxBytes: s.maxImportBytes, maxPixels: s.maxPixels, computeVisualHash: s.perceptualDuplicates,
	}
	committed, committedWarnings, err := scanPrepare(ctx, s.root, limits)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify committed collection: %w", err)
	}
	if err := verifyExpected(inventory.items, committed); err != nil {
		return Snapshot{}, fmt.Errorf("verify committed manifest: %w", err)
	}
	committedAdvisories, err := s.perceptualAdvisories(ctx, committed)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify perceptual duplicate advisories: %w", err)
	}
	verified := buildSnapshotWithNotices(s.root, committed, changes, committedWarnings, committedAdvisories, false)
	if err := s.validatePreparedSnapshot(verified); err != nil {
		return Snapshot{}, fmt.Errorf("verify committed collection snapshot: %w", err)
	}
	return verified, nil
}

func scanPrepare(
	ctx context.Context,
	root string,
	limits inventoryLimits,
	originOverrides ...map[string]Origin,
) ([]Item, []string, error) {
	var overrides map[string]Origin
	if len(originOverrides) > 0 {
		overrides = originOverrides[0]
	}
	origins, err := readManifestOrigins(ctx, root)
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
		result, err := scanPrepareEntry(ctx, root, entry, limits)
		if err != nil {
			return nil, nil, err
		}
		if result.warning != "" {
			warnings = append(warnings, result.warning)
		}
		if result.include {
			item, err := prepareItemOrigin(result.item, origins, overrides)
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

func prepareItemOrigin(
	item Item,
	committed map[string]manifestOrigin,
	overrides map[string]Origin,
) (Item, error) {
	if metadata, ok := committed[item.Name]; ok && metadata.digest == item.Digest {
		item.Key = metadata.key
		item.Origin = metadata.origin
		item.SourceKeys = append([]string(nil), metadata.sourceKeys...)
		item.TransformKey = metadata.transformKey
		item.Derivative = metadata.derivative
	}
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

type scannedPrepareEntry struct {
	item    Item
	include bool
	warning string
}

func scanPrepareEntry(
	ctx context.Context,
	root string,
	entry os.DirEntry,
	limits inventoryLimits,
) (scannedPrepareEntry, error) {
	if err := ctx.Err(); err != nil {
		return scannedPrepareEntry{}, fmt.Errorf("scan collection: %w", err)
	}
	if isReserved(strings.ToLower(entry.Name())) || !isSupportedName(entry.Name()) {
		return scannedPrepareEntry{}, nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return scannedPrepareEntry{}, fmt.Errorf("artwork %s is a symlink", entry.Name())
	}
	item, err := inspectPrepareItem(ctx, root, entry, limits)
	if err == nil {
		return scannedPrepareEntry{item: item, include: true}, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return scannedPrepareEntry{}, err
	}
	return scannedPrepareEntry{warning: fmt.Sprintf("excluded artwork %s: %v", entry.Name(), err)}, nil
}

func inspectPrepareItem(
	ctx context.Context,
	root string,
	entry os.DirEntry,
	limits inventoryLimits,
) (Item, error) {
	info, err := entry.Info()
	if err != nil {
		return Item{}, fmt.Errorf("inspect: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Item{}, errors.New("symlink is not allowed")
	}
	if !info.Mode().IsRegular() {
		return Item{}, errors.New("not a regular file")
	}
	if info.Size() > limits.maxBytes {
		return Item{}, fmt.Errorf("exceeds %d-byte limit", limits.maxBytes)
	}
	path := filepath.Join(root, entry.Name())
	file, err := os.Open(path)
	if err != nil {
		return Item{}, fmt.Errorf("open: %w", err)
	}
	validated, validateErr := readAndValidateWithOptions(ctx, file, entry.Name(), validationOptions(limits))
	closeErr := file.Close()
	if validateErr != nil {
		return Item{}, fmt.Errorf("validate: %w", errors.Join(validateErr, closeErr))
	}
	if closeErr != nil {
		return Item{}, fmt.Errorf("close: %w", closeErr)
	}
	return Item{
		Name: entry.Name(), Key: entry.Name(), Path: path, Digest: validated.digest,
		Type: validated.typeID, Size: info.Size(), Width: validated.width, Height: validated.height,
		Origin:     Origin{Key: "operator:" + entry.Name(), Class: OriginOperator},
		visualHash: validated.visualHash, visualHashValid: validated.visualHashValid,
	}, nil
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

//nolint:gocyclo // explicit field comparison prevents persistence schema fields from being skipped
func manifestsEqual(left, right manifest) bool {
	if left.Version != right.Version || left.Generation != right.Generation || len(left.Items) != len(right.Items) {
		return false
	}
	for index := range left.Items {
		leftItem, rightItem := left.Items[index], right.Items[index]
		if leftItem.Name != rightItem.Name || leftItem.Digest != rightItem.Digest || leftItem.Type != rightItem.Type ||
			leftItem.Width != rightItem.Width || leftItem.Height != rightItem.Height ||
			leftItem.OriginKey != rightItem.OriginKey || leftItem.Class != rightItem.Class ||
			leftItem.Key != rightItem.Key || leftItem.TransformKey != rightItem.TransformKey ||
			leftItem.Derivative != rightItem.Derivative || !slices.Equal(leftItem.SourceKeys, rightItem.SourceKeys) {
			return false
		}
	}
	return true
}
