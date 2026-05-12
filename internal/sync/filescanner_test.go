package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanArtworkDir(t *testing.T) {
	dir := t.TempDir()

	// Create a set of files with various extensions.
	for _, f := range []string{"a.jpg", "b.JPEG", "c.png", "d.txt", "e.gif"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil { //nolint:gosec // Test file
			t.Fatal(err)
		}
	}

	// Create a subdirectory - it should be ignored.
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create an image inside the subdirectory - it should also be ignored.
	if err := os.WriteFile(filepath.Join(dir, "subdir", "sub.jpg"), []byte("x"), 0644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	files, err := ScanArtworkDir(dir)
	if err != nil {
		t.Fatalf("ScanArtworkDir: %v", err)
	}

	// Only .jpg, .jpeg (case-insensitive), .png in the root directory should be included.
	if _, ok := files["a.jpg"]; !ok {
		t.Error("expected a.jpg")
	}
	if _, ok := files["b.JPEG"]; !ok {
		t.Error("expected b.JPEG")
	}
	if _, ok := files["c.png"]; !ok {
		t.Error("expected c.png")
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(files), files)
	}

	if _, ok := files["d.txt"]; ok {
		t.Error("d.txt should be excluded")
	}
	if _, ok := files["e.gif"]; ok {
		t.Error("e.gif should be excluded")
	}
	if _, ok := files["subdir"]; ok {
		t.Error("subdir should be excluded")
	}
	if _, ok := files["sub.jpg"]; ok {
		t.Error("sub.jpg (in subdirectory) should be excluded")
	}
}

func TestScanArtworkDir_Empty(t *testing.T) {
	dir := t.TempDir()
	files, err := ScanArtworkDir(dir)
	if err != nil {
		t.Fatalf("ScanArtworkDir on empty dir: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestScanArtworkDir_Missing(t *testing.T) {
	_, err := ScanArtworkDir("/nonexistent/path/xyz")
	if err == nil {
		t.Error("expected error for missing directory")
	}
}

func TestFileTypeFromExt(t *testing.T) {
	tests := []struct{ file, want string }{
		{"photo.jpg", extJPG},
		{"photo.JPEG", extJPG},
		{"photo.jpeg", extJPG},
		{"photo.png", extPNG},
		{"photo.PNG", extPNG},
		{"photo", extJPG}, // Default to jpg
		{"photo.txt", extJPG},
	}
	for _, tc := range tests {
		got := FileTypeFromExt(tc.file)
		if got != tc.want {
			t.Errorf("FileTypeFromExt(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}
