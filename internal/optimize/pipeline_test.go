package optimize

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type mockCatalog struct {
	files     map[string]struct{}
	err       error
	renamed   map[string]string
	renamedMu sync.Mutex
}

func (m *mockCatalog) SupportedFiles() (map[string]struct{}, error) {
	if m.err != nil {
		return nil, m.err
	}
	res := make(map[string]struct{})
	for k := range m.files {
		res[k] = struct{}{}
	}
	return res, nil
}

func (m *mockCatalog) NoteFileRename(oldName, newName string) {
	m.renamedMu.Lock()
	defer m.renamedMu.Unlock()
	if m.renamed == nil {
		m.renamed = make(map[string]string)
	}
	m.renamed[oldName] = newName
}

func TestOptimizeCatalog(t *testing.T) {
	tempDir := t.TempDir()

	err := os.WriteFile(filepath.Join(tempDir, "test.jpg"), []byte("notanimage"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	t.Run("SupportedFiles error", func(t *testing.T) {
		cat := &mockCatalog{err: errors.New("catalog error")}
		count, err := OptimizeCatalog(context.Background(), tempDir, cat, Config{}, nil, logger)
		if count != 0 {
			t.Errorf("expected count 0, got %d", count)
		}
		if err == nil || err.Error() != "catalog error" {
			t.Errorf("expected catalog error, got %v", err)
		}
	})

	t.Run("Context cancelled", func(t *testing.T) {
		cat := &mockCatalog{
			files: map[string]struct{}{
				"test.jpg": {},
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // immediately cancel

		count, err := OptimizeCatalog(ctx, tempDir, cat, Config{}, nil, logger)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context canceled, got %v", err)
		}
		if count != 0 {
			t.Errorf("expected count 0, got %d", count)
		}
	})

	t.Run("Opt disabled - validation failure", func(t *testing.T) {
		cat := &mockCatalog{
			files: map[string]struct{}{
				"test.jpg": {},
			},
		}
		count, err := OptimizeCatalog(context.Background(), tempDir, cat, Config{Enabled: false}, nil, logger)
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if count != 0 {
			t.Errorf("expected count 0, got %d", count)
		}
	})
}
func TestHandleSingleOptimization(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	t.Run("Opt disabled - valid image", func(t *testing.T) {
		tempDir := t.TempDir()
		// create a tiny valid JPEG
		validJPG := []byte{
			0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01,
			0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xff, 0xdb, 0x00, 0x43,
			0x00, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
			0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
			0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
			0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
			0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
			0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0xff, 0xc0, 0x00,
			0x0b, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xff, 0xc4,
			0x00, 0x14, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0xff, 0xc4, 0x00, 0x14,
			0x10, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xda, 0x00, 0x08, 0x01, 0x01,
			0x00, 0x00, 0x3f, 0x00, 0x37, 0xff, 0xd9,
		}

		err := os.WriteFile(filepath.Join(tempDir, "valid.jpg"), validJPG, 0644)
		if err != nil {
			t.Fatal(err)
		}

		o := &optContext{
			artworkDir: tempDir,
			localFiles: map[string]struct{}{"valid.jpg": {}},
			cfg:        Config{Enabled: false},
			logger:     logger,
		}

		mod, ok := handleSingleOptimization("valid.jpg", o)
		if mod {
			t.Errorf("expected not modified")
		}
		if !ok {
			t.Errorf("expected ok")
		}
	})

	t.Run("Opt enabled - unsupported image error", func(t *testing.T) {
		tempDir := t.TempDir()
		err := os.WriteFile(filepath.Join(tempDir, "test.jpg"), []byte("notanimage"), 0644)
		if err != nil {
			t.Fatal(err)
		}

		o := &optContext{
			artworkDir: tempDir,
			localFiles: map[string]struct{}{"test.jpg": {}},
			cfg:        Config{Enabled: true, MaxWidth: 3840, MaxHeight: 2160},
			logger:     logger,
		}

		mod, ok := handleSingleOptimization("test.jpg", o)
		if mod {
			t.Errorf("expected not modified")
		}
		if ok {
			t.Errorf("expected not ok")
		}
	})

	t.Run("Opt enabled - fast path", func(t *testing.T) {
		tempDir := t.TempDir()
		// create a file that matches checkFastPath pattern
		err := os.WriteFile(filepath.Join(tempDir, "image_3840x2160_opt.h_a1b2c3d4.jpg"), []byte("notanimage"), 0644)
		if err != nil {
			t.Fatal(err)
		}

		o := &optContext{
			artworkDir: tempDir,
			localFiles: map[string]struct{}{"image_3840x2160_opt.h_a1b2c3d4.jpg": {}},
			cfg:        Config{Enabled: true, MaxWidth: 3840, MaxHeight: 2160},
			logger:     logger,
		}

		mod, ok := handleSingleOptimization("image_3840x2160_opt.h_a1b2c3d4.jpg", o)
		if mod {
			t.Errorf("expected not modified")
		}
		if !ok {
			t.Errorf("expected ok")
		}
	})
}
func TestEnqueueOptimizeJobs(t *testing.T) {
	files := map[string]struct{}{
		"test1.jpg":   {},
		"._test2.jpg": {}, // AppleDouble file, should be pruned
		"test3.jpg":   {},
	}

	jobs := enqueueOptimizeJobs(files)

	count := 0
	for range jobs {
		count++
	}

	if count != 2 {
		t.Errorf("expected 2 jobs, got %d", count)
	}
	if _, ok := files["._test2.jpg"]; ok {
		t.Errorf("expected AppleDouble file to be pruned from map")
	}
}

func TestClampWorkers(t *testing.T) {
	if clampWorkers(2) != minOptimizeWorkers {
		t.Errorf("expected clamp 2 -> minOptimizeWorkers")
	}
	if clampWorkers(32) != maxOptimizeWorkers {
		t.Errorf("expected clamp 32 -> maxOptimizeWorkers")
	}
	if clampWorkers(8) != 8 {
		t.Errorf("expected clamp 8 -> 8")
	}
}
func TestRecordRename(t *testing.T) {
	o := &optContext{
		localFiles: map[string]struct{}{"old.jpg": {}},
	}

	o.recordRename("old.jpg", "new.jpg")

	if _, ok := o.localFiles["old.jpg"]; ok {
		t.Errorf("expected old.jpg to be removed")
	}
	if _, ok := o.localFiles["new.jpg"]; !ok {
		t.Errorf("expected new.jpg to be added")
	}
}

func TestRecordDelete(t *testing.T) {
	o := &optContext{
		localFiles: map[string]struct{}{"test.jpg": {}},
	}

	o.recordDelete("test.jpg")

	if _, ok := o.localFiles["test.jpg"]; ok {
		t.Errorf("expected test.jpg to be removed")
	}
}

func TestRunOptimizeWorkers_CancelInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	jobs := make(chan string, 100)
	for i := 0; i < 100; i++ {
		jobs <- "test.jpg"
	}
	close(jobs)

	o := &optContext{
		logger:     slog.New(slog.NewTextHandler(os.Stdout, nil)),
		cfg:        Config{Enabled: false},
		artworkDir: t.TempDir(),
	}
	_ = os.WriteFile(filepath.Join(o.artworkDir, "test.jpg"), []byte("data"), 0644)

	var count int64

	// Cancel before workers run to ensure at least one worker gets blocked in the select
	cancel()
	runOptimizeWorkers(ctx, jobs, o, &count)
}

func TestRunOptimizeWorkers_ModifiedCount(t *testing.T) {
	ctx := context.Background()
	jobs := make(chan string, 1)
	jobs <- "tomodify.jpg"
	close(jobs)

	tempDir := t.TempDir()

	validJPG := []byte{
		0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xff, 0xdb, 0x00, 0x43,
		0x00, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0xff, 0xc0, 0x00,
		0x0b, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xff, 0xc4,
		0x00, 0x14, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0xff, 0xc4, 0x00, 0x14,
		0x10, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xda, 0x00, 0x08, 0x01, 0x01,
		0x00, 0x00, 0x3f, 0x00, 0x37, 0xff, 0xd9,
	}
	_ = os.WriteFile(filepath.Join(tempDir, "tomodify.jpg"), validJPG, 0644)

	o := &optContext{
		logger:     slog.New(slog.NewTextHandler(os.Stdout, nil)),
		cfg:        Config{Enabled: true, MaxWidth: 3840, MaxHeight: 2160},
		artworkDir: tempDir,
		localFiles: map[string]struct{}{"tomodify.jpg": {}},
		catalog: &mockCatalog{
			files: map[string]struct{}{"tomodify.jpg": {}},
		},
	}

	var count int64
	runOptimizeWorkers(ctx, jobs, o, &count)

	if count != 1 {
		t.Errorf("expected count to be 1, got %d", count)
	}
}

func TestRunOptimizeWorkers_Default(t *testing.T) {
	// 2. Second test: Uncancelled context to hit default:
	ctx := context.Background()

	jobs := make(chan string, 1)
	jobs <- "test.jpg"
	close(jobs)

	o := &optContext{
		logger:     slog.New(slog.NewTextHandler(os.Stdout, nil)),
		cfg:        Config{Enabled: false},
		artworkDir: t.TempDir(),
	}

	_ = os.WriteFile(filepath.Join(o.artworkDir, "test.jpg"), []byte("data"), 0644)

	var count int64
	runOptimizeWorkers(ctx, jobs, o, &count)
}

func TestHandleSingleOptimization_Modified(t *testing.T) {
	tempDir := t.TempDir()

	// Write a valid JPG so OptimizeFile succeeds but modifies it
	validJPG := []byte{
		0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xff, 0xdb, 0x00, 0x43,
		0x00, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0xff, 0xc0, 0x00,
		0x0b, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xff, 0xc4,
		0x00, 0x14, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0xff, 0xc4, 0x00, 0x14,
		0x10, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xda, 0x00, 0x08, 0x01, 0x01,
		0x00, 0x00, 0x3f, 0x00, 0x37, 0xff, 0xd9,
	}
	err := os.WriteFile(filepath.Join(tempDir, "tomodify.jpg"), validJPG, 0644)
	if err != nil {
		t.Fatal(err)
	}

	renameCalled := false
	o := &optContext{
		artworkDir: tempDir,
		localFiles: map[string]struct{}{"tomodify.jpg": {}},
		cfg:        Config{Enabled: true, MaxWidth: 3840, MaxHeight: 2160},
		logger:     slog.New(slog.NewTextHandler(os.Stdout, nil)),
		onRename: func(oldName, newName string) {
			renameCalled = true
		},
		catalog: &mockCatalog{
			files: map[string]struct{}{"tomodify.jpg": {}},
		},
	}

	mod, ok := handleSingleOptimization("tomodify.jpg", o)

	if !ok {
		t.Errorf("expected ok")
	}
	if !mod {
		t.Errorf("expected mod to be true")
	}
	if !renameCalled {
		t.Errorf("expected onRename to be called")
	}
}
