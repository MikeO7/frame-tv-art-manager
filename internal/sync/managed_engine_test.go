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
	}); err == nil || !strings.Contains(err.Error(), "item limit 1 exceeded") {
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
