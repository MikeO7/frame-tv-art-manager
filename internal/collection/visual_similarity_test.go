package collection_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestPrepareKeepsVisuallySimilarArtworkWithoutWarning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeGradient := func(name string, width, height int) {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := range height {
			for x := range width {
				value := uint8(x * 255 / max(width-1, 1))
				img.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
			}
		}
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeGradient("large.png", 90, 80)
	writeGradient("small.png", 45, 40)
	store, err := collection.New(collection.Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(snapshot.Items) != 2 {
		t.Fatalf("Prepare() retained %d items, want 2", len(snapshot.Items))
	}
	for _, warning := range snapshot.Warnings {
		if strings.Contains(strings.ToLower(warning), "visual duplicate") {
			t.Fatalf("Prepare() warning = %q", warning)
		}
	}
	for _, name := range []string{"large.png", "small.png"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("prepare removed %s: %v", name, err)
		}
	}
}
