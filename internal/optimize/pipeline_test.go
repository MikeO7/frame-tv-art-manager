package optimize

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recordingCatalog struct {
	mu      sync.Mutex
	files   map[string]struct{}
	err     error
	renames [][2]string
}

func (c *recordingCatalog) SupportedFiles() (map[string]struct{}, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := make(map[string]struct{}, len(c.files))
	for name := range c.files {
		out[name] = struct{}{}
	}
	return out, nil
}

func (c *recordingCatalog) NoteFileRename(oldName, newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renames = append(c.renames, [2]string{oldName, newName})
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func writeTestImage(t *testing.T, path string, width, height int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	var encodeErr error
	if strings.EqualFold(filepath.Ext(path), extPNG) {
		encodeErr = png.Encode(f, img)
	} else {
		encodeErr = jpeg.Encode(f, img, &jpeg.Options{Quality: 90})
	}
	if encodeErr != nil {
		_ = f.Close()
		t.Fatalf("encode image: %v", encodeErr)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close image: %v", err)
	}
}

func TestOptimizeCatalogValidationAndErrors(t *testing.T) {
	t.Run("catalog error", func(t *testing.T) {
		wantErr := errors.New("catalog unavailable")
		count, err := OptimizeCatalog(context.Background(), t.TempDir(), &recordingCatalog{err: wantErr}, DefaultConfig(), nil, discardLogger())
		if count != 0 || !errors.Is(err, wantErr) {
			t.Fatalf("OptimizeCatalog() = (%d, %v), want (0, %v)", count, err, wantErr)
		}
	})

	t.Run("disabled validates and filters corrupt files", func(t *testing.T) {
		dir := t.TempDir()
		writeTestImage(t, filepath.Join(dir, "valid.jpg"), 8, 8)
		if err := os.WriteFile(filepath.Join(dir, "corrupt.jpg"), []byte("broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		catalog := &recordingCatalog{files: map[string]struct{}{"valid.jpg": {}, "corrupt.jpg": {}, "._sidecar.jpg": {}}}
		cfg := DefaultConfig()
		cfg.Enabled = false
		count, err := OptimizeCatalog(context.Background(), dir, catalog, cfg, nil, discardLogger())
		if err != nil || count != 0 {
			t.Fatalf("OptimizeCatalog() = (%d, %v), want (0, nil)", count, err)
		}
	})

	t.Run("canceled context is returned", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		count, err := OptimizeCatalog(ctx, t.TempDir(), &recordingCatalog{files: map[string]struct{}{}}, DefaultConfig(), nil, discardLogger())
		if count != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("OptimizeCatalog() = (%d, %v), want canceled", count, err)
		}
	})
}

func TestOptimizeCatalogRenamesImage(t *testing.T) {
	dir := t.TempDir()
	const oldName = "landscape.h_abcdef123456.jpg"
	writeTestImage(t, filepath.Join(dir, oldName), 12, 8)
	catalog := &recordingCatalog{files: map[string]struct{}{oldName: {}}}
	cfg := DefaultConfig()
	cfg.MaxWidth = 10
	cfg.MaxHeight = 6
	var callback [][2]string

	count, err := OptimizeCatalog(context.Background(), dir, catalog, cfg, func(oldName, newName string) {
		callback = append(callback, [2]string{oldName, newName})
	}, discardLogger())
	if err != nil {
		t.Fatalf("OptimizeCatalog: %v", err)
	}
	if count != 1 || len(callback) != 1 || len(catalog.renames) != 1 {
		t.Fatalf("count=%d callback=%v catalog=%v", count, callback, catalog.renames)
	}
	if callback[0][0] != oldName || callback[0][1] == oldName {
		t.Fatalf("unexpected callback: %v", callback)
	}
	if err := ValidateImage(filepath.Join(dir, callback[0][1])); err != nil {
		t.Fatalf("renamed output invalid: %v", err)
	}
}

func TestCollagePipelineJPEGAndPNG(t *testing.T) {
	for _, ext := range []string{extJPG, extPNG} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			f1 := "upload_a.h_aaaaaaaaaaaa" + ext
			f2 := "upload_b.h_bbbbbbbbbbbb" + ext
			writeTestImage(t, filepath.Join(dir, f1), 8, 12)
			writeTestImage(t, filepath.Join(dir, f2), 8, 12)
			catalog := &recordingCatalog{}
			cfg := DefaultConfig()
			cfg.MaxWidth = 3840
			cfg.MaxHeight = 2160
			name, err := processCollagePair(collageJob{
				artworkDir: dir,
				f1:         f1,
				f2:         f2,
				cfg:        cfg,
				catalog:    catalog,
				logger:     discardLogger(),
			})
			if err != nil {
				t.Fatalf("processCollagePair: %v", err)
			}
			if filepath.Ext(name) != ext {
				t.Fatalf("output extension = %q, want %q", filepath.Ext(name), ext)
			}
			if err := ValidateImage(filepath.Join(dir, name)); err != nil {
				t.Fatalf("collage invalid: %v", err)
			}
			for _, source := range []string{f1, f2} {
				if _, err := os.Stat(filepath.Join(dir, source)); !os.IsNotExist(err) {
					t.Fatalf("source %s was not removed", source)
				}
			}
		})
	}
}

