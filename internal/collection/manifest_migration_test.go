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
