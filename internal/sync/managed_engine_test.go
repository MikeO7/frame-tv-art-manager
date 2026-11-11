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
	"strings"
	"testing"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestManagedEngineForcesConfiguredDryRunOnImports(t *testing.T) {
	root := t.TempDir()
	cfg := managedTestConfig(t, root)
	cfg.DryRun = true
	cfg.MaxArtworkImages = 10
	managed, err := NewManagedEngine(context.Background(), cfg, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewManagedEngine() error = %v", err)
	}
	snapshot, err := managed.Import(context.Background(), collectionpkg.ImportRequest{
		Hint: "projected.jpg", Reader: bytes.NewReader(createSmallJPEG(t)),
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if !snapshot.DryRun {
		t.Fatal("Import() did not force the configured dry-run policy")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry-run Import() created durable entries: %v", entries)
	}
}

func TestNewManagedEngineRequiresCanonicalConfiguration(t *testing.T) {
	managed, err := NewManagedEngine(context.Background(), nil, slog.Default(), nil)
	if err == nil || managed != nil {
		t.Fatalf("NewManagedEngine(nil) = %v, %v, want constructor failure", managed, err)
	}
	if !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("NewManagedEngine(nil) error = %q, want configuration context", err)
	}
}

func TestManagedEngineDerivesCollectionRootAndLimitsFromEngineConfiguration(t *testing.T) {
	root := t.TempDir()
	cfg := managedTestConfig(t, root)
	cfg.MaxArtworkImages = 1
	managed, err := NewManagedEngine(context.Background(), cfg, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewManagedEngine() error = %v", err)
	}

	first, err := managed.Import(context.Background(), collectionpkg.ImportRequest{
		Hint: "first.jpg", Reader: bytes.NewReader(createSmallJPEG(t)),
	})
	if err != nil {
		t.Fatalf("first Import() error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Path != filepath.Join(root, first.Items[0].Name) {
		t.Fatalf("first Import() items = %+v, want one item rooted in %q", first.Items, root)
	}

	secondImage := append(append([]byte(nil), createSmallJPEG(t)...), 0x01)
	if _, err := managed.Import(context.Background(), collectionpkg.ImportRequest{
		Hint: "second.jpg", Reader: bytes.NewReader(secondImage),
	}); !errors.Is(err, collectionpkg.ErrCollectionFull) || !strings.Contains(err.Error(), "item limit 1 exceeded") {
		t.Fatalf("second Import() error = %v, want configured item limit", err)
	}
}

func TestManagedEngineLifecycleDelegatesThroughCanonicalBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := managedTestConfig(t, root)
	cfg.DryRun = true
	managed, err := NewManagedEngine(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewManagedEngine() error = %v", err)
	}
	if _, err := managed.Prepare(context.Background(), collectionpkg.PrepareRequest{}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := managed.RunLoop(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunLoop() error = %v, want cancellation", err)
	}
	if err := managed.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestManagedEnginePrepareAppliesConfiguredDryRunToAPlainRequest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local.jpg"), createSmallJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := managedTestConfig(t, root)
	cfg.DryRun = true
	managed, err := NewManagedEngine(context.Background(), cfg, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewManagedEngine() error = %v", err)
	}
	// Startup prepares with an empty request and relies on the collection to
	// apply DRY_RUN.
	snapshot, err := managed.Prepare(context.Background(), collectionpkg.PrepareRequest{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !snapshot.DryRun {
		t.Fatal("Prepare() did not apply the configured dry-run policy")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "local.jpg" {
		t.Fatalf("dry-run Prepare() changed the artwork folder: %v", entries)
	}
}

func TestManagedEngineRefusesAPreviewWhileATransformRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local.jpg"), createSmallJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	managed, err := NewManagedEngine(context.Background(), managedTestConfig(t, root), slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewManagedEngine() error = %v", err)
	}
	t.Cleanup(func() { _ = managed.Close(context.Background()) })
	snapshot, err := managed.Prepare(context.Background(), collectionpkg.PrepareRequest{})
	if err != nil || len(snapshot.Items) != 1 {
		t.Fatalf("Prepare() = %d items, %v; want the one artwork", len(snapshot.Items), err)
	}

	// A collection optimization holds the running slot for minutes.
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- managed.limits.run(context.Background(), false, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	rendered, err := managed.RenderPreview(context.Background(), snapshot.Items[0])
	if rendered != nil || !errors.Is(err, errTransformsBusy) {
		t.Fatalf("RenderPreview() beside a running transform = %v, %v; want errTransformsBusy", rendered, err)
	}
	if !strings.Contains(err.Error(), "try again shortly") || !strings.Contains(err.Error(), "local.jpg") {
		t.Fatalf("RenderPreview() error = %q, want it to name the artwork and say to retry shortly", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if rendered, err := managed.RenderPreview(context.Background(), snapshot.Items[0]); err != nil || rendered == nil {
		t.Fatalf("RenderPreview() on an idle gate = %v, %v; want the preview", rendered, err)
	}
}

func managedTestConfig(t *testing.T, artworkDir string) *config.Config {
	t.Helper()
	return &config.Config{
		TVIPs: []string{"192.0.2.10"}, ArtworkDir: artworkDir, TokenDir: t.TempDir(),
		ClientName: "Frame Manager Test", SyncIntervalMin: 5,
		ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
		UploadAttempts: 1,
	}
}

func createSmallJPEG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 10, 10))
	var output bytes.Buffer
	if err := jpeg.Encode(&output, canvas, nil); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return output.Bytes()
}
