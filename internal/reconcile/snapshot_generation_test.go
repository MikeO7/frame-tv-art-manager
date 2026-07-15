package reconcile

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestValidateSnapshotAcceptsAuthoritativeCollectionGeneration(t *testing.T) {
	t.Parallel()

	var encoded bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.SetRGBA(0, 0, color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xff})
	if err := png.Encode(&encoded, pixel); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store, err := collection.New(collection.Config{
		Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(encoded.Bytes()), Hint: "art.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.ValidateSnapshot(root, snapshot); err != nil {
		t.Fatalf("authoritative snapshot is invalid: %v", err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		t.Fatalf("reconciliation rejected authoritative snapshot: %v", err)
	}
}
