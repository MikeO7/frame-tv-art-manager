package optimize

import (
	"context"
	"errors"
	"image"
	"image/color"
	"path/filepath"
	"testing"
)

func TestOptimizeFileContextHonorsCancellationBeforeIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "missing.jpg")

	name, modified, err := optimizeFile(ctx, path, DefaultConfig(), discardLogger(), defaultPixelWorkers())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OptimizeFileContext() error = %v, want context.Canceled", err)
	}
	if name != "missing.jpg" || modified {
		t.Fatalf("OptimizeFileContext() = (%q, %t), want (missing.jpg, false)", name, modified)
	}
}

func TestGalleryMasterPolishUsesDefaultWorkerEnvelope(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 255, G: 240, B: 230, A: 255})

	result := galleryMasterPolish(img)
	if result != img {
		t.Fatal("galleryMasterPolish() replaced the input buffer")
	}
	got := result.RGBAAt(0, 0)
	if got.R == 255 || got.G == 240 || got.B == 230 {
		t.Fatalf("galleryMasterPolish() left the source pixel unchanged: %#v", got)
	}
	if got.A != 255 {
		t.Fatalf("galleryMasterPolish() alpha = %d, want 255", got.A)
	}
}
