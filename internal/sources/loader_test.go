package sources

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoader_NoSourcesFile(t *testing.T) {
	l := NewLoader("", t.TempDir(), "", "", testLogger())
	n, err := l.Sync()
	if err != nil {
		t.Errorf("expected no error when sources file is empty, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 downloads, got %d", n)
	}
}

func TestLoader_MissingSourcesFile(t *testing.T) {
	l := NewLoader("/nonexistent/sources.txt", t.TempDir(), "", "", testLogger())
	n, err := l.Sync()
	if err != nil {
		t.Errorf("missing file should not error, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 downloads for missing file, got %d", n)
	}
}

func TestLoader_DownloadsImage(t *testing.T) {
	// Serve a fake JPEG.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-jpeg-data"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	srcFile := filepath.Join(dir, "sources.txt")
	if err := os.WriteFile(srcFile, []byte(srv.URL+"/photo.jpg\n"), 0644); err != nil { //nolint:gosec // Test file
		t.Fatal(err)
	}

	l := NewLoader(srcFile, dir, "", "", testLogger())
	n, err := l.Sync()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 download, got %d", n)
	}

	// Second sync should be idempotent — 0 new downloads.
	n2, err := l.Sync()
	if err != nil {
		t.Errorf("unexpected error on second sync: %v", err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 downloads on second sync (already exists), got %d", n2)
	}
}

func TestLoader_SkipsComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("data"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	srcFile := filepath.Join(dir, "sources.txt")
	content := "# this is a comment\n\n" + srv.URL + "/photo.jpg\n# another comment\n"
	if err := os.WriteFile(srcFile, []byte(content), 0644); err != nil { //nolint:gosec // Test file
		t.Fatal(err)
	}

	l := NewLoader(srcFile, dir, "", "", testLogger())
	n, err := l.Sync()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 download (comment lines skipped), got %d", n)
	}
}

func TestLoader_HandlesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	srcFile := filepath.Join(dir, "sources.txt")
	if err := os.WriteFile(srcFile, []byte(srv.URL+"/missing.jpg\n"), 0644); err != nil { //nolint:gosec // Test file
		t.Fatal(err)
	}

	l := NewLoader(srcFile, dir, "", "", testLogger())
	n, err := l.Sync()
	// Error is logged and skipped — sync returns 0 downloads, no error.
	if err != nil {
		t.Errorf("HTTP error should be logged, not returned: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 successful downloads on 404, got %d", n)
	}
}

func TestURLToFilename_Deterministic(t *testing.T) {
	l := &Loader{}
	url := "https://example.com/photo.jpg?w=3840"
	f1 := l.urlToFilename(url)
	f2 := l.urlToFilename(url)
	if f1 != f2 {
		t.Errorf("urlToFilename should be deterministic: %q != %q", f1, f2)
	}
}

func TestURLToFilename_DifferentURLs(t *testing.T) {
	l := &Loader{}
	f1 := l.urlToFilename("https://example.com/a.jpg")
	f2 := l.urlToFilename("https://example.com/b.jpg")
	if f1 == f2 {
		t.Error("different URLs should produce different filenames")
	}
}
