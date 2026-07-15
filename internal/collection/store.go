package collection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
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

func (s *store) Import(ctx context.Context, request ImportRequest) (Snapshot, error) {
	if request.Reader == nil {
		return Snapshot{}, errors.New("import reader is nil")
	}
	if err := s.acquire(ctx); err != nil {
		return Snapshot{}, err
	}
	defer s.release()

	input, err := s.validateRequest(ctx, request)
	if err != nil {
		return Snapshot{}, err
	}
	origin, err := importOrigin(request.Origin, input)
	if err != nil {
		return Snapshot{}, fmt.Errorf("validate import origin: %w", err)
	}
	items, err := s.inventory(ctx, request.DryRun)
	if err != nil {
		return Snapshot{}, err
	}
	projected, change, duplicate := s.plan(items, input, origin)
	if request.DryRun {
		return buildSnapshot(s.root, items, []Change{change}, true), nil
	}
	return s.publish(ctx, input, projected, change, duplicate)
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

func (s *store) inventory(ctx context.Context, dryRun bool) ([]Item, error) {
	if err := validateExistingLayout(s.root); err != nil {
		return nil, fmt.Errorf("validate collection layout: %w", err)
	}
	if dryRun {
		if err := rejectActiveTransaction(ctx, s.root); err != nil {
			return nil, err
		}
	} else if err := recoverTransaction(ctx, s.root); err != nil {
		return nil, fmt.Errorf("recover collection: %w", err)
	}
	items, err := scan(ctx, s.root, s.maxImportBytes, s.maxPixels)
	if err != nil {
		return nil, fmt.Errorf("inventory collection: %w", err)
	}
	return items, nil
}

func (s *store) publish(
	ctx context.Context,
	input validatedImage,
	projected []Item,
	change Change,
	duplicate bool,
) (Snapshot, error) {
	if !duplicate && s.maxItems > 0 && len(projected) > s.maxItems {
		return Snapshot{}, fmt.Errorf("collection item limit %d exceeded", s.maxItems)
	}
	if err := ensureLayout(s.root); err != nil {
		return Snapshot{}, fmt.Errorf("prepare collection layout: %w", err)
	}
	if err := s.commit(ctx, input, projected, duplicate); err != nil {
		return Snapshot{}, fmt.Errorf("commit import: %w", err)
	}
	committed, err := scan(ctx, s.root, s.maxImportBytes, s.maxPixels)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify committed collection: %w", err)
	}
	if err := verifyExpected(projected, committed); err != nil {
		return Snapshot{}, fmt.Errorf("verify committed manifest: %w", err)
	}
	return buildSnapshot(s.root, committed, []Change{change}, false), nil
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

func copyBytes(dst io.Writer, data []byte) error {
	if _, err := dst.Write(data); err != nil {
		return fmt.Errorf("write artwork bytes: %w", err)
	}
	return nil
}
