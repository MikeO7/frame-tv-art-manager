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

func TestArtworkCatalogInvalidateCache(t *testing.T) {
	dir := t.TempDir()
	idx := NewArtworkCatalog(dir, slog.Default())
	idx.catalog["old.jpg"] = struct{}{}
	idx.cacheValid = true

	idx.InvalidateCache()
	if idx.cacheValid || len(idx.catalog) != 0 {
		t.Fatalf("InvalidateCache() left cache valid: %+v", idx.catalog)
	}
}

func TestArtworkCatalogMaxReached(t *testing.T) {
	idx := NewArtworkCatalog(t.TempDir(), slog.Default())
	idx.catalog["a.jpg"] = struct{}{}
	idx.catalog["b.jpg"] = struct{}{}
	if !idx.MaxReached(2) || idx.MaxReached(0) {
		t.Fatal("MaxReached() did not enforce positive complete-catalog limit")
	}
}

func TestArtworkCatalog_UnsupportedPath(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := NewArtworkCatalog(tmpFile, slog.Default())
	_, err := idx.SupportedFiles()
	if err == nil {
		t.Error("expected error when artwork path is a file")
	}
}

func TestArtworkCatalog_RebuildDoesNotMutateArtworkNames(t *testing.T) {
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

	idx := NewArtworkCatalog(dir, slog.Default())
	files, err := idx.SupportedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 supported file after rebuild, got %d", len(files))
	}
	if _, ok := files["plainphoto.jpg"]; !ok {
		t.Fatalf("inventory changed operator filename: %v", files)
	}
	if _, err := os.Stat(filepath.Join(dir, "plainphoto.jpg")); err != nil {
		t.Fatalf("inventory mutated artwork path: %v", err)
	}
}

func TestArtworkCatalog_RebuildRepeatedScanIsStable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("jpeg-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	idx := NewArtworkCatalog(dir, slog.Default())
	if _, err := idx.SupportedFiles(); err != nil {
		t.Fatal(err)
	}
	before := len(idx.hashIndex)
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("repeat rebuild: %v", err)
	}
	if len(idx.hashIndex) != before {
		t.Error("expected repeated inventory to preserve hash index")
	}
}

func TestArtworkCatalog_LookupAndRegisterPrefix(t *testing.T) {
	idx := NewArtworkCatalog(t.TempDir(), slog.Default())
	idx.prefixMap["nasa__apod"] = "001__nasa__apod.h_abc.jpg"

	got, ok := idx.LookupPrefix("001__nasa__apod")
	if !ok || got != "001__nasa__apod.h_abc.jpg" {
		t.Errorf("LookupPrefix = %q %v", got, ok)
	}

	got, ok = idx.LookupPrefix("nasa__apod")
	if !ok || got != "001__nasa__apod.h_abc.jpg" {
		t.Errorf("LookupPrefix with stripped index = %q %v", got, ok)
	}
}

func TestArtworkCatalog_FileHash(t *testing.T) {
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

func TestArtworkCatalog_ProcessFile_VerifiesBytesDespiteEmbeddedHash(t *testing.T) {
	dir := t.TempDir()
	idx := NewArtworkCatalog(dir, slog.Default())
	name := "001__photo.h_abc123def456.jpg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("verified bytes"), 0o644); err != nil {
		t.Fatalf("write artwork: %v", err)
	}
	wantHash, err := fileHash(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("hash artwork: %v", err)
	}
	entry := idx.processFile(name)
	if entry.hash != wantHash {
		t.Errorf("hash = %q, want verified %q", entry.hash, wantHash)
	}
	if entry.err != nil {
		t.Errorf("unexpected error: %v", entry.err)
	}
}

func TestArtworkCatalog_RebuildPropagatesHashFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.jpg")
	if err := os.WriteFile(path, []byte("artwork"), 0o600); err != nil {
		t.Fatalf("write artwork: %v", err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("remove read permissions: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("filesystem permits owner reads despite mode 000")
	}

	catalog := NewArtworkCatalog(dir, slog.Default())
	if err := catalog.Rebuild(); err == nil {
		t.Fatal("expected unreadable artwork to fail the complete inventory")
	}
	if len(catalog.catalog) != 0 || len(catalog.hashIndex) != 0 {
		t.Fatalf("failed rebuild retained partial inventory: catalog=%v hashes=%v", catalog.catalog, catalog.hashIndex)
	}
}

func TestArtworkCatalog_RebuildNeverDeletesCanonicalDuplicateSurvivor(t *testing.T) {
	dir := t.TempDir()
	data := []byte("identical artwork bytes")
	for _, name := range []string{"photo.jpg", "photo.h_stale.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	catalog := NewArtworkCatalog(dir, slog.Default())
	if err := catalog.Rebuild(); err != nil {
		t.Fatalf("rebuild catalog: %v", err)
	}
	files, err := catalog.SupportedFiles()
	if err != nil {
		t.Fatalf("SupportedFiles() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("supported files = %v, want one survivor", files)
	}
	for name := range files {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatalf("read survivor %s: %v", name, readErr)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("survivor %s has wrong bytes", name)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read artwork directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("inventory deleted duplicate artwork: %v", entries)
	}
}
