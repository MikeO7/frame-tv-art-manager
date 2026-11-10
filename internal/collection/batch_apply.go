package collection

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type batchPlan struct {
	items     []Item
	additions []Item
	deletions []Item
	changes   []Change
}

func (s *store) apply(ctx context.Context, request ApplyRequest) (Snapshot, error) {
	if err := validateStageDirectory(s.root, request.Directory); err != nil {
		return Snapshot{}, err
	}
	current, plan, err := s.planApply(ctx, request)
	if err != nil {
		return Snapshot{}, err
	}
	projected := buildSnapshot(s.root, plan.items, plan.changes, nil, request.DryRun)
	if err := s.validatePreparedSnapshot(projected); err != nil {
		return Snapshot{}, err
	}
	if request.DryRun {
		return projected, nil
	}
	if len(plan.additions)+len(plan.deletions) == 0 {
		return s.applyOriginsOnly(ctx, current, plan.items, projected)
	}
	if err := ensureLayout(s.root); err != nil {
		return Snapshot{}, fmt.Errorf("prepare collection layout: %w", err)
	}
	// Originals are durable before the transaction that deletes the artwork
	// they were copied from.
	if err := s.keepOriginals(ctx, current, plan.items); err != nil {
		return Snapshot{}, err
	}
	if err := s.commitBatch(ctx, plan, stagedDirectory(request.Directory)); err != nil {
		return Snapshot{}, fmt.Errorf("commit staged collection: %w", err)
	}
	return s.verifyCommitted(ctx, plan.items, plan.changes)
}

func (s *store) planApply(ctx context.Context, request ApplyRequest) ([]Item, batchPlan, error) {
	current, err := s.inventory(ctx, request.DryRun)
	if err != nil {
		return nil, batchPlan{}, err
	}
	staged, err := s.scanApplyDirectory(ctx, request.Directory, current, request.Origins, request.Metadata)
	if err != nil {
		return nil, batchPlan{}, fmt.Errorf("inventory staged collection: %w", err)
	}
	plan, err := buildBatchPlan(current, staged, request.Baseline)
	if err != nil {
		return nil, batchPlan{}, err
	}
	return current, plan, nil
}

func (s *store) applyOriginsOnly(ctx context.Context, current, next []Item, projected Snapshot) (Snapshot, error) {
	if slices.EqualFunc(current, next, itemEqual) {
		return projected, nil
	}
	if err := ensureLayout(s.root); err != nil {
		return Snapshot{}, fmt.Errorf("prepare collection layout: %w", err)
	}
	if err := s.keepOriginals(ctx, current, next); err != nil {
		return Snapshot{}, err
	}
	if err := commitManifest(ctx, s.root, newManifest(next)); err != nil {
		return Snapshot{}, fmt.Errorf("commit staged collection origins: %w", err)
	}
	return projected, nil
}

func validateStageDirectory(root, directory string) error {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("staged collection directory must be absolute and canonical")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect staged collection directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("staged collection path must be a non-symlink directory")
	}
	if pathWithin(root, directory) {
		return errors.New("staged collection directory must be outside the collection root")
	}
	rootInfo, err := os.Stat(root)
	if err == nil {
		return validateResolvedStage(root, directory, rootInfo, info)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect collection root: %w", err)
	}
	return nil
}

func validateResolvedStage(root, directory string, rootInfo, directoryInfo os.FileInfo) error {
	if os.SameFile(directoryInfo, rootInfo) {
		return errors.New("staged collection directory must not be the collection root")
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedDirectory, directoryErr := filepath.EvalSymlinks(directory)
	if rootErr != nil || directoryErr != nil {
		return fmt.Errorf("resolve staged collection isolation: %w", errors.Join(rootErr, directoryErr))
	}
	if pathWithin(resolvedRoot, resolvedDirectory) {
		return errors.New("staged collection directory resolves inside the collection root")
	}
	return nil
}

func pathWithin(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func (s *store) scanApplyDirectory(
	ctx context.Context,
	directory string,
	current []Item,
	overrides map[string]Origin,
	metadata map[string]ItemMetadata,
) ([]Item, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read staged collection: %w", err)
	}
	currentByName := make(map[string]Item, len(current))
	for _, item := range current {
		currentByName[item.Name] = item
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		item, err := s.scanApplyEntry(ctx, directory, entry, currentByName, overrides, metadata)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sortItems(items)
	return items, nil
}

//nolint:revive // scan dependencies are explicit capabilities of this internal transaction step
func (s *store) scanApplyEntry(
	ctx context.Context,
	directory string,
	entry os.DirEntry,
	current map[string]Item,
	overrides map[string]Origin,
	metadata map[string]ItemMetadata,
) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, fmt.Errorf("scan staged collection: %w", err)
	}
	if !isArtworkName(entry.Name()) {
		return Item{}, fmt.Errorf("staged entry %q is not artwork", entry.Name())
	}
	item, err := inspectItem(ctx, directory, entry, s.limits(), Item{})
	if err != nil {
		return Item{}, fmt.Errorf("inspect staged artwork %s: %w", entry.Name(), err)
	}
	item.Path = filepath.Join(s.root, item.Name)
	item = withRecordedMetadata(item, current)
	if origin, ok := overrides[item.Name]; ok {
		item.Origin = origin
		if origin.Class == OriginSource {
			item.SourceKeys = appendSourceKey(item.SourceKeys, origin.Key)
		}
	}
	if projected, ok := metadata[item.Name]; ok {
		item = withMetadata(item, projected)
	}
	if err := validateSnapshotOrigin(item); err != nil {
		return Item{}, fmt.Errorf("staged artwork %s origin: %w", item.Name, err)
	}
	return item, nil
}

func buildBatchPlan(current, staged []Item, baseline *Snapshot) (batchPlan, error) {
	old := make(map[string]Item, len(current))
	next := make(map[string]Item, len(staged))
	for _, item := range current {
		old[item.Name] = item
	}
	plan := batchPlan{items: staged}
	for _, item := range staged {
		next[item.Name] = item
		previous, exists := old[item.Name]
		if exists && previous.Digest != item.Digest {
			return batchPlan{}, fmt.Errorf("staged artwork %s replaces committed bytes in place", item.Name)
		}
		if !exists {
			plan.additions = append(plan.additions, item)
			plan.changes = append(plan.changes, Change{Kind: ChangeAdded, Name: item.Name})
		}
	}
	rendered := renderedFrom(baseline)
	for _, item := range current {
		if _, exists := next[item.Name]; exists {
			continue
		}
		// The stage never saw artwork that arrived or changed after its
		// baseline, so its absence from the stage is not a decision to delete.
		if digest, known := rendered[item.Name]; baseline != nil && (!known || digest != item.Digest) {
			plan.items = append(plan.items, item)
			continue
		}
		plan.deletions = append(plan.deletions, item)
		plan.changes = append(plan.changes, Change{Kind: ChangeMissing, Name: item.Name})
	}
	sortItems(plan.items)
	sort.Slice(plan.changes, func(i, j int) bool { return plan.changes[i].Name < plan.changes[j].Name })
	return plan, nil
}

func renderedFrom(baseline *Snapshot) map[string][sha256.Size]byte {
	if baseline == nil {
		return nil
	}
	rendered := make(map[string][sha256.Size]byte, len(baseline.Items))
	for _, item := range baseline.Items {
		rendered[item.Name] = item.Digest
	}
	return rendered
}
