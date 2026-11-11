package optimize

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestIsPortraitFile_SeekFailureFromNonSeekableSource(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait.jpg")
	writeJPEGToFIFO(t, portraitPath, 2, 4)

	_, err := isPortraitFile(context.Background(), portraitPath)
	if err == nil || !strings.Contains(err.Error(), "seek") {
		t.Fatalf("isPortraitFile() = %v, expected seek error", err)
	}
}

func TestLoadAndRotateImage_SeekFailureFromNonSeekableSource(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait.jpg")
	writeJPEGToFIFO(t, portraitPath, 2, 4)

	_, err := loadAndRotateImageWithConfig(context.Background(), portraitPath, DefaultConfig(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), "seek") {
		t.Fatalf("loadAndRotateImageWithConfig() = %v, expected seek error", err)
	}
}

func TestIsPortraitFile_DecodeConfigError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-image.txt")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := isPortraitFile(context.Background(), path); err == nil {
		t.Fatal("expected decode config error")
	}
}

func TestProcessCollagePair_SecondImageLoadFailure(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12
	_, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.jpg", "missing.jpg")
	if err == nil || !strings.Contains(err.Error(), "missing.jpg") {
		t.Fatalf("expected second-image load failure, got %v", err)
	}
}

func TestProcessCollagePairRejectsCombinedWorkingSetBeforePixelDecode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePNGHeaderOnly(t, filepath.Join(dir, "one.png"), 8000, 5000)
	writePNGHeaderOnly(t, filepath.Join(dir, "two.png"), 8000, 5000)
	cfg := DefaultConfig()
	cfg.MaxWorkingBytes = 128 * 1024 * 1024
	_, err := testPass(dir, cfg).processCollagePair(context.Background(), "one.png", "two.png")
	if err == nil || !strings.Contains(err.Error(), "working set") {
		t.Fatalf("processCollagePair() error = %v, want pre-decode working-set rejection", err)
	}
}

func TestProcessCollagePair_CreateTempFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions, so the read-only directory cannot force the failure")
	}
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
	_, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.jpg", "upload_b.jpg")
	if err == nil || !strings.Contains(err.Error(), "create collage output") {
		t.Fatalf("expected create temp failure, got %v", err)
	}
}

func TestProcessCollagePairExtendsDigestForOccupiedName(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.h_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.h_b.jpg"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12

	collageName, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.h_a.jpg", "upload_b.h_b.jpg")
	if err != nil {
		t.Fatalf("seed collage: %v", err)
	}
	collisionPath := filepath.Join(dir, collageName)
	original, err := os.ReadFile(collisionPath)
	if err != nil {
		t.Fatalf("read seeded collage: %v", err)
	}
	writeTestImage(t, filepath.Join(dir, "upload_a.h_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.h_b.jpg"), 8, 12)

	alternateName, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.h_a.jpg", "upload_b.h_b.jpg")
	if err != nil || alternateName == collageName {
		t.Fatalf("alternate collage = %q, error %v", alternateName, err)
	}
	if got, err := os.ReadFile(collisionPath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("collision target was replaced: %d bytes, %v", len(got), err)
	}
	if _, err := os.Stat(filepath.Join(dir, alternateName)); err != nil {
		t.Fatalf("alternate collage does not exist: %v", err)
	}
	for _, name := range []string{"upload_a.h_a.jpg", "upload_b.h_b.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("collage source %q still exists: %v", name, err)
		}
	}
}

func TestCollageUtilsPortraitDetectionAndRotation(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait.jpg")
	landscapePath := filepath.Join(dir, "landscape.jpg")
	writePNGForCollageTests(t, portraitPath, 2, 4)
	writePNGForCollageTests(t, landscapePath, 4, 2)

	isPortrait, err := isPortraitFile(context.Background(), portraitPath)
	if err != nil || !isPortrait {
		t.Fatalf("isPortraitFile(portrait) = (%v, %v), want (true, nil)", isPortrait, err)
	}

	isPortrait, err = isPortraitFile(context.Background(), landscapePath)
	if err != nil || isPortrait {
		t.Fatalf("isPortraitFile(landscape) = (%v, %v), want (false, nil)", isPortrait, err)
	}

	img, err := loadAndRotateImageWithConfig(context.Background(), portraitPath, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatalf("loadAndRotateImageWithConfig(portrait) = %v", err)
	}
	if got := img.Bounds().Dy(); got != 4 {
		t.Fatalf("loadAndRotateImage width x height = %d x %d", img.Bounds().Dx(), got)
	}
}

