package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestLimitSnapshotKeepsFirstItemsByName(t *testing.T) {
	t.Parallel()

	items := []collectionpkg.Item{
		{Name: "c.jpg", Key: "c.jpg", Digest: [32]byte{3}},
		{Name: "a.jpg", Key: "a.jpg", Digest: [32]byte{1}},
		{Name: "b.jpg", Key: "b.jpg", Digest: [32]byte{2}},
	}
	full := collectionpkg.Snapshot{
		Generation: collectionpkg.SnapshotGeneration(items), Items: items,
		Changes: []collectionpkg.Change{
			{Kind: collectionpkg.ChangeAdded, Name: "c.jpg"},
			{Kind: collectionpkg.ChangeAdded, Name: "a.jpg"},
			{Kind: collectionpkg.ChangeMissing, Name: "gone.jpg"},
		},
	}
	tests := []struct {
		name      string
		limit     int
		wantNames []string
		wantWarn  bool
	}{
		{name: "unlimited", limit: 0, wantNames: []string{"c.jpg", "a.jpg", "b.jpg"}},
		{name: "at limit", limit: 3, wantNames: []string{"c.jpg", "a.jpg", "b.jpg"}},
		{name: "over limit", limit: 2, wantNames: []string{"a.jpg", "b.jpg"}, wantWarn: true},
		{name: "single", limit: 1, wantNames: []string{"a.jpg"}, wantWarn: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, warning := limitSnapshot(full, test.limit)
			if (warning != "") != test.wantWarn {
				t.Fatalf("warning = %q, want warning %v", warning, test.wantWarn)
			}
			names := make([]string, 0, len(got.Items))
			for _, item := range got.Items {
				names = append(names, item.Name)
			}
			if strings.Join(names, ",") != strings.Join(test.wantNames, ",") {
				t.Fatalf("items = %v, want %v", names, test.wantNames)
			}
			if got.Generation != collectionpkg.SnapshotGeneration(got.Items) {
				t.Fatal("limited snapshot generation does not describe its items")
			}
			if !test.wantWarn {
				return
			}
			if !strings.Contains(warning, "MAX_ARTWORK_IMAGES") {
				t.Fatalf("warning = %q, want it to name the setting", warning)
			}
			for _, change := range got.Changes {
				if change.Name == "c.jpg" {
					t.Fatalf("changes = %+v, want dropped items excluded", got.Changes)
				}
			}
		})
	}
	if len(full.Items) != 3 || full.Items[0].Name != "c.jpg" {
		t.Fatalf("limitSnapshot mutated its input: %+v", full.Items)
	}
}

func TestPrepareCycleSyncsSubsetWithoutTouchingFolder(t *testing.T) {
	t.Parallel()

	artworkDir := t.TempDir()
	for index, name := range []string{"zebra.jpg", "apple.jpg", "mango.jpg"} {
		if err := os.WriteFile(filepath.Join(artworkDir, name), createJPEG(t, 4+index, 4), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := collectionpkg.New(collectionpkg.Config{Root: artworkDir, MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ArtworkDir: artworkDir, MaxArtworkImages: 2}
	manager := &localCollection{cycle: make(chan struct{}, 1), limits: newTransformGate(), cfg: cfg, logger: slog.Default(), loader: fixedSourceLoader{}, store: store}

	result, err := manager.prepareCycle(context.Background())
	if err != nil {
		t.Fatalf("prepareCycle() error = %v, want an over-limit folder to sync", err)
	}
	if len(result.snapshot.Items) != 2 || result.snapshot.Items[0].Name != "apple.jpg" ||
		result.snapshot.Items[1].Name != "mango.jpg" {
		t.Fatalf("synced items = %+v, want apple.jpg and mango.jpg", result.snapshot.Items)
	}
	if len(result.warnings) != 1 || !strings.Contains(result.warnings[0], "3 images") {
		t.Fatalf("warnings = %v, want one over-limit warning", result.warnings)
	}
	if _, err := os.Stat(filepath.Join(artworkDir, "zebra.jpg")); err != nil {
		t.Fatalf("image beyond the limit was removed from the folder: %v", err)
	}
}
