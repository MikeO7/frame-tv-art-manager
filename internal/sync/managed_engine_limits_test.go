package sync

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestManagedEngineSizesLocalFilesSeparatelyFromImports(t *testing.T) {
	t.Parallel()

	// A noisy PNG larger than 1 MiB: over MAX_DOWNLOAD_SIZE_MB=1 yet within a
	// 2 MiB local file limit.
	canvas := image.NewNRGBA(image.Rect(0, 0, 640, 640))
	state := uint32(1)
	for index := range canvas.Pix {
		state = state*1664525 + 1013904223
		canvas.Pix[index] = byte(state >> 24)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= 1<<20 || encoded.Len() > 2<<20 {
		t.Fatalf("fixture is %d bytes, want between 1 and 2 MiB", encoded.Len())
	}
	tests := []struct {
		name      string
		localMB   int
		wantItems int
	}{
		{name: "within local limit", localMB: 2, wantItems: 1},
		{name: "over local limit", localMB: 1, wantItems: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "large.png"), encoded.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := managedTestConfig(t, root)
			cfg.MaxDownloadSizeMB, cfg.MaxLocalFileSizeMB = 1, test.localMB
			managed, err := NewManagedEngine(context.Background(), cfg, slog.Default(), nil)
			if err != nil {
				t.Fatalf("NewManagedEngine() error = %v", err)
			}
			t.Cleanup(func() { _ = managed.Close(context.Background()) })
			snapshot, err := managed.Prepare(context.Background(), collectionpkg.PrepareRequest{})
			if err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			if len(snapshot.Items) != test.wantItems {
				t.Fatalf("Prepare() items = %d, want %d", len(snapshot.Items), test.wantItems)
			}
		})
	}
}
