package collection_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestPrepareDryRunAdoptsValidatedArtworkWithoutMutation(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "art")
	writeArtwork(t, root, "operator.png", encodeImage(t, "png", 3, 2))
	before := directoryFiles(t, root)

	store := newStore(t, root)
	first, err := store.Prepare(context.Background(), collection.PrepareRequest{DryRun: true})
	if err != nil {
		t.Fatalf("prepare dry run: %v", err)
	}
	second, err := store.Prepare(context.Background(), collection.PrepareRequest{DryRun: true})
	if err != nil {
		t.Fatalf("repeat prepare dry run: %v", err)
	}
	if !first.DryRun || len(first.Items) != 1 || len(first.Changes) != 1 {
		t.Fatalf("snapshot = %+v", first)
	}
	if first.Items[0].Origin.Class != collection.OriginOperator || first.Changes[0].Kind != collection.ChangeAdopted {
		t.Fatalf("operator artwork was not adopted: %+v", first)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("dry-run result is nondeterministic: first=%+v second=%+v", first, second)
	}
	if after := directoryFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry run mutated collection: before=%v after=%v", before, after)
	}
}

func TestPrepareEmptyCollectionDryRunDoesNotCreateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	store := newStore(t, root)
	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{DryRun: true})
	if err != nil {
		t.Fatalf("prepare empty dry run: %v", err)
	}
	if !snapshot.DryRun || len(snapshot.Items) != 0 || len(snapshot.Changes) != 0 || snapshot.Generation == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("dry run created root: %v", err)
	}
}

func TestPrepareCreatesDurableEmptyCollection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	store := newStore(t, root)
	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare empty collection: %v", err)
	}
	if snapshot.DryRun || len(snapshot.Items) != 0 || snapshot.Generation == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	assertMode(t, root, 0o755)
	assertMode(t, filepath.Join(root, ".frame-tv-art-manager", "manifest.json"), 0o600)
}

func TestPrepareCommitsAdoptedInventoryAndStableGeneration(t *testing.T) {
	root := t.TempDir()
	writeArtwork(t, root, "zebra.jpg", encodeImage(t, "jpeg", 3, 2))
	writeArtwork(t, root, "alpha.png", encodeImage(t, "png", 2, 3))
	store := newStore(t, root)

	first, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(first.Items) != 2 || len(first.Changes) != 2 || first.Generation == "" {
		t.Fatalf("first snapshot = %+v", first)
	}
	if first.Items[0].Name != "alpha.png" || first.Items[1].Name != "zebra.jpg" {
		t.Fatalf("items not sorted: %+v", first.Items)
	}
	assertMode(t, filepath.Join(root, ".frame-tv-art-manager", "manifest.json"), 0o600)
	second, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("repeat prepare: %v", err)
	}
	if second.Generation != first.Generation || len(second.Changes) != 0 {
		t.Fatalf("stable inventory changed: first=%+v second=%+v", first, second)
	}
}

func TestPrepareReusesDigestBoundValidationForUnchangedArtwork(t *testing.T) {
	var fullDecodes atomic.Int64
	image.RegisterFormat(
		"png",
		"RUSE",
		func(io.Reader) (image.Image, error) {
			fullDecodes.Add(1)
			return image.NewRGBA(image.Rect(0, 0, 2, 2)), nil
		},
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 2, Height: 2}, nil
		},
	)

	root := t.TempDir()
	writeArtwork(t, root, "stable.png", []byte("RUSE"))
	store, err := collection.New(collection.Config{
		Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20,
		PerceptualDuplicates: true, PerceptualDuplicateDistance: 6,
	})
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	first, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare initial collection: %v", err)
	}
	decodesAfterInitial := fullDecodes.Load()
	if decodesAfterInitial == 0 {
		t.Fatal("initial preparation did not fully decode artwork")
	}

	second, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare unchanged collection: %v", err)
	}
	if got := fullDecodes.Load(); got != decodesAfterInitial {
		t.Fatalf("unchanged artwork full decodes = %d, want %d", got, decodesAfterInitial)
	}
	if second.Generation != first.Generation || len(second.Changes) != 0 {
		t.Fatalf("unchanged snapshot = %+v, first generation %q", second, first.Generation)
	}
}

func TestPrepareRejectsUnsafeProjectedInventoryWithoutCommittingManifest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxItems int
		first    []byte
		second   []byte
		want     string
	}{
		{
			name: "configured item limit", maxItems: 1,
			first: encodeImage(t, "png", 2, 2), second: encodeImage(t, "jpeg", 3, 2),
			want: "item limit 1 exceeded",
		},
		{
			name: "duplicate digest", first: encodeImage(t, "png", 2, 2),
			want: "repeats digest",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			second := testCase.second
			if second == nil {
				second = testCase.first
			}
			writeArtwork(t, root, "first.png", testCase.first)
			secondName := "second.jpg"
			if bytes.Equal(testCase.first, second) {
				secondName = "second.png"
			}
			writeArtwork(t, root, secondName, second)
			store, err := collection.New(collection.Config{Root: root, MaxItems: testCase.maxItems})
			if err != nil {
				t.Fatalf("construct store: %v", err)
			}
			if _, err := store.Prepare(context.Background(), collection.PrepareRequest{}); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Prepare() error = %v, want containing %q", err, testCase.want)
			}
			manifest := filepath.Join(root, ".frame-tv-art-manager", "manifest.json")
			if _, err := os.Lstat(manifest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Prepare() published manifest after rejection: %v", err)
			}
		})
	}
}

func TestPrepareExcludesCorruptArtworkWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	writeArtwork(t, root, "good.png", encodeImage(t, "png", 2, 2))
	writeArtwork(t, root, "broken.jpg", []byte("not an image"))
	store := newStore(t, root)

	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Name != "good.png" || len(snapshot.Warnings) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	assertContents(t, filepath.Join(root, "broken.jpg"), "not an image")
}

func TestPrepareWarnsForUnsupportedArtworkShapesWithoutRemovingThem(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "directory.png"), 0o755); err != nil {
		t.Fatalf("mkdir artwork-shaped path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "oversize.jpg"), bytes.Repeat([]byte("x"), (1<<20)+1), 0o644); err != nil {
		t.Fatalf("write oversized artwork: %v", err)
	}
	store := newStore(t, root)
	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(snapshot.Items) != 0 || len(snapshot.Warnings) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(root, "directory.png")); err != nil {
		t.Fatalf("artwork-shaped directory removed: %v", err)
	}
	assertContents(t, filepath.Join(root, "oversize.jpg"), string(bytes.Repeat([]byte("x"), (1<<20)+1)))
}

func TestPrepareReportsMissingCommittedArtworkWithoutDeletingOtherFiles(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := encodeImage(t, "png", 2, 2)
	committed, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(data), Hint: "missing.png",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := os.Remove(committed.Items[0].Path); err != nil {
		t.Fatalf("remove committed artwork: %v", err)
	}
	before := directoryFiles(t, root)

	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{DryRun: true})
	if err != nil {
		t.Fatalf("prepare dry run: %v", err)
	}
	if len(snapshot.Items) != 0 || len(snapshot.Changes) != 1 || snapshot.Changes[0].Kind != collection.ChangeMissing {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if after := directoryFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry run changed missing state: before=%v after=%v", before, after)
	}
}

func TestPrepareCommitsExplicitMissingAndReplacementDrift(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	committed, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(encodeImage(t, "png", 2, 2)), Hint: "changed.png",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	writeArtwork(t, root, committed.Items[0].Name, encodeImage(t, "png", 3, 3))

	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare drift: %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Origin.Class != collection.OriginOperator || len(snapshot.Changes) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Changes[0].Kind != collection.ChangeAdopted || snapshot.Changes[1].Kind != collection.ChangeMissing {
		t.Fatalf("changes = %+v", snapshot.Changes)
	}
	stable, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil || len(stable.Changes) != 0 {
		t.Fatalf("replacement drift did not stabilize: snapshot=%+v err=%v", stable, err)
	}
}

func TestPrepareRejectsInvalidLayoutAndManifest(t *testing.T) {
	t.Run("layout", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write root blocker: %v", err)
		}
		store := newStore(t, root)
		if snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{}); err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	})
	t.Run("manifest", func(t *testing.T) {
		root := t.TempDir()
		control := filepath.Join(root, ".frame-tv-art-manager")
		if err := os.Mkdir(control, 0o700); err != nil {
			t.Fatalf("mkdir control: %v", err)
		}
		if err := os.WriteFile(filepath.Join(control, "manifest.json"), []byte("{"), 0o600); err != nil {
			t.Fatalf("write malformed manifest: %v", err)
		}
		store := newStore(t, root)
		if snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{}); err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
			t.Fatalf("snapshot=%+v err=%v", snapshot, err)
		}
	})
}

func TestPrepareRejectsArtworkSymlinkWithoutMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.png")
	writeArtwork(t, filepath.Dir(target), filepath.Base(target), encodeImage(t, "png", 1, 1))
	link := filepath.Join(root, "linked.png")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink artwork: %v", err)
	}
	store := newStore(t, root)

	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	info, statErr := os.Lstat(link)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink changed: info=%v err=%v", info, statErr)
	}
}

func TestPrepareRecoversBeforeInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recovery")
	data := encodeImage(t, "png", 2, 3)
	prepareInterruptedImport(t, root, data, "staged")
	store := newStore(t, root)

	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare recovery: %v", err)
	}
	if len(snapshot.Items) != 1 || len(snapshot.Changes) != 0 {
		t.Fatalf("recovered snapshot = %+v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(root, ".frame-tv-art-manager", "transaction.json")); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestPrepareDryRunRefusesRecoveryWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recovery")
	data := encodeImage(t, "png", 2, 3)
	prepareInterruptedImport(t, root, data, "staged")
	before := directoryFiles(t, root)
	store := newStore(t, root)

	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{DryRun: true})
	if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if after := directoryFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry run recovered transaction: before=%v after=%v", before, after)
	}
}

func TestPrepareHonorsCancellationWhileImportOwnsMutation(t *testing.T) {
	store := newStore(t, t.TempDir())
	reader := &gatedReader{
		data: encodeImage(t, "png", 2, 2), started: make(chan struct{}), release: make(chan struct{}),
	}
	importDone := make(chan error, 1)
	go func() {
		_, err := store.Import(context.Background(), collection.ImportRequest{Reader: reader, Hint: "one.png"})
		importDone <- err
	}()
	<-reader.started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	snapshot, err := store.Prepare(ctx, collection.PrepareRequest{})
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	close(reader.release)
	if err := <-importDone; err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestPrepareHonorsCancellationDuringInventory(t *testing.T) {
	store := newStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := store.Prepare(ctx, collection.PrepareRequest{})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}
