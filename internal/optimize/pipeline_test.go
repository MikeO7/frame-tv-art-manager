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
	"testing"
)

// OptimizeFile runs the production single-file transform with concise test setup.
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (string, bool, error) {
	return optimizeFileWithPolicy(context.Background(), path, filepath.Base(path), false, cfg, logger)
}

func testPass(dir string, cfg Config) *catalogPass {
	return &catalogPass{artworkDir: dir, cfg: cfg, logger: discardLogger()}
}

// runTestPass runs a complete pass over the named files in dir.
func runTestPass(ctx context.Context, dir string, cfg Config, names ...string) (*catalogPass, error) {
	pass := testPass(dir, cfg)
	pass.inputs = make(map[string]StageInput, len(names))
	for _, name := range names {
		pass.inputs[name] = StageInput{}
	}
	return pass, pass.run(ctx)
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

func TestCatalogPassValidationAndErrors(t *testing.T) {
	t.Run("disabled validates and filters corrupt files", func(t *testing.T) {
		dir := t.TempDir()
		writeTestImage(t, filepath.Join(dir, "valid.jpg"), 8, 8)
		if err := os.WriteFile(filepath.Join(dir, "corrupt.jpg"), []byte("broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig()
		cfg.Enabled = false
		pass, err := runTestPass(context.Background(), dir, cfg, "valid.jpg", "corrupt.jpg", "._sidecar.jpg")
		if err != nil || pass.optimized != 0 || len(pass.renames) != 0 {
			t.Fatalf("run() = (%d, %v), renames %v; want no work", pass.optimized, err, pass.renames)
		}
	})

	t.Run("canceled context is returned before work", func(t *testing.T) {
		dir := t.TempDir()
		writeTestImage(t, filepath.Join(dir, "alpha.jpg"), 12, 8)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		pass, err := runTestPass(ctx, dir, DefaultConfig(), "alpha.jpg")
		if pass.optimized != 0 || !errors.Is(err, context.Canceled) || len(pass.renames) != 0 {
			t.Fatalf("run() = (%d, %v), renames %v; want canceled before work", pass.optimized, err, pass.renames)
		}
	})
}

func TestCatalogPassRenamesImage(t *testing.T) {
	dir := t.TempDir()
	const oldName = "landscape.h_abcdef123456.jpg"
	writeTestImage(t, filepath.Join(dir, oldName), 12, 8)
	cfg := DefaultConfig()
	cfg.MaxWidth = 10
	cfg.MaxHeight = 6

	pass, err := runTestPass(context.Background(), dir, cfg, oldName)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if pass.optimized != 1 || len(pass.renames) != 1 {
		t.Fatalf("count=%d renames=%v", pass.optimized, pass.renames)
	}
	if rename := pass.renames[0]; rename.OldName != oldName || rename.NewName == oldName {
		t.Fatalf("unexpected rename: %v", rename)
	}
	if err := ValidateImage(context.Background(), filepath.Join(dir, pass.renames[0].NewName)); err != nil {
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
			cfg := DefaultConfig()
			cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight
			name, err := testPass(dir, cfg).processCollagePair(context.Background(), f1, f2)
			if err != nil {
				t.Fatalf("processCollagePair: %v", err)
			}
			if filepath.Ext(name) != ext {
				t.Fatalf("output extension = %q, want %q", filepath.Ext(name), ext)
			}
			if err := ValidateImage(context.Background(), filepath.Join(dir, name)); err != nil {
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

func TestProcessCollagesUpdatesBatchState(t *testing.T) {
	dir := t.TempDir()
	f1 := "upload_first.h_aaaaaaaaaaaa.jpg"
	f2 := "upload_second.h_bbbbbbbbbbbb.jpg"
	writeTestImage(t, filepath.Join(dir, f1), 4, 8)
	writeTestImage(t, filepath.Join(dir, f2), 4, 8)
	localFiles := map[string]struct{}{f1: {}, f2: {}}
	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight
	pass := testPass(dir, cfg)
	if err := pass.processCollages(context.Background(), localFiles); err != nil {
		t.Fatalf("processCollages: %v", err)
	}
	if pass.optimized != 1 || len(localFiles) != 1 || len(pass.renames) != 2 {
		t.Fatalf("count=%d files=%v renames=%v", pass.optimized, localFiles, pass.renames)
	}
	for name := range localFiles {
		if !strings.HasPrefix(name, "collage-") {
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
	if err != nil || modified || name != "exact.jpg" {
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
	pngName, modified, err := OptimizeFile(pngPath, cfg, discardLogger())
	if err != nil || !modified || filepath.Ext(pngName) != extPNG {
		t.Fatalf("PNG OptimizeFile = (%q, %v, %v)", pngName, modified, err)
	}

	if _, _, err := OptimizeFile(filepath.Join(dir, "missing.jpg"), cfg, discardLogger()); err == nil {
		t.Fatal("missing image did not return an error")
	}
}

func TestOptimizeFileSkipsUnusableFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.jpg"), []byte("not-an-image"), 0o600); err != nil {
		t.Fatalf("write invalid image: %v", err)
	}
	disabled := DefaultConfig()
	disabled.Enabled = false
	for _, test := range []struct {
		name, file string
		cfg        Config
	}{
		{name: "invalid while disabled", file: "invalid.jpg", cfg: disabled},
		{name: "missing while enabled", file: "missing.jpg", cfg: DefaultConfig()},
	} {
		t.Run(test.name, func(t *testing.T) {
			pass := testPass(dir, test.cfg)
			pass.optimizeFile(context.Background(), test.file)
			if pass.optimized != 0 || len(pass.renames) != 0 {
				t.Fatalf("optimizeFile(%s) counted=%d renames=%v", test.file, pass.optimized, pass.renames)
			}
		})
	}
}
