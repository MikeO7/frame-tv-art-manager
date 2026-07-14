package sync

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

func TestArtworkCollectionBoundaryValidationAndCancellation(t *testing.T) {
	t.Parallel()

	if collection, err := newArtworkCollection(nil, nil, nil, nil); err == nil || collection != nil {
		t.Fatalf("newArtworkCollection(nil) = %v, %v, want configuration error", collection, err)
	}
	if collection, err := newArtworkCollection(&config.Config{}, nil, nil, nil); err == nil || collection != nil {
		t.Fatalf("newArtworkCollection(nil store) = %v, %v, want store error", collection, err)
	}

	root := t.TempDir()
	store := &recordingCollectionStore{snapshot: collectionpkg.Snapshot{Generation: "stable", DryRun: true}}
	boundary, err := newArtworkCollection(&config.Config{ArtworkDir: root, DryRun: true}, nil, nil, store)
	if err != nil {
		t.Fatalf("newArtworkCollection() error = %v", err)
	}

	prepared, err := boundary.Prepare(context.Background(), collectionpkg.PrepareRequest{})
	if err != nil || prepared.Generation != "stable" || !store.prepareRequest.DryRun {
		t.Fatalf("Prepare() = %+v, %v; request = %+v", prepared, err, store.prepareRequest)
	}
	imported, err := boundary.Import(context.Background(), collectionpkg.ImportRequest{
		Reader: bytes.NewReader(createSmallJPEG(t)), Hint: "art.jpg",
	})
	if err != nil || imported.Generation != "stable" || !store.importRequest.DryRun {
		t.Fatalf("Import() = %+v, %v; request = %+v", imported, err, store.importRequest)
	}
	stage := t.TempDir()
	origin := collectionpkg.Origin{Key: "source:apply", Class: collectionpkg.OriginSource}
	applied, err := boundary.Apply(context.Background(), collectionpkg.ApplyRequest{
		Directory: stage, Origins: map[string]collectionpkg.Origin{"art.jpg": origin},
	})
	if err != nil || applied.Generation != "stable" || !store.applyRequest.DryRun ||
		store.applyRequest.Directory != stage || store.applyRequest.Origins["art.jpg"] != origin {
		t.Fatalf("Apply() = %+v, %v; request = %+v", applied, err, store.applyRequest)
	}

	local := boundary.(*localCollection)
	local.mutation <- struct{}{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := local.Prepare(canceled, collectionpkg.PrepareRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Prepare() error = %v, want context cancellation", err)
	}
	if _, err := local.Import(canceled, collectionpkg.ImportRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Import() error = %v, want context cancellation", err)
	}
	if _, err := local.Apply(canceled, collectionpkg.ApplyRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Apply() error = %v, want context cancellation", err)
	}
	if _, err := local.prepareCycle(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepareCycle() error = %v, want context cancellation", err)
	}
	<-local.mutation
}

type recordingCollectionStore struct {
	snapshot       collectionpkg.Snapshot
	prepareRequest collectionpkg.PrepareRequest
	importRequest  collectionpkg.ImportRequest
	applyRequest   collectionpkg.ApplyRequest
	applyCalls     int
}

func (store *recordingCollectionStore) Prepare(
	_ context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	store.prepareRequest = request
	return store.snapshot, nil
}

func (store *recordingCollectionStore) Import(
	_ context.Context,
	request collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	store.importRequest = request
	return store.snapshot, nil
}

func (store *recordingCollectionStore) Apply(
	_ context.Context,
	request collectionpkg.ApplyRequest,
) (collectionpkg.Snapshot, error) {
	store.applyCalls++
	store.applyRequest = request
	return store.snapshot, nil
}

func TestLocalCollectionOptimizationPreflightSkipsOrStages(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		wantApply int
	}{
		{name: "verified optimized snapshot skips stage and apply", filename: "gallery_4x4_opt.h_abcdef123456.jpg"},
		{name: "raw jpeg still stages", filename: "gallery.jpg", wantApply: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artworkDir := t.TempDir()
			path := filepath.Join(artworkDir, test.filename)
			if err := os.WriteFile(path, createJPEG(t, 4, 4), 0o644); err != nil {
				t.Fatalf("write artwork: %v", err)
			}
			base := newTestCollectionStore(t, artworkDir)
			recording := &countingCollectionStore{CollectionStore: base}
			manager := &localCollection{
				cfg: &config.Config{
					ArtworkDir: artworkDir, OptimizeEnabled: true,
					OptimizeMaxWidth: 4, OptimizeMaxHeight: 4, OptimizeJPEGQuality: 90,
				},
				logger: slog.Default(), loader: fixedSourceLoader{},
				catalog: sources.NewArtworkCatalog(artworkDir, slog.Default()), store: recording,
			}
			tempRoot := t.TempDir()
			t.Setenv("TMPDIR", tempRoot)

			result, err := manager.prepareCycle(context.Background())
			if err != nil {
				t.Fatalf("prepareCycle() error = %v", err)
			}
			if recording.applyCalls != test.wantApply {
				t.Fatalf("Apply() calls = %d, want %d", recording.applyCalls, test.wantApply)
			}
			entries, err := os.ReadDir(tempRoot)
			if err != nil {
				t.Fatalf("read temp root: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("optimization stage leaked entries: %v", entries)
			}
			if len(result.snapshot.Items) != 1 {
				t.Fatalf("snapshot items = %d, want 1", len(result.snapshot.Items))
			}
		})
	}
}

type countingCollectionStore struct {
	CollectionStore
	applyCalls int
}

