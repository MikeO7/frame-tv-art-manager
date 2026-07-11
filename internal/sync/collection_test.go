package sync

import (
	"context"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

func TestLocalCollectionPrepareOwnsCompleteCycle(t *testing.T) {
	t.Parallel()

	artworkDir := t.TempDir()
	file, err := os.Create(filepath.Join(artworkDir, "source.jpg"))
	if err != nil {
		t.Fatalf("create source artwork: %v", err)
	}
	if err := jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 12, 8)), nil); err != nil {
		_ = file.Close()
		t.Fatalf("encode source artwork: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close source artwork: %v", err)
	}

	cfg := &config.Config{
		ArtworkDir: artworkDir, OptimizeEnabled: true,
		OptimizeMaxWidth: 10, OptimizeMaxHeight: 6, OptimizeJPEGQuality: 90,
	}
	catalog := sources.NewArtworkCatalog(artworkDir, slog.Default())
	collection := &localCollection{
		cfg: cfg, logger: slog.Default(), loader: fixedSourceLoader{downloaded: 1}, catalog: catalog,
	}

	got, err := collection.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare collection: %v", err)
	}
	if got.downloaded != 1 || got.optimized != 1 || len(got.files) != 1 || len(got.warnings) != 0 {
		t.Fatalf("prepared collection = %+v", got)
	}
}

func TestLocalCollectionPrepareRetainsSourceFailureAsWarning(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("provider offline")
	artworkDir := t.TempDir()
	collection := &localCollection{
		cfg: &config.Config{ArtworkDir: artworkDir}, logger: slog.Default(),
		loader:  fixedSourceLoader{err: wantErr},
		catalog: sources.NewArtworkCatalog(artworkDir, slog.Default()),
	}

	got, err := collection.prepare(context.Background())
	if err != nil {
		t.Fatalf("prepare collection: %v", err)
	}
	if len(got.warnings) != 1 {
		t.Fatalf("warnings = %v, want source failure", got.warnings)
	}
}

type fixedSourceLoader struct {
	downloaded int
	err        error
}

func (l fixedSourceLoader) Sync(context.Context) (int, error) {
	return l.downloaded, l.err
}
