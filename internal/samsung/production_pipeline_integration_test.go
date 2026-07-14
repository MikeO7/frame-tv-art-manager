package samsung_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestProductionPipelineDeletesUnknownArtworkIdempotently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collectionRoot := filepath.Join(t.TempDir(), "artwork")
	store, err := collection.New(collection.Config{Root: collectionRoot})
	if err != nil {
		t.Fatalf("collection.New() error = %v", err)
	}
	snapshot, err := store.Prepare(ctx, collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("Store.Prepare() error = %v", err)
	}
	if snapshot.DryRun || len(snapshot.Items) != 0 || snapshot.Generation == "" {
		t.Fatalf("prepared Collection Snapshot = %+v, want committed empty snapshot", snapshot)
	}

	fixture := samsung.NewProtocolTVFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "tokens", "frame-token")
	adapter, err := samsung.NewAdapter(samsung.Config{
		Address: fixture.Address(t), ClientName: "production-pipeline-tracer", TokenPath: tokenPath,
		ConnectTimeout: 2 * time.Second, RequestTimeout: 2 * time.Second, GateTimeout: time.Second,
		BackoffBase: time.Second, BackoffMaximum: time.Minute,
	}, samsung.Dependencies{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("samsung.NewAdapter() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Errorf("Adapter.Close() error = %v", err)
		}
	})

	stateDirectory := filepath.Join(t.TempDir(), "reconciliation")
	service, err := reconcile.New(reconcile.Config{
		StateDirectory: stateDirectory,
		Policy:         reconcile.Policy{RemoveUnknown: true},
	}, reconcile.Dependencies{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("reconcile.New() error = %v", err)
	}

	first, err := service.Run(ctx, reconcile.Request{
		CycleID: "production-tracer-1", TV: adapter, Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("first Service.Run() error = %v", err)
	}
	if first.Status != reconcile.StatusComplete || first.Applied != 1 || first.State.Pending != nil {
		t.Fatalf("first reconciliation result = %+v, want one completed mutation", first)
	}
	if first.State.LastCompleteCycle != "production-tracer-1" || first.State.LastCollectionGen != snapshot.Generation {
		t.Fatalf("first reconciliation state = %+v, want completed cycle and Collection Snapshot generation", first.State)
	}
	if got := fixture.DeleteMutations(); !slices.Equal(got, []string{"art-1"}) {
		t.Fatalf("wire delete mutations = %v, want [art-1]", got)
	}
	if got := fixture.Inventory(); len(got) != 0 {
		t.Fatalf("fixture inventory after postcondition = %v, want empty", got)
	}
	second, err := service.Run(ctx, reconcile.Request{
		CycleID: "production-tracer-2", TV: adapter, Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("second Service.Run() error = %v", err)
	}
	if second.Status != reconcile.StatusComplete || second.Applied != 0 || second.State.Pending != nil {
		t.Fatalf("second reconciliation result = %+v, want idempotent completion", second)
	}
	if second.State.LastCompleteCycle != "production-tracer-2" || second.State.LastCollectionGen != snapshot.Generation {
		t.Fatalf("second reconciliation state = %+v, want current cycle and unchanged Collection Snapshot generation", second.State)
	}
	if got := fixture.DeleteMutations(); !slices.Equal(got, []string{"art-1"}) {
		t.Fatalf("wire delete mutations after second cycle = %v, want one mutation", got)
	}

	assertPathMode(t, filepath.Dir(tokenPath), 0o700)
	assertPathMode(t, tokenPath, 0o600)
	assertPathMode(t, stateDirectory, 0o700)
	stateEntries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatalf("read reconciliation state directory: %v", err)
	}
	if len(stateEntries) != 1 {
		t.Fatalf("reconciliation state entries = %v, want one state file", stateEntries)
	}
	assertPathMode(t, filepath.Join(stateDirectory, stateEntries[0].Name()), 0o600)
}

func TestProductionPipelinePreservesTypedSlideshowKind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collectionRoot := filepath.Join(t.TempDir(), "artwork")
	store, err := collection.New(collection.Config{Root: collectionRoot})
	if err != nil {
		t.Fatalf("collection.New() error = %v", err)
	}
	snapshot, err := store.Prepare(ctx, collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("Store.Prepare() error = %v", err)
	}

	fixture := samsung.NewProtocolTVFixture(t)
	adapter, err := samsung.NewAdapter(samsung.Config{
		Address: fixture.Address(t), ClientName: "typed-slideshow-tracer",
		TokenPath:      filepath.Join(t.TempDir(), "tokens", "frame-token"),
		ConnectTimeout: 2 * time.Second, RequestTimeout: 2 * time.Second, GateTimeout: time.Second,
		BackoffBase: time.Second, BackoffMaximum: time.Minute,
	}, samsung.Dependencies{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("samsung.NewAdapter() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		if err := adapter.Close(closeCtx); err != nil {
			t.Errorf("Adapter.Close() error = %v", err)
		}
	})

	desired := samsung.SlideshowSetting{Interval: 15, Kind: samsung.SlideshowSequential}
	service, err := reconcile.New(reconcile.Config{
		StateDirectory: filepath.Join(t.TempDir(), "reconciliation"),
		Policy: reconcile.Policy{Slideshow: reconcile.SlideshowPolicy{
			Mode: reconcile.PolicySet, Setting: desired,
		}},
	}, reconcile.Dependencies{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("reconcile.New() error = %v", err)
	}

	first, err := service.Run(ctx, reconcile.Request{
		CycleID: "typed-slideshow-1", TV: adapter, Snapshot: snapshot,
	})
	if err != nil || first.Status != reconcile.StatusComplete || first.Applied != 1 {
		t.Fatalf("first typed slideshow reconciliation = %+v, %v", first, err)
	}
	if got := fixture.SlideshowSetting(); got != desired {
		t.Fatalf("fixture slideshow = %+v, want %+v", got, desired)
	}
	if got := fixture.SlideshowMutations(); !slices.Equal(got, []samsung.SlideshowSetting{desired}) {
		t.Fatalf("wire slideshow mutations = %+v, want [%+v]", got, desired)
	}

	second, err := service.Run(ctx, reconcile.Request{
		CycleID: "typed-slideshow-2", TV: adapter, Snapshot: snapshot,
	})
	if err != nil || second.Status != reconcile.StatusComplete || second.Applied != 0 {
		t.Fatalf("second typed slideshow reconciliation = %+v, %v", second, err)
	}
	if got := fixture.SlideshowMutations(); len(got) != 1 {
		t.Fatalf("idempotent cycle emitted extra slideshow writes: %+v", got)
	}
}

func assertPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}
