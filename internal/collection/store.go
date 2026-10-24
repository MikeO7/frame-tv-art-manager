package collection

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
)

const (
	defaultMaxImportBytes int64 = 50 << 20
	defaultMaxPixels      int64 = 40_000_000
)

type store struct {
	root           string
	maxItems       int
	maxImportBytes int64
	maxPixels      int64
	mutation       chan struct{}
}

func New(cfg Config) (Store, error) {
	if cfg.Root == "" {
		return nil, errors.New("collection root is empty")
	}
	if !filepath.IsAbs(cfg.Root) {
		return nil, fmt.Errorf("collection root %q is not absolute", cfg.Root)
	}
	if cfg.MaxItems < 0 || cfg.MaxImportBytes < 0 || cfg.MaxPixels < 0 {
		return nil, errors.New("collection limits must not be negative")
	}
	if cfg.MaxImportBytes == 0 {
		cfg.MaxImportBytes = defaultMaxImportBytes
	}
	if cfg.MaxPixels == 0 {
		cfg.MaxPixels = defaultMaxPixels
	}
	return &store{
		root:           filepath.Clean(cfg.Root),
		maxItems:       cfg.MaxItems,
		maxImportBytes: cfg.MaxImportBytes,
		maxPixels:      cfg.MaxPixels,
		mutation:       make(chan struct{}, 1),
	}, nil
}

// Import validates and commits one artwork through the same transaction as
// ImportBatch.
func (s *store) Import(ctx context.Context, request ImportRequest) (Snapshot, error) {
	return s.ImportBatch(ctx, []ImportRequest{request})
}

// ImportBatch validates and commits multiple independent imports under one
// collection mutation. It inventories and verifies the collection once for
// the whole batch while preserving the ordered change result of each request.
func (s *store) ImportBatch(ctx context.Context, requests []ImportRequest) (Snapshot, error) {
	if len(requests) == 0 {
		return Snapshot{}, errors.New("import batch is empty")
	}
	dryRun := requests[0].DryRun
	for _, request := range requests {
		if request.Reader == nil {
			return Snapshot{}, errors.New("import reader is nil")
		}
		if request.DryRun != dryRun {
			return Snapshot{}, errors.New("import batch mixes dry-run and committed requests")
		}
	}
	if err := s.acquire(ctx); err != nil {
		return Snapshot{}, err
	}
	defer s.release()

	inputs := make([]validatedImage, len(requests))
	origins := make([]Origin, len(requests))
	for index, request := range requests {
		input, err := s.validateRequest(ctx, request)
		if err != nil {
			return Snapshot{}, fmt.Errorf("validate import %d: %w", index+1, err)
		}
		origin, err := importOrigin(request.Origin, input)
		if err != nil {
			return Snapshot{}, fmt.Errorf("validate import %d origin: %w", index+1, err)
		}
		inputs[index], origins[index] = input, origin
	}
	items, err := s.inventory(ctx, dryRun)
	if err != nil {
		return Snapshot{}, err
	}
	plan, data, err := s.planImports(items, inputs, origins)
	if err != nil {
		return Snapshot{}, err
	}
	if dryRun {
		return buildSnapshot(s.root, items, plan.changes, nil, true), nil
	}
	if err := ensureLayout(s.root); err != nil {
		return Snapshot{}, fmt.Errorf("prepare collection layout: %w", err)
	}
	if len(plan.additions) == 0 {
		if err := commitManifest(ctx, s.root, newManifest(plan.items)); err != nil {
			return Snapshot{}, fmt.Errorf("commit import batch manifest: %w", err)
		}
	} else if err := s.commitBatch(ctx, plan, func(_ context.Context, item Item) ([]byte, error) {
		return data[item.Name], nil
	}); err != nil {
		return Snapshot{}, fmt.Errorf("commit import batch: %w", err)
	}
	return s.verifyCommitted(ctx, plan.items, plan.changes)
}

