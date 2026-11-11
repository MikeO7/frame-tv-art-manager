package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func TestArtworkCollectionBoundaryValidationAndCancellation(t *testing.T) {
	t.Parallel()

	if collection, err := newLocalCollection(nil, nil, nil, nil, newTransformGate()); err == nil || collection != nil {
		t.Fatalf("newLocalCollection(nil) = %v, %v, want configuration error", collection, err)
	}
	if collection, err := newLocalCollection(&config.Config{}, nil, nil, nil, newTransformGate()); err == nil || collection != nil {
		t.Fatalf("newLocalCollection(nil store) = %v, %v, want store error", collection, err)
	}

	root := t.TempDir()
	store := &recordingCollectionStore{snapshot: collectionpkg.Snapshot{Generation: "stable", DryRun: true}}
	boundary, err := newLocalCollection(&config.Config{ArtworkDir: root, DryRun: true}, nil, nil, store, newTransformGate())
	if err != nil {
		t.Fatalf("newLocalCollection() error = %v", err)
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

	// A second cycle waits for the running one and gives up with its caller.
	local := boundary
	local.cycle <- struct{}{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := local.prepareCycle(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepareCycle() error = %v, want context cancellation", err)
	}
	<-local.cycle
}

func TestLocalCollectionLogsInventoryBoundaryAndDuration(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	store := &gatedCollectionStore{
		Store:   newTestCollectionStore(t, root),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	collection := &localCollection{
		cfg:    &config.Config{ArtworkDir: root},
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
		store:  store,
	}

	done := make(chan error, 1)
	go func() {
		_, err := collection.Prepare(context.Background(), collectionpkg.PrepareRequest{})
		done <- err
	}()
	<-store.started
	if !strings.Contains(output.String(), `"msg":"inventorying local artwork"`) {
		t.Fatal("inventory did not log progress before blocking work")
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	var completed map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode structured log: %v; line=%q", err, line)
		}
		if entry["msg"] == "local artwork inventory complete" {
			completed = entry
		}
	}
	if completed == nil {
		t.Fatal("inventory completion was not logged")
	}
	if _, ok := completed["duration_ms"].(float64); !ok {
		t.Fatalf("completion duration = %#v; entry=%#v", completed["duration_ms"], completed)
	}
}

func TestLocalCollectionLogsInventoryFailureDuration(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	wantErr := errors.New("inventory unavailable")
	release := make(chan struct{})
	close(release)
	store := &gatedCollectionStore{
		Store:   newTestCollectionStore(t, root),
		started: make(chan struct{}),
		release: release,
		err:     wantErr,
	}
	collection := &localCollection{
		cfg:    &config.Config{ArtworkDir: root},
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
		store:  store,
	}

	_, err := collection.Prepare(context.Background(), collectionpkg.PrepareRequest{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Prepare() error = %v, want %v", err, wantErr)
	}
	logs := output.String()
	if !strings.Contains(logs, `"msg":"local artwork inventory failed"`) ||
		!strings.Contains(logs, `"duration_ms":`) {
		t.Fatalf("inventory failure timing log missing: %s", logs)
	}
}

type gatedCollectionStore struct {
	collectionpkg.Store
	started chan struct{}
	release chan struct{}
	err     error
}

func (store *gatedCollectionStore) Prepare(
	ctx context.Context,
	request collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	close(store.started)
	select {
	case <-store.release:
		if store.err != nil {
			return collectionpkg.Snapshot{}, store.err
		}
		return store.Store.Prepare(ctx, request)
	case <-ctx.Done():
		return collectionpkg.Snapshot{}, ctx.Err()
	}
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

func (store *recordingCollectionStore) ImportBatch(
	_ context.Context,
	requests []collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	if len(requests) > 0 {
		store.importRequest = requests[0]
	}
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
		width     int
		height    int
		wantApply int
	}{
		{name: "exact raw image skips needless transform", filename: "gallery.jpg", width: 4, height: 4},
		{name: "wrong-size raw jpeg stages", filename: "wide.jpg", width: 8, height: 4, wantApply: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artworkDir := t.TempDir()
			path := filepath.Join(artworkDir, test.filename)
			if err := os.WriteFile(path, createJPEG(t, test.width, test.height), 0o644); err != nil {
				t.Fatalf("write artwork: %v", err)
			}
			base := newTestCollectionStore(t, artworkDir)
			recording := &countingCollectionStore{Store: base}
			manager := &localCollection{
				cfg: &config.Config{
					ArtworkDir: artworkDir, OptimizeEnabled: true,
					OptimizeMaxWidth: 4, OptimizeMaxHeight: 4, OptimizeJPEGQuality: 90,
				},
				logger: slog.Default(), loader: fixedSourceLoader{}, store: recording,
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

func TestLocalCollectionOptimizationPreservesUploadIdentityInManifest(t *testing.T) {
	t.Parallel()
	artworkDir := t.TempDir()
	store := newTestCollectionStore(t, artworkDir)
	imported, err := store.Import(context.Background(), collectionpkg.ImportRequest{
		Reader: bytes.NewReader(createJPEG(t, 8, 4)), Hint: "family.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	original := imported.Items[0]
	cfg := &config.Config{
		ArtworkDir: artworkDir, OptimizeEnabled: true,
		OptimizeMaxWidth: 4, OptimizeMaxHeight: 4, OptimizeJPEGQuality: 90,
	}
	manager := &localCollection{
		cfg: cfg, logger: slog.Default(), loader: fixedSourceLoader{}, store: store,
	}
	result, err := manager.prepareCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.snapshot.Items[0]
	if got.Name == original.Name || got.Key != original.Key || got.Origin != original.Origin ||
		got.Derivative != collectionpkg.DerivativeOptimized || got.TransformKey != optimize.TransformKey(cfg.OptimizeOptions()) {
		t.Fatalf("optimized upload metadata = %+v; original = %+v", got, original)
	}
}

type countingCollectionStore struct {
	collectionpkg.Store
	applyCalls int
}

func (store *countingCollectionStore) Apply(
	ctx context.Context,
	request collectionpkg.ApplyRequest,
) (collectionpkg.Snapshot, error) {
	store.applyCalls++
	return store.Store.Apply(ctx, request)
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
	collection := &localCollection{
		cfg: cfg, logger: slog.Default(), loader: fixedSourceLoader{downloaded: 1},
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
	manager := &localCollection{
		cfg: &config.Config{
			ArtworkDir: artworkDir, OptimizeEnabled: true,
			OptimizeMaxWidth: 10, OptimizeMaxHeight: 6, OptimizeJPEGQuality: 90,
		},
		logger: slog.Default(), loader: fixedSourceLoader{}, store: store,
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

func TestLocalCollectionPrepareKeepsLastKnownGoodArtWhenSourcesFail(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("provider offline")
	tests := []struct {
		name     string
		canceled bool
		wantErr  error
	}{
		{name: "provider outage degrades"},
		{name: "cancellation stops the cycle", canceled: true, wantErr: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			artworkDir := t.TempDir()
			store := newTestCollectionStore(t, artworkDir)
			if _, err := store.Import(context.Background(), collectionpkg.ImportRequest{
				Reader: bytes.NewReader(createJPEG(t, 12, 8)), Hint: "kept.jpg",
				Origin: collectionpkg.Origin{Key: "source:direct__kept", Class: collectionpkg.OriginSource},
			}); err != nil {
				t.Fatalf("seed artwork: %v", err)
			}
			status := health.NewStatus()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loaderErr := providerErr
			if test.canceled {
				cancel()
				loaderErr = context.Canceled
			}
			collection := &localCollection{
				cfg: &config.Config{ArtworkDir: artworkDir}, logger: slog.New(slog.DiscardHandler), health: status,
				loader: fixedSourceLoader{err: loaderErr}, store: store,
			}

			prepared, err := collection.prepareCycle(ctx)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("prepare collection error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil || len(prepared.snapshot.Items) != 1 || len(prepared.warnings) != 1 {
				t.Fatalf("prepare collection = %+v, %v; want the kept artwork and one warning", prepared, err)
			}
			if status.SourceWarning == "" {
				t.Fatal("source failure was not reported to health")
			}
		})
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
		store: newTestCollectionStore(t, artworkDir),
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

func newTestCollectionStore(t *testing.T, artworkDir string) collectionpkg.Store {
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

func (panicSourceLoader) Sync(context.Context, collectionpkg.Snapshot) (int, error) {
	panic("source loader called during dry-run")
}

func (l fixedSourceLoader) Sync(context.Context, collectionpkg.Snapshot) (int, error) {
	return l.downloaded, l.err
}

type blockingSourceLoader struct {
	started chan struct{}
	release chan struct{}
}

func (l blockingSourceLoader) Sync(ctx context.Context, _ collectionpkg.Snapshot) (int, error) {
	close(l.started)
	select {
	case <-l.release:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// applyHookStore runs before once, ahead of the first Apply.
type applyHookStore struct {
	collectionpkg.Store
	before     func()
	applyCalls int
}

func (store *applyHookStore) Apply(ctx context.Context, request collectionpkg.ApplyRequest) (collectionpkg.Snapshot, error) {
	store.applyCalls++
	if store.applyCalls == 1 {
		store.before()
	}
	return store.Store.Apply(ctx, request)
}

// createGrayJPEG is a flat picture of one gray level, so pictures of
// different levels stay distinct after optimization.
func createGrayJPEG(t *testing.T, level uint8) []byte {
	t.Helper()
	picture := image.NewGray(image.Rect(0, 0, 8, 4))
	for index := range picture.Pix {
		picture.Pix[index] = level
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, picture, nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buffer.Bytes()
}

func requireAllOptimized(t *testing.T, snapshot collectionpkg.Snapshot, want int) {
	t.Helper()
	if len(snapshot.Items) != want {
		t.Fatalf("cycle snapshot has %d items, want %d: %+v", len(snapshot.Items), want, snapshot.Items)
	}
	for _, item := range snapshot.Items {
		if item.Derivative != collectionpkg.DerivativeOptimized {
			t.Fatalf("%s reached the cycle result unoptimized", item.Name)
		}
	}
}

// A cycle downloads and optimizes for minutes. An upload or an assistant's
// inventory read during that time must be served at once, and the uploaded
// picture must still come out of the cycle optimized.
func TestLocalCollectionServesImportsWhileACycleDownloads(t *testing.T) {
	t.Parallel()
	artworkDir := t.TempDir()
	store := newTestCollectionStore(t, artworkDir)
	if _, err := store.Import(context.Background(), collectionpkg.ImportRequest{
		Reader: bytes.NewReader(createJPEG(t, 8, 4)), Hint: "first.jpg",
	}); err != nil {
		t.Fatal(err)
	}
	loader := blockingSourceLoader{started: make(chan struct{}), release: make(chan struct{})}
	manager := &localCollection{
		cfg: &config.Config{
			ArtworkDir: artworkDir, OptimizeEnabled: true,
			OptimizeMaxWidth: 4, OptimizeMaxHeight: 4, OptimizeJPEGQuality: 90,
		},
		logger: slog.Default(), loader: loader, store: store,
	}
	type outcome struct {
		prepared preparedCollection
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		prepared, err := manager.prepareCycle(context.Background())
		done <- outcome{prepared: prepared, err: err}
	}()
	<-loader.started

	waiting, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := manager.Prepare(waiting, collectionpkg.PrepareRequest{DryRun: true}); err != nil {
		t.Fatalf("inventory read during a cycle: %v", err)
	}
	if _, err := manager.Import(waiting, collectionpkg.ImportRequest{
		Reader: bytes.NewReader(createGrayJPEG(t, 200)), Hint: "second.jpg",
	}); err != nil {
		t.Fatalf("import during a cycle: %v", err)
	}
	select {
	case result := <-done:
		t.Fatalf("cycle finished while its download was still blocked: %+v", result)
	default:
	}
	close(loader.release)
	result := <-done
	if result.err != nil {
		t.Fatalf("prepareCycle() error = %v", result.err)
	}
	requireAllOptimized(t, result.prepared.snapshot, 2)
}

// A picture imported while the cycle is already rendering is kept by the
// publication and rendered by a further pass of the same cycle.
func TestLocalCollectionOptimizesArtworkThatArrivesWhileRendering(t *testing.T) {
	t.Parallel()
	artworkDir := t.TempDir()
	inner := newTestCollectionStore(t, artworkDir)
	if _, err := inner.Import(context.Background(), collectionpkg.ImportRequest{
		Reader: bytes.NewReader(createJPEG(t, 8, 4)), Hint: "first.jpg",
	}); err != nil {
		t.Fatal(err)
	}
	store := &applyHookStore{Store: inner}
	manager := &localCollection{
		cfg: &config.Config{
			ArtworkDir: artworkDir, OptimizeEnabled: true,
			OptimizeMaxWidth: 4, OptimizeMaxHeight: 4, OptimizeJPEGQuality: 90,
		},
		logger: slog.Default(), loader: fixedSourceLoader{}, store: store,
	}
	store.before = func() {
		if _, err := manager.Import(context.Background(), collectionpkg.ImportRequest{
			Reader: bytes.NewReader(createGrayJPEG(t, 200)), Hint: "second.jpg",
		}); err != nil {
			t.Errorf("import while rendering: %v", err)
		}
	}
	result, err := manager.prepareCycle(context.Background())
	if err != nil {
		t.Fatalf("prepareCycle() error = %v", err)
	}
	requireAllOptimized(t, result.snapshot, 2)
	if store.applyCalls != 2 || result.optimized != 2 {
		t.Fatalf("published %d times and optimized %d pictures, want 2 and 2", store.applyCalls, result.optimized)
	}
}
