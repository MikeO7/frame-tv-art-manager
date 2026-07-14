package optimize

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func TestIsPortraitFile_SeekFailureFromNonSeekableSource(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait.jpg")
	writeJPEGToFIFO(t, portraitPath, 2, 4)

	_, err := isPortraitFile(portraitPath)
	if err == nil || !strings.Contains(err.Error(), "seek") {
		t.Fatalf("isPortraitFile() = %v, expected seek error", err)
	}
}

func TestLoadAndRotateImage_SeekFailureFromNonSeekableSource(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait.jpg")
	writeJPEGToFIFO(t, portraitPath, 2, 4)

	_, err := loadAndRotateImage(portraitPath)
	if err == nil || !strings.Contains(err.Error(), "seek") {
		t.Fatalf("loadAndRotateImage() = %v, expected seek error", err)
	}
}

func TestIsPortraitFile_DecodeConfigError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-image.txt")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := isPortraitFile(path); err == nil {
		t.Fatal("expected decode config error")
	}
}

func TestProcessCollagePair_SecondImageLoadFailure(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12
	_, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.jpg",
		f2:         "missing.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err == nil || !strings.Contains(err.Error(), "load/rotate missing.jpg") {
		t.Fatalf("expected second-image load failure, got %v", err)
	}
}

func TestProcessCollagePair_CreateTempFailure(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.jpg"), 8, 12)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod readonly source dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o700)
	})

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12
	_, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.jpg",
		f2:         "upload_b.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err == nil || !strings.Contains(err.Error(), "create collage output") {
		t.Fatalf("expected create temp failure, got %v", err)
	}
}

func TestProcessCollagePair_CommitCollisionIsError(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.h_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.h_b.jpg"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12

	stem1, hash1, ext1 := artwork.ExtractStemAndHash("upload_a.h_a.jpg")
	stem2, hash2, _ := artwork.ExtractStemAndHash("upload_b.h_b.jpg")
	ext := strings.ToLower(ext1)
	if ext != extJPG && ext != extJPEG && ext != extPNG {
		ext = extJPG
	}
	collageName := artwork.BuildOptimizedName("collage_"+stem1+"_"+stem2, cfg.MaxWidth, cfg.MaxHeight, hash1+"_"+hash2, ext)
	collisionPath := filepath.Join(dir, collageName)
	if err := os.WriteFile(collisionPath, []byte("operator artwork"), 0o644); err != nil {
		t.Fatalf("create output collision target: %v", err)
	}

	_, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.h_a.jpg",
		f2:         "upload_b.h_b.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err == nil || !strings.Contains(err.Error(), "commit collage") {
		t.Fatalf("expected commit collision error, got %v", err)
	}
	if got, err := os.ReadFile(collisionPath); err != nil || string(got) != "operator artwork" {
		t.Fatalf("collision target was replaced: %q, %v", got, err)
	}
	for _, name := range []string{"upload_a.h_a.jpg", "upload_b.h_b.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("collage source %q was removed after collision: %v", name, err)
		}
	}
}

func TestCollageUtilsPortraitDetectionAndRotation(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait.jpg")
	landscapePath := filepath.Join(dir, "landscape.jpg")
	writePNGForCollageTests(t, portraitPath, 2, 4)
	writePNGForCollageTests(t, landscapePath, 4, 2)

	isPortrait, err := isPortraitFile(portraitPath)
	if err != nil || !isPortrait {
		t.Fatalf("isPortraitFile(portrait) = (%v, %v), want (true, nil)", isPortrait, err)
	}

	isPortrait, err = isPortraitFile(landscapePath)
	if err != nil || isPortrait {
		t.Fatalf("isPortraitFile(landscape) = (%v, %v), want (false, nil)", isPortrait, err)
	}

	img, err := loadAndRotateImage(portraitPath)
	if err != nil {
		t.Fatalf("loadAndRotateImage(portrait) = %v", err)
	}
	if got := img.Bounds().Dy(); got != 4 {
		t.Fatalf("loadAndRotateImage width x height = %d x %d", img.Bounds().Dx(), got)
	}
}

func TestProcessCollagePairError(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.MaxWidth = 12
	cfg.MaxHeight = 12
	_, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "missing.jpg",
		f2:         "also-missing.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err == nil {
		t.Fatal("expected processCollagePair error for missing files")
	}
}

func TestLoadAndRotateImage_Errors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notimage.jpg")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	_, err := loadAndRotateImage(path)
	if err == nil || (!strings.Contains(err.Error(), "unknown format") && !strings.Contains(err.Error(), "image: ")) {
		t.Fatalf("loadAndRotateImage() = %v", err)
	}

	_, err = loadAndRotateImage(filepath.Join(dir, "missing.jpg"))
	if err == nil {
		t.Fatal("expected open error for missing file")
	}
}

func TestProcessCollagesOddUploadPortraitSkipsOne(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "upload_1.h_aaa.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_2.h_bbb.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_3.h_ccc.jpg"), 4, 8)

	localFiles := map[string]struct{}{
		"upload_1.h_aaa.jpg": {},
		"upload_2.h_bbb.jpg": {},
		"upload_3.h_ccc.jpg": {},
	}

	var count int64
	if err := processCollages(collageBatch{
		artworkDir:     dir,
		localFiles:     localFiles,
		cfg:            DefaultConfig(),
		catalog:        &recordingCatalog{},
		onRename:       nil,
		logger:         discardLogger(),
		optimizedCount: &count,
	}); err != nil {
		t.Fatalf("processCollages() = %v", err)
	}
	if atomic.LoadInt64(&count) != 1 {
		t.Fatalf("expected one collage to be created, got %d", atomic.LoadInt64(&count))
	}
	for name := range localFiles {
		if len(name) >= 6 && name[:6] == "upload" {
			t.Fatalf("expected upload sources to be removed, still present: %q", name)
		}
	}
	if len(localFiles) != 1 {
		t.Fatalf("expected one collaged output, got %d files", len(localFiles))
	}
}