func (store *countingCollectionStore) Apply(
	ctx context.Context,
	request collectionpkg.ApplyRequest,
) (collectionpkg.Snapshot, error) {
	store.applyCalls++
	return store.CollectionStore.Apply(ctx, request)
}

func TestLocalCollectionPrepareOwnsCompleteCycle(t *testing.T) {
	t.Parallel()

	artworkDir := t.TempDir()
	file, err := os.Create(filepath.Join(artworkDir, "source.jpg"))
	if err != nil {
		t.Fatalf("create source artwork: %v", err)
	}
	if err := jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 12, 8)), nil); err != nil {
		_ = file.Close()
		t.Fatalf("encode source artwork: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close source artwork: %v", err)
	}

	cfg := &config.Config{
		ArtworkDir: artworkDir, OptimizeEnabled: true,
		OptimizeMaxWidth: 10, OptimizeMaxHeight: 6, OptimizeJPEGQuality: 90,
	}
	catalog := sources.NewArtworkCatalog(artworkDir, slog.Default())
	collection := &localCollection{
		cfg: cfg, logger: slog.Default(), loader: fixedSourceLoader{downloaded: 1}, catalog: catalog,
		store: newTestCollectionStore(t, artworkDir),
	}

	got, err := collection.prepareCycle(context.Background())
	if err != nil {
		t.Fatalf("prepare collection: %v", err)
	}
	if got.downloaded != 1 || got.optimized != 1 || len(got.snapshot.Items) != 1 || len(got.warnings) != 0 {
		t.Fatalf("prepared collection = %+v", got)
	}
}

func TestLocalCollectionPreservesSourceOriginAcrossOptimization(t *testing.T) {
	t.Parallel()

	artworkDir := t.TempDir()
	store := newTestCollectionStore(t, artworkDir)
	origin := collectionpkg.Origin{Key: "source:direct__landscape", Class: collectionpkg.OriginSource}
	if _, err := store.Import(context.Background(), collectionpkg.ImportRequest{
		Reader: bytes.NewReader(createJPEG(t, 12, 8)), Hint: "001__direct__landscape.jpg", Origin: origin,
	}); err != nil {
		t.Fatalf("seed source artwork: %v", err)
	}
	catalog := sources.NewArtworkCatalog(artworkDir, slog.Default())
	manager := &localCollection{
		cfg: &config.Config{
			ArtworkDir: artworkDir, OptimizeEnabled: true,
			OptimizeMaxWidth: 10, OptimizeMaxHeight: 6, OptimizeJPEGQuality: 90,
		},
		logger: slog.Default(), loader: fixedSourceLoader{}, catalog: catalog, store: store,
	}

	prepared, err := manager.prepareCycle(context.Background())
	if err != nil {
		t.Fatalf("prepare optimized source: %v", err)
	}
	if prepared.optimized != 1 || len(prepared.snapshot.Items) != 1 {
		t.Fatalf("prepared optimized source = %+v", prepared)
	}
	item := prepared.snapshot.Items[0]
	if item.Origin != origin || item.Name == "001__direct__landscape.jpg" {
		t.Fatalf("optimized source origin = %+v", item)
	}
}

func createJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buffer.Bytes()
}

func TestLocalCollectionPreparePropagatesSourceFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("provider offline")
	artworkDir := t.TempDir()
	collection := &localCollection{
		cfg: &config.Config{ArtworkDir: artworkDir}, logger: slog.Default(),
		loader:  fixedSourceLoader{err: wantErr},
		catalog: sources.NewArtworkCatalog(artworkDir, slog.Default()),
		store:   newTestCollectionStore(t, artworkDir),
	}

	_, err := collection.prepareCycle(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("prepare collection error = %v, want %v", err, wantErr)
	}
}

func TestLocalCollectionPrepareDryRunIsReadOnly(t *testing.T) {
	t.Parallel()

	artworkDir := t.TempDir()
	name := "operator original.jpg"
	file, err := os.Create(filepath.Join(artworkDir, name))
	if err != nil {
		t.Fatalf("create artwork: %v", err)
	}
	if err := jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 12, 8)), nil); err != nil {
		_ = file.Close()
		t.Fatalf("encode artwork: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close artwork: %v", err)
	}

	collection := &localCollection{
		cfg:    &config.Config{ArtworkDir: artworkDir, DryRun: true, OptimizeEnabled: true},
		logger: slog.Default(), loader: panicSourceLoader{},
		catalog: sources.NewArtworkCatalog(artworkDir, slog.Default()),
		store:   newTestCollectionStore(t, artworkDir),
	}
	result, err := collection.prepareCycle(context.Background())
	if err != nil {
		t.Fatalf("prepare dry-run: %v", err)
	}
	if len(result.snapshot.Items) != 1 || result.snapshot.Items[0].Name != name ||
		result.downloaded != 0 || result.optimized != 0 {
		t.Fatalf("dry-run result = %+v", result)
	}
	entries, err := os.ReadDir(artworkDir)
	if err != nil {
		t.Fatalf("read artwork directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("dry-run changed artwork namespace: %v", entries)
	}
}

func newTestCollectionStore(t *testing.T, artworkDir string) CollectionStore {
	t.Helper()
	store, err := collectionpkg.New(collectionpkg.Config{Root: artworkDir})
	if err != nil {
		t.Fatalf("construct test collection store: %v", err)
	}
	return store
}

type fixedSourceLoader struct {
	downloaded int
	err        error
}

type panicSourceLoader struct{}

func (panicSourceLoader) Sync(context.Context) (int, error) {
	panic("source loader called during dry-run")
}

func (l fixedSourceLoader) Sync(context.Context) (int, error) {
	return l.downloaded, l.err
}