func TestCollageOutputExtensionIsOrderIndependentAndPreservesPNG(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		first, second string
		want          string
	}{
		{name: "JPEG pair", first: "one.jpg", second: "two.jpeg", want: extJPG},
		{name: "PNG first", first: "one.png", second: "two.jpg", want: extPNG},
		{name: "PNG second", first: "one.jpg", second: "two.PNG", want: extPNG},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := collageOutputExtension(test.first, test.second); got != test.want {
				t.Fatalf("collageOutputExtension(%q, %q) = %q, want %q", test.first, test.second, got, test.want)
			}
		})
	}
}

func TestCreateCollageSharpensEachPanelAtItsOwnScale(t *testing.T) {
	t.Parallel()
	columns := func(values ...uint8) *image.RGBA {
		input := image.NewRGBA(image.Rect(0, 0, len(values), len(values)))
		for x, value := range values {
			fillRGBA(input, image.Rect(x, 0, x+1, len(values)), color.RGBA{R: value, G: value, B: value, A: 255})
		}
		return input
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 16, 8
	cfg.SmartCropEnabled = false
	cfg.SharpenAmount, cfg.SharpenThreshold = 1, 0
	cfg.PadStyle = padStyleBlur
	logger := slog.New(slog.DiscardHandler)
	// The left source is shown 1:1 and is sharpened; the right is enlarged
	// twice, which is past the point where sharpening adds anything.
	native, enlarged := columns(60, 62, 70, 100, 150, 172, 180, 180), columns(60, 100, 150, 180)
	env := newRenderEnv(cfg, logger)
	output := renderCollage(context.Background(), native, enlarged, env)
	plainLeft, _ := centerCropConfigured(context.Background(), native, 8, 8, env)
	plainRight, _ := centerCropConfigured(context.Background(), enlarged, 8, 8, env)
	sharpened := false
	for y := 1; y < 7; y++ {
		for x := 2; x < 6; x++ {
			sharpened = sharpened || output.RGBAAt(x, y) != plainLeft.RGBAAt(x, y)
			if got, want := output.RGBAAt(8+x, y), plainRight.RGBAAt(x, y); got != want {
				t.Fatalf("enlarged right panel pixel (%d,%d) = %v, want unsharpened %v", x, y, got, want)
			}
		}
	}
	if !sharpened {
		t.Fatal("the 1:1 left panel was not sharpened")
	}
}

func TestProcessCollagePairError(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.MaxWidth = 12
	cfg.MaxHeight = 12
	_, err := testPass(dir, cfg).processCollagePair(context.Background(), "missing.jpg", "also-missing.jpg")
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

	_, err := loadAndRotateImageWithConfig(context.Background(), path, DefaultConfig(), discardLogger())
	if err == nil || (!strings.Contains(err.Error(), "unknown format") && !strings.Contains(err.Error(), "image: ")) {
		t.Fatalf("loadAndRotateImageWithConfig() = %v", err)
	}

	_, err = loadAndRotateImageWithConfig(context.Background(), filepath.Join(dir, "missing.jpg"), DefaultConfig(), discardLogger())
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

	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight
	pass := testPass(dir, cfg)
	if err := pass.processCollages(context.Background(), localFiles); err != nil {
		t.Fatalf("processCollages() = %v", err)
	}
	if pass.optimized != 1 {
		t.Fatalf("expected one collage to be created, got %d", pass.optimized)
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

func TestCollectRawPortraitsKeepsReadablePortraits(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "upload_ok.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_derived.jpg"), 4, 8)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_landscape.jpg"), 8, 4)

	localFiles := map[string]struct{}{
		"upload_ok.jpg":        {},
		"upload_landscape.jpg": {},
		"upload_derived.jpg":   {},
		"upload_ignore.opt":    {},
		"upload_missing.jpg":   {},
	}
	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	pass := testPass(dir, cfg)
	pass.inputs = map[string]StageInput{"upload_derived.jpg": {Derivative: DerivativeCollage}}
	if got := pass.collectRawPortraits(context.Background(), localFiles); !slices.Equal(got, []string{"upload_ok.jpg"}) {
		t.Fatalf("collectRawPortraits(collage) = %#v", got)
	}
	cfg.PortraitMode = "crop"
	if got := testPass(dir, cfg).collectRawPortraits(context.Background(), localFiles); len(got) != 0 {
		t.Fatalf("collectRawPortraits(crop) = %#v, want none", got)
	}
}

func TestCatalogPassSkipsAppleDoubleFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]struct{}{"._a.jpg": {}, "._b.jpg": {}, "upload_ok.jpg": {}}
	for name := range files {
		writePNGForCollageTests(t, filepath.Join(dir, name), 4, 8)
	}
	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight
	pass, err := runTestPass(context.Background(), dir, cfg, slices.Collect(maps.Keys(files))...)
	if err != nil || pass.optimized != 0 || len(pass.renames) != 0 {
		t.Fatalf("run() = (%d, %v), renames %v; want AppleDouble files and the unpaired portrait untouched",
			pass.optimized, err, pass.renames)
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s was not left in place: %v", name, err)
		}
	}
}

