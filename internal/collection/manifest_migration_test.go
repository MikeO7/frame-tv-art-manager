package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareMigratesVersionOneManifestToDurableMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	value, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := value.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(encodeInternalImage(t, 2, 2)), Hint: "legacy.png",
		Origin: Origin{Key: "source:legacy-provider-item", Class: OriginSource},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := seed.Items[0]
	legacy := manifest{
		Version: 1, Generation: legacyGeneration(seed.Items),
		Items: []manifestItem{{
			Name: item.Name, Digest: stringHex(item.Digest[:]), Type: item.Type,
			Width: item.Width, Height: item.Height, OriginKey: item.Origin.Key, Class: item.Origin.Class,
		}},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, controlDirectory, manifestName)
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := value.Prepare(context.Background(), PrepareRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := migrated.Items[0]; got.Key != got.Name || len(got.SourceKeys) != 1 || got.SourceKeys[0] != item.Origin.Key {
		t.Fatalf("migrated metadata = %+v", got)
	}
	committed, exists, err := readManifest(context.Background(), root)
	if err != nil || !exists || committed.Version != 3 {
		t.Fatalf("committed manifest = version %d, exists %v, error %v", committed.Version, exists, err)
	}
}

func TestPrepareRemovesLegacyVisualHashFromManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	value, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := value.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(encodeInternalImage(t, 2, 2)), Hint: "art.png",
		Origin: Origin{Key: "operator:art.png", Class: OriginOperator},
	}); err != nil {
		t.Fatal(err)
	}
	current, exists, err := readManifest(context.Background(), root)
	if err != nil || !exists {
		t.Fatalf("read current manifest: exists %v, error %v", exists, err)
	}
	current.Items[0].VisualHash = "legacy-invalid-hash"
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, controlDirectory, manifestName)
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := value.Prepare(context.Background(), PrepareRequest{}); err != nil {
		t.Fatal(err)
	}
	migrated, exists, err := readManifest(context.Background(), root)
	if err != nil || !exists {
		t.Fatalf("read migrated manifest: exists %v, error %v", exists, err)
	}
	if got := migrated.Items[0].VisualHash; got != "" {
		t.Fatalf("legacy visual hash retained as %q", got)
	}
}
