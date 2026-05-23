package sources

import (
	"bytes"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestArtworkIndex_InvalidateCacheAndRename(t *testing.T) {
	dir := t.TempDir()
	idx := NewArtworkIndex(dir, slog.Default())

	idx.catalog["old.jpg"] = struct{}{}
	idx.prefixMap["identity"] = "old.jpg"
	idx.hashIndex["abc"] = "old.jpg"

	idx.InvalidateCache()

	idx.NoteFileRename("old.jpg", "new.jpg")
	if _, ok := idx.catalog["old.jpg"]; ok {
		t.Error("old name should be removed from catalog")
	}
	if _, ok := idx.catalog["new.jpg"]; !ok {
		t.Error("new name should be in catalog")
	}
	if idx.prefixMap["identity"] != "new.jpg" {
		t.Errorf("prefix map = %q", idx.prefixMap["identity"])
	}
	if idx.hashIndex["abc"] != "new.jpg" {
		t.Errorf("hash index = %q", idx.hashIndex["abc"])
	}
}

func TestArtworkIndex_RegisterHashAndMaxReached(t *testing.T) {
	idx := NewArtworkIndex(t.TempDir(), slog.Default())

	if existing, dup := idx.RegisterHash("hash1", "a.jpg"); dup || existing != "" {
		t.Errorf("first register should not be duplicate: %q %v", existing, dup)
	}
	if existing, dup := idx.RegisterHash("hash1", "b.jpg"); !dup || existing != "a.jpg" {
		t.Errorf("second register should be duplicate: %q %v", existing, dup)
	}

	idx.MarkVisited("a.jpg")
	idx.MarkVisited("b.jpg")
	if !idx.MaxReached(2) {
		t.Error("expected MaxReached true at limit")
	}
	if idx.MaxReached(0) {
		t.Error("expected MaxReached false when limit is 0")
	}
}

func TestArtworkIndex_SetHash(t *testing.T) {
	idx := NewArtworkIndex(t.TempDir(), slog.Default())
	idx.SetHash("hash", "file.jpg")
	if idx.hashIndex["hash"] != "file.jpg" {
		t.Errorf("SetHash failed: %q", idx.hashIndex["hash"])
	}
}

func TestArtworkIndex_UnsupportedPath(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := NewArtworkIndex(tmpFile, slog.Default())
	_, err := idx.SupportedFiles()
	if err == nil {
		t.Error("expected error when artwork path is a file")
	}
}

func TestArtworkIndex_UnusedManagedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"001__nasa__apod.h_abc.jpg", "002__direct__x.h_def.jpg", "manual.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	idx := NewArtworkIndex(dir, slog.Default())
	idx.MarkVisited("001__nasa__apod.h_abc.jpg")

	unused := idx.UnusedManagedFiles()
	if len(unused) != 1 || unused[0] != "002__direct__x.h_def.jpg" {
		t.Errorf("unused managed = %v", unused)
	}
}

func TestArtworkIndex_RebuildMigratesHashName(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	// Avoid trailing __segment that ParseIdentity would treat as a hash suffix.
	if err := os.WriteFile(filepath.Join(dir, "plainphoto.jpg"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	idx := NewArtworkIndex(dir, slog.Default())
	files, err := idx.SupportedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 supported file after rebuild, got %d", len(files))
	}
	for name := range files {
		if name == "plainphoto.jpg" {
			t.Errorf("expected migration to hash-based name, still have %q", name)
		}
	}
}

func TestArtworkIndex_RebuildCacheHit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("jpeg-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	idx := NewArtworkIndex(dir, slog.Default())
	if _, err := idx.SupportedFiles(); err != nil {
		t.Fatal(err)
	}
	before := len(idx.hashIndex)
	idx.Rebuild()
	if len(idx.hashIndex) != before {
		t.Error("expected cache hit to preserve hash index")
	}
}

func TestArtworkIndex_LookupAndRegisterPrefix(t *testing.T) {
	idx := NewArtworkIndex(t.TempDir(), slog.Default())
	idx.RegisterPrefix("001__nasa__apod", "001__nasa__apod.h_abc.jpg")

	got, ok := idx.LookupPrefix("001__nasa__apod")
	if !ok || got != "001__nasa__apod.h_abc.jpg" {
		t.Errorf("LookupPrefix = %q %v", got, ok)
	}

	got, ok = idx.LookupPrefix("nasa__apod")
	if !ok || got != "001__nasa__apod.h_abc.jpg" {
		t.Errorf("LookupPrefix with stripped index = %q %v", got, ok)
	}
}

func TestFileHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := fileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Errorf("expected sha256 hex length 64, got %d", len(hash))
	}
}

func TestProcessFile_WithEmbeddedHash(t *testing.T) {
	dir := t.TempDir()
	idx := NewArtworkIndex(dir, slog.Default())
	entry := idx.processFile("001__photo.h_abc123def456.jpg")
	if entry.hash != "abc123def456" {
		t.Errorf("hash = %q", entry.hash)
	}
	if entry.err != nil {
		t.Errorf("unexpected error: %v", entry.err)
	}
}