func TestProcessCollagePairRecordsBothSourceRenames(t *testing.T) {
	dir := t.TempDir()
	writePNGForCollageTests(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	writePNGForCollageTests(t, filepath.Join(dir, "upload_b.jpg"), 8, 12)

	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12

	pass := testPass(dir, cfg)
	name, err := pass.processCollagePair(context.Background(), "upload_a.jpg", "upload_b.jpg")
	if err != nil {
		t.Fatalf("processCollagePair() = %v", err)
	}
	want := []rename{{OldName: "upload_a.jpg", NewName: name}, {OldName: "upload_b.jpg", NewName: name}}
	if !slices.Equal(pass.renames, want) {
		t.Fatalf("renames = %v, want %v", pass.renames, want)
	}
}

func TestIsPortraitFile_UsesExifOrientation(t *testing.T) {
	dir := t.TempDir()
	portraitPath := filepath.Join(dir, "portrait_oriented.jpg")
	writeJPEGWithExifOrientation(t, portraitPath, 4, 2, 8)

	isPortrait, err := isPortraitFile(context.Background(), portraitPath)
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

	got, err := loadAndRotateImageWithConfig(context.Background(), portraitPath, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatalf("loadAndRotateImageWithConfig() = %v", err)
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
	name, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.raw", "upload_b.raw")
	if err != nil {
		t.Fatalf("processCollagePair() = %v", err)
	}
	if filepath.Ext(name) != extJPG {
		t.Fatalf("expected fallback extension %s, got %s", extJPG, filepath.Ext(name))
	}
	if err := validateImage(context.Background(), filepath.Join(dir, name)); err != nil {
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

	name, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.h_xxx.jpg", "upload_b.h_yyy.jpg")
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

	name, err := testPass(dir, cfg).processCollagePair(context.Background(), "upload_a.h_xxx.png", "upload_b.h_yyy.png")
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
	cfg.PortraitMode = portraitModeCollage
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight

	localFiles := map[string]struct{}{
		"remote_1.jpg":       {},
		"upload_2.h_abc.jpg": {},
		"remote_3.jpg":       {},
		"remote_4.jpg":       {},
		"junk.txt":           {},
	}
	pass := testPass(dir, cfg)
	if err := pass.processCollages(context.Background(), localFiles); err != nil {
		t.Fatalf("processCollages() = %v", err)
	}
	if pass.optimized != 2 {
		t.Fatalf("expected two collages from four portrait-like files, got %d", pass.optimized)
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

func writePNGHeaderOnly(t *testing.T, path string, width, height uint32) {
	t.Helper()
	var encoded bytes.Buffer
	encoded.Write(pngSignature[:])
	writeChunk := func(name string, payload []byte) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
		encoded.Write(length[:])
		encoded.WriteString(name)
		encoded.Write(payload)
		var checksum [4]byte
		binary.BigEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(append([]byte(name), payload...)))
		encoded.Write(checksum[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8
	ihdr[9] = 2
	writeChunk("IHDR", ihdr)
	writeChunk("IEND", nil)
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
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

func TestProcessCollagesLeavesAnOversizedPairAndKeepsGoing(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.jpg"), 8, 12)
	localFiles := map[string]struct{}{
		"upload_a.jpg": {},
		"upload_b.jpg": {},
	}

	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight
	// Each portrait fits the budget alone; the two together do not.
	cfg.MaxWorkingBytes = estimatedWorkingBytes(workingInputOfSize(8, 12, 4), cfg)
	pass := testPass(dir, cfg)
	// A pair too large to render together is not a failed pass: it would
	// otherwise stop every other file from being optimized, every cycle.
	if err := pass.processCollages(context.Background(), localFiles); err != nil {
		t.Fatalf("processCollages() = %v, want the oversized pair skipped", err)
	}
	if pass.optimized != 0 || len(pass.renames) != 0 || len(localFiles) != 2 {
		t.Fatalf("count=%d renames=%v files=%v, want the pair left in place", pass.optimized, pass.renames, localFiles)
	}
	for name := range localFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("portrait %s was not left in place: %v", name, err)
		}
	}
}

// smallCollageWidth and smallCollageHeight keep collage pipeline tests fast;
// the 3840x2160 output contract is covered by the collage geometry tests.
const (
	smallCollageWidth  = 96
	smallCollageHeight = 54
)

func TestPairPortraitsMatchesColorAndShape(t *testing.T) {
	t.Parallel()
	features := []portraitFeature{
		{name: "a-blue-dark.jpg", color: oklab{l: 0.3, a: -0.02, b: -0.1}, aspect: 0.67},
		{name: "b-warm-light.jpg", color: oklab{l: 0.8, a: 0.03, b: 0.08}, aspect: 0.75},
		{name: "c-blue-dark.jpg", color: oklab{l: 0.32, a: -0.02, b: -0.09}, aspect: 0.66},
		{name: "d-warm-light.jpg", color: oklab{l: 0.78, a: 0.03, b: 0.07}, aspect: 0.75},
		{name: "e-odd.jpg", color: oklab{l: 0.55}, aspect: 0.4},
	}
	pairs, leftover := pairPortraits(features)
	want := [][2]string{{"a-blue-dark.jpg", "c-blue-dark.jpg"}, {"b-warm-light.jpg", "d-warm-light.jpg"}}
	if !slices.Equal(pairs, want) {
		t.Fatalf("pairs = %v, want %v", pairs, want)
	}
	if leftover != "e-odd.jpg" {
		t.Fatalf("leftover = %q, want the least compatible portrait", leftover)
	}
	if pairs, leftover := pairPortraits(features[:2]); len(pairs) != 1 || leftover != "" {
		t.Fatalf("two portraits: pairs %v leftover %q", pairs, leftover)
	}
	if pairs, leftover := pairPortraits(nil); len(pairs) != 0 || leftover != "" {
		t.Fatalf("no portraits: pairs %v leftover %q", pairs, leftover)
	}
}

// Taking the closest pair first strands the two outer portraits with each
// other; trading partners gives two good pairs instead of one perfect and one
// poor.
func TestPairPortraitsMinimizesTotalMismatch(t *testing.T) {
	t.Parallel()
	lightness := []float64{0.20, 0.44, 0.50, 0.74}
	features := make([]portraitFeature, 0, len(lightness)+1)
	for index, value := range lightness {
		features = append(features, portraitFeature{name: fmt.Sprintf("%d.jpg", index), color: oklab{l: value}, aspect: 0.67})
	}
	if greedy, _ := greedyPairs(len(features), func(a, b int) float64 { return pairCost(features[a], features[b]) }); !slices.Equal(
		greedy, [][2]int{{1, 2}, {0, 3}},
	) {
		t.Fatalf("greedy pairs = %v; the case no longer exercises a partner trade", greedy)
	}
	pairs, leftover := pairPortraits(features)
	if want := [][2]string{{"0.jpg", "1.jpg"}, {"2.jpg", "3.jpg"}}; !slices.Equal(pairs, want) || leftover != "" {
		t.Fatalf("pairs = %v leftover %q, want %v", pairs, leftover, want)
	}

	// With an odd count the leftover is chosen for the lowest total too: the
	// outlier is left out though taking the best pairs first would pair it.
	features = append(features, portraitFeature{name: "outlier.jpg", color: oklab{l: 0.95, a: 0.3}, aspect: 0.4})
	pairs, leftover = pairPortraits(features)
	if want := [][2]string{{"0.jpg", "1.jpg"}, {"2.jpg", "3.jpg"}}; !slices.Equal(pairs, want) || leftover != "outlier.jpg" {
		t.Fatalf("odd count: pairs = %v leftover %q, want %v and the outlier", pairs, leftover, want)
	}
}

// A backlog too large for exhaustive search still ends at least as good as
// taking the best pairs first, and pairs every portrait exactly once.
func TestPairPortraitsImprovesLargeBacklog(t *testing.T) {
	t.Parallel()
	features := make([]portraitFeature, 2*exactPairingLimit+1)
	state := uint32(7)
	for index := range features {
		state = state*1664525 + 1013904223
		features[index] = portraitFeature{
			name:   fmt.Sprintf("%02d.jpg", index),
			color:  oklab{l: float64(state>>8&0xff) / 255, a: float64(state>>16&0xff)/510 - 0.25},
			aspect: 0.5 + float64(state>>24)/1020,
		}
	}
	byName := make(map[string]portraitFeature, len(features))
	for _, feature := range features {
		byName[feature.name] = feature
	}
	total := func(pairs [][2]string) float64 {
		sum := 0.0
		for _, pair := range pairs {
			sum += pairCost(byName[pair[0]], byName[pair[1]])
		}
		return sum
	}
	greedy, _ := greedyPairs(len(features), func(a, b int) float64 { return pairCost(features[a], features[b]) })
	greedyTotal := 0.0
	for _, pair := range greedy {
		greedyTotal += pairCost(features[pair[0]], features[pair[1]])
	}
	pairs, leftover := pairPortraits(features)
	if got := total(pairs); got >= greedyTotal {
		t.Fatalf("total mismatch %.4f is not below best-first pairing's %.4f", got, greedyTotal)
	}
	seen := map[string]int{leftover: 1}
	for _, pair := range pairs {
		seen[pair[0]]++
		seen[pair[1]]++
	}
	if len(pairs) != exactPairingLimit || len(seen) != len(features) {
		t.Fatalf("%d pairs covering %d of %d portraits", len(pairs), len(seen), len(features))
	}
}
