package optimize

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type mockCatalog struct {
	files map[string]struct{}
	err   error
	mu    sync.Mutex
}

func (m *mockCatalog) SupportedFiles() (map[string]struct{}, error) {
	if m.err != nil {
		return nil, m.err
	}
	res := make(map[string]struct{})
	m.mu.Lock()
	for k := range m.files {
		res[k] = struct{}{}
	}
	m.mu.Unlock()
	return res, nil
}

func (m *mockCatalog) NoteFileRename(oldName, newName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, oldName)
	m.files[newName] = struct{}{}
}

func createDummyImage(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOptimizeCatalog(t *testing.T) {
	tmpDir := t.TempDir()

	validFile := "valid.jpg"
	corruptFile := "corrupt.jpg"
	renameFile := "rename.jpg"
	ignoredFile := "._ignored.jpg"

	createDummyImage(t, filepath.Join(tmpDir, validFile), 100, 100)
	createDummyImage(t, filepath.Join(tmpDir, renameFile), 200, 200)

	err := os.WriteFile(filepath.Join(tmpDir, corruptFile), []byte("not an image"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	cat := &mockCatalog{
		files: map[string]struct{}{
			validFile:   {},
			corruptFile: {},
			renameFile:  {},
			ignoredFile: {},
		},
	}

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.MaxWidth = 150
	cfg.MaxHeight = 150

	renames := make(map[string]string)
	var mu sync.Mutex
	onRename := func(oldName, newName string) {
		mu.Lock()
		renames[oldName] = newName
		mu.Unlock()
	}

	logger := slog.Default()
	ctx := context.Background()

	count, err := OptimizeCatalog(ctx, tmpDir, cat, cfg, onRename, logger)
	if err != nil {
		t.Fatalf("OptimizeCatalog failed: %v", err)
	}

	if count == 0 {
		t.Error("Expected at least one file to be optimized")
	}

	mu.Lock()
	if _, ok := renames[renameFile]; !ok {
		t.Errorf("Expected %s to be renamed", renameFile)
	}
	mu.Unlock()

	// Check disabled scenario
	cat2 := &mockCatalog{
		files: map[string]struct{}{
			validFile:   {},
			corruptFile: {},
		},
	}
	cfg.Enabled = false
	count2, err := OptimizeCatalog(ctx, tmpDir, cat2, cfg, onRename, logger)
	if err != nil {
		t.Fatalf("OptimizeCatalog failed: %v", err)
	}
	if count2 != 0 {
		t.Errorf("Expected 0 files optimized when disabled, got %d", count2)
	}
}

func TestOptimizeCatalog_CatalogError(t *testing.T) {
	cat := &mockCatalog{err: errors.New("catalog error")}
	_, err := OptimizeCatalog(context.Background(), "", cat, DefaultConfig(), nil, slog.Default())
	if err == nil {
		t.Error("Expected error when catalog fails")
	}
}

func TestOptimizeCatalog_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cat := &mockCatalog{
		files: map[string]struct{}{
			"test1.jpg": {},
			"test2.jpg": {},
		},
	}

	_, err := OptimizeCatalog(ctx, t.TempDir(), cat, DefaultConfig(), nil, slog.Default())
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context canceled error, got %v", err)
	}
}