// planImports projects each validated input onto the collection in order and
// returns the resulting plan with the bytes of every new addition by name.
func (s *store) planImports(items []Item, inputs []validatedImage, origins []Origin) (batchPlan, map[string][]byte, error) {
	plan := batchPlan{items: cloneItems(items)}
	data := make(map[string][]byte, len(inputs))
	for index, input := range inputs {
		next, change, duplicate := s.plan(plan.items, input, origins[index])
		if !duplicate {
			if s.maxItems > 0 && len(next) > s.maxItems {
				return batchPlan{}, nil, fmt.Errorf("collection item limit %d exceeded", s.maxItems)
			}
			added := slices.IndexFunc(next, func(item Item) bool { return item.Name == change.Name })
			plan.additions = append(plan.additions, next[added])
			data[change.Name] = input.data
		}
		plan.items = next
		plan.changes = append(plan.changes, change)
	}
	return plan, data, nil
}

func importOrigin(requested Origin, input validatedImage) (Origin, error) {
	if requested == (Origin{}) {
		return Origin{Key: "upload:" + fmt.Sprintf("%x", input.digest), Class: OriginOperatorUpload}, nil
	}
	item := Item{Name: input.stem + "." + string(input.typeID), Digest: input.digest, Origin: requested}
	if err := validateSnapshotOrigin(item); err != nil {
		return Origin{}, err
	}
	return requested, nil
}

func (s *store) Prepare(ctx context.Context, request PrepareRequest) (Snapshot, error) {
	if err := s.acquire(ctx); err != nil {
		return Snapshot{}, err
	}
	defer s.release()

	return s.prepare(ctx, request)
}

func (s *store) Apply(ctx context.Context, request ApplyRequest) (Snapshot, error) {
	if err := s.acquire(ctx); err != nil {
		return Snapshot{}, err
	}
	defer s.release()

	return s.apply(ctx, request)
}

func (s *store) validateRequest(ctx context.Context, request ImportRequest) (validatedImage, error) {
	limit, err := s.importLimit(request.MaxBytes)
	if err != nil {
		return validatedImage{}, err
	}
	input, err := readAndValidate(ctx, request.Reader, request.Hint, limit, s.pixelLimit(request.Policy))
	if err != nil {
		return validatedImage{}, fmt.Errorf("%w: %w", ErrInvalidImport, err)
	}
	return input, nil
}

// recover validates the layout and finishes an interrupted transaction. A dry
// run never mutates, so it refuses to observe a collection needing recovery.
func (s *store) recover(ctx context.Context, dryRun bool) error {
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

func (s *store) inventory(ctx context.Context, dryRun bool) ([]Item, error) {
	if err := s.recover(ctx, dryRun); err != nil {
		return nil, err
	}
	items, err := scan(ctx, s.root, s.limits())
	if err != nil {
		return nil, fmt.Errorf("inventory collection: %w", err)
	}
	return items, nil
}

// verifyCommitted rescans the collection and confirms it matches expected.
func (s *store) verifyCommitted(ctx context.Context, expected []Item, changes []Change) (Snapshot, error) {
	committed, err := scan(ctx, s.root, s.limits())
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify committed collection: %w", err)
	}
	if err := verifyExpected(expected, committed); err != nil {
		return Snapshot{}, fmt.Errorf("verify committed manifest: %w", err)
	}
	return buildSnapshot(s.root, committed, changes, nil, false), nil
}

func (s *store) limits() inventoryLimits {
	return inventoryLimits{maxBytes: s.maxImportBytes, maxPixels: s.maxPixels}
}

func (s *store) acquire(ctx context.Context) error {
	select {
	case s.mutation <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for collection mutation: %w", ctx.Err())
	}
}

func (s *store) release() {
	<-s.mutation
}

func (s *store) importLimit(requested int64) (int64, error) {
	if requested < 0 {
		return 0, errors.New("import byte limit must not be negative")
	}
	if requested == 0 || requested > s.maxImportBytes {
		return s.maxImportBytes, nil
	}
	return requested, nil
}

func (s *store) pixelLimit(policy Policy) int64 {
	if policy.MaxPixels > 0 && policy.MaxPixels < s.maxPixels {
		return policy.MaxPixels
	}
	return s.maxPixels
}

func verifyExpected(expected, committed []Item) error {
	if len(expected) != len(committed) {
		return fmt.Errorf("item count changed from %d to %d", len(expected), len(committed))
	}
	for index := range expected {
		if !itemEqual(expected[index], committed[index]) {
			return fmt.Errorf("item %d changed during publication", index)
		}
	}
	return nil
}
