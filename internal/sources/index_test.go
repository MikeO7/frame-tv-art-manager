package sources

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestArtworkIndex_SupportedFiles(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"a.jpg", "jpeg-content-a"},
		{"b.JPEG", "jpeg-content-b"},
		{"c.png", "png-content-c"},
		{"d.txt", "text-content-d"},
		{"e.gif", "gif-content-e"},
	} {
		if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	idx := NewArtworkIndex(dir, slog.Default())
	files, err := idx.SupportedFiles()
	if err != nil {
		t.Fatalf("SupportedFiles: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 supported images, got %d: %v", len(files), files)
	}

	for name := range files {
		ext := filepath.Ext(name)
		if ext != extJPG && ext != ".JPEG" && ext != extPNG {
			t.Errorf("unexpected supported file %q", name)
		}
	}
}

func TestArtworkIndex_SupportedFiles_Missing(t *testing.T) {
	idx := NewArtworkIndex("/nonexistent/path/xyz", slog.Default())
	_, err := idx.SupportedFiles()
	if err == nil {
		t.Error("expected error for missing directory")
	}
}