func TestCollectRawPortraitsAndWorkerBounds(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_portrait.jpg"), 4, 8)
	writeTestImage(t, filepath.Join(dir, "remote_portrait.jpg"), 4, 8)
	writeTestImage(t, filepath.Join(dir, "upload_landscape.jpg"), 8, 4)
	files := map[string]struct{}{
		"upload_portrait.jpg": {}, "remote_portrait.jpg": {}, "upload_landscape.jpg": {},
		"upload_1x1_opt.h_hash.jpg": {}, "._sidecar.jpg": {}, "missing.jpg": {},
	}
	got := collectRawPortraits(dir, files, false)
	if len(got) != 1 || got[0] != "upload_portrait.jpg" {
		t.Fatalf("collectRawPortraits(upload only) = %v", got)
	}
	if _, exists := files["._sidecar.jpg"]; exists {
		t.Fatal("AppleDouble sidecar was not pruned")
	}
	got = collectRawPortraits(dir, files, true)
	if len(got) != 2 {
		t.Fatalf("collectRawPortraits(all) = %v, want two portraits", got)
	}
	if clampWorkers(0) != minOptimizeWorkers || clampWorkers(8) != 8 || clampWorkers(100) != maxOptimizeWorkers {
		t.Fatal("clampWorkers did not enforce worker bounds")
	}
}

func TestProcessCollagesUpdatesBatchState(t *testing.T) {
	dir := t.TempDir()
	f1 := "upload_first.h_aaaaaaaaaaaa.jpg"
	f2 := "upload_second.h_bbbbbbbbbbbb.jpg"
	writeTestImage(t, filepath.Join(dir, f1), 4, 8)
	writeTestImage(t, filepath.Join(dir, f2), 4, 8)
	localFiles := map[string]struct{}{f1: {}, f2: {}}
	catalog := &recordingCatalog{}
	var count int64
	var callbacks [][2]string
	processCollages(collageBatch{
		artworkDir: dir,
		localFiles: localFiles,
		cfg:        DefaultConfig(),
		catalog:    catalog,
		onRename: func(oldName, newName string) {
			callbacks = append(callbacks, [2]string{oldName, newName})
		},
		logger:         discardLogger(),
		optimizedCount: &count,
	})
	if count != 1 || len(localFiles) != 1 || len(callbacks) != 2 || len(catalog.renames) != 2 {
		t.Fatalf("count=%d files=%v callbacks=%v renames=%v", count, localFiles, callbacks, catalog.renames)
	}
	for name := range localFiles {
		if !strings.HasPrefix(name, "collage_") {
			t.Fatalf("unexpected collage name %q", name)
		}
	}
}

func TestOptimizeFileFastAndUnsupportedPaths(t *testing.T) {
	dir := t.TempDir()
	jpegPath := filepath.Join(dir, "exact.jpg")
	writeTestImage(t, jpegPath, 8, 6)
	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 6
	name, modified, err := OptimizeFile(jpegPath, cfg, discardLogger())
	if err != nil || !modified || name == "exact.jpg" {
		t.Fatalf("exact OptimizeFile = (%q, %v, %v)", name, modified, err)
	}
	if fastName, fastModified, fastErr := OptimizeFile(filepath.Join(dir, name), cfg, discardLogger()); fastErr != nil || fastModified || fastName != name {
		t.Fatalf("fast OptimizeFile = (%q, %v, %v)", fastName, fastModified, fastErr)
	}

	cfg.Enabled = false
	if _, modified, err := OptimizeFile(filepath.Join(dir, name), cfg, discardLogger()); err != nil || modified {
		t.Fatalf("disabled OptimizeFile = (%v, %v)", modified, err)
	}

	pngPath := filepath.Join(dir, "image.png")
	writeTestImage(t, pngPath, 4, 4)
	cfg.Enabled = true
	if _, modified, err := OptimizeFile(pngPath, cfg, discardLogger()); err != nil || modified {
		t.Fatalf("PNG OptimizeFile = (%v, %v)", modified, err)
	}

	if _, _, err := OptimizeFile(filepath.Join(dir, "missing.jpg"), cfg, discardLogger()); err == nil {
		t.Fatal("missing image did not return an error")
	}
}