func TestCollectRawPortraitsFiltersAppleDoubleFiles(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "._ignore.png"), 2, 2)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_ok.jpg"), 4, 8)

	localFiles := map[string]struct{}{
		"._ignore.png":      {},
		"upload_ok.jpg":     {},
		"upload_ignore.opt": {},
	}

	got := collectRawPortraits(dir, localFiles, true)
	if len(got) != 1 {
		t.Fatalf("collectRawPortraits() = %#v", got)
	}
	found := false
	for _, name := range got {
		if name == "upload_ok.jpg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("collectRawPortraits() = %#v", got)
	}
	if _, ok := localFiles["._ignore.png"]; ok {
		t.Fatalf("expected AppleDouble file to be removed")
	}
}

func TestProcessCollagePair_OnRenameErrorReturnedAsWrap(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_b.jpg"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12

	_, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.jpg",
		f2:         "upload_b.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		onRename: func(_, _ string) error {
			return errors.New("rename hook failed")
		},
		logger: discardLogger(),
	})
	if err == nil || !strings.Contains(err.Error(), "rename hook failed") {
		t.Fatalf("expected wrapped rename hook error, got %v", err)
	}
}

func TestIsPortraitFile_UsesExifOrientation(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait_oriented.jpg")
	writeJPEGWithExifOrientation(t, portraitPath, 4, 2, 8)

	isPortrait, err := isPortraitFile(portraitPath)
	if err != nil {
		t.Fatalf("isPortraitFile() = %v", err)
	}
	if !isPortrait {
		t.Fatalf("expected orientation to flip dimensions and report portrait")
	}
}

func TestLoadAndRotateImage_RespectsExifOrientation(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait_oriented_for_load.jpg")
	writeJPEGWithExifOrientation(t, portraitPath, 4, 2, 6)

	got, err := loadAndRotateImage(portraitPath)
	if err != nil {
		t.Fatalf("loadAndRotateImage() = %v", err)
	}
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 4 {
		t.Fatalf("expected rotated portrait bounds 2x4, got %dx%d", got.Bounds().Dx(), got.Bounds().Dy())
	}
}

func TestProcessCollagePair_FallbacksUnknownExtensionToJPEG(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.raw"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.raw"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12
	name, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.raw",
		f2:         "upload_b.raw",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("processCollagePair() = %v", err)
	}
	if filepath.Ext(name) != extJPG {
		t.Fatalf("expected fallback extension %s, got %s", extJPG, filepath.Ext(name))
	}
	if err := ValidateImage(filepath.Join(dir, name)); err != nil {
		t.Fatalf("output image invalid: %v", err)
	}
}

func TestProcessCollagePair_MuseumModeBranch(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.h_xxx.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.h_yyy.jpg"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12
	cfg.MuseumModeEnabled = true
	cfg.MuseumModeIntensity = 2

	name, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.h_xxx.jpg",
		f2:         "upload_b.h_yyy.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("processCollagePair() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("expected collage output file %q to exist", name)
	}
}

func TestProcessCollagePair_PngInputs(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "upload_a.h_xxx.png"), 8, 12)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_b.h_yyy.png"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12

	name, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.h_xxx.png",
		f2:         "upload_b.h_yyy.png",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("processCollagePair() = %v", err)
	}
	if filepath.Ext(name) != extPNG {
		t.Fatalf("expected png collage output, got %q", filepath.Ext(name))
	}

	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("expected output collage file %q: %v", name, err)
	}
}

func TestProcessCollages_WithCollageModeIncludesNonUploadEntries(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "remote_1.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_2.h_abc.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "remote_3.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "remote_4.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "junk.txt"), 4, 8)

	cfg := DefaultConfig()
	cfg.PortraitMode = "collage"

	localFiles := map[string]struct{}{
		"remote_1.jpg":       {},
		"upload_2.h_abc.jpg": {},
		"remote_3.jpg":       {},
		"remote_4.jpg":       {},
		"junk.txt":           {},
	}
	var count int64
	if err := processCollages(collageBatch{
		artworkDir:     dir,
		localFiles:     localFiles,
		cfg:            cfg,
		catalog:        &recordingCatalog{},
		onRename:       nil,
		logger:         discardLogger(),
		optimizedCount: &count,
	}); err != nil {
		t.Fatalf("processCollages() = %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two collages from four portrait-like files, got %d", count)
	}
}

func writePNGForCollageTests(t *testing.T, path string, width, height int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	defer f.Close()

	img := imageFromPattern(width, height)
	if err := pngEncode(f, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}
}

func writeJPEGToFIFO(t *testing.T, path string, width, height int) {
	t.Helper()

	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.SetRGBA(x, y, color.RGBA{R: uint8(16 * x), G: uint8(32 * y), B: 80, A: 255})
		}
	}
	var payload bytes.Buffer
	if err := jpeg.Encode(&payload, imageData, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode fifo payload: %v", err)
	}

	reader, err := os.OpenFile(path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		t.Fatalf("open fifo reader: %v", err)
	}
	writer, err := os.OpenFile(path, syscall.O_WRONLY, 0o600)
	if err != nil {
		_ = reader.Close()
		t.Fatalf("open fifo writer: %v", err)
	}
	if _, err := io.Copy(writer, &payload); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		t.Fatalf("write fifo payload: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
}

func pngEncode(file *os.File, img image.Image) error {
	return png.Encode(file, img)
}

func imageFromPattern(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(16 * x),
				G: uint8(32 * y),
				B: 80,
				A: 255,
			})
		}
	}
	return img
}
