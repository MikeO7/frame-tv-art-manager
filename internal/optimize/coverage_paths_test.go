package optimize

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJPEGTestImage(t *testing.T, path string, width, height int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	defer func() { _ = f.Close() }()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}

	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode image: %v", err)
	}
}

func TestRewriteImage_SeekFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid.jpg")
	writeJPEGTestImage(t, path, 8, 8)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	_ = f.Close()

	_, _, err = rewriteImage(rewriteParams{
		f:        f,
		path:     filepath.Join(t.TempDir(), "renamed.jpg"),
		filename: "renamed.jpg",
		width:    8, height: 8,
		cfg:    DefaultConfig(),
		logger: slog.Default(),
	})
	if err == nil || !strings.Contains(err.Error(), "seek to start") {
		t.Fatalf("rewriteImage() = %v, expected seek failure", err)
	}
}

func TestRewriteImage_DecodeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jpg")
	if err := os.WriteFile(path, []byte("not-an-image"), 0o600); err != nil {
		t.Fatalf("write bad image: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer f.Close()

	_, _, err = rewriteImage(rewriteParams{
		f:        f,
		path:     path,
		filename: "bad.jpg",
		width:    8, height: 8,
		cfg:    DefaultConfig(),
		logger: slog.Default(),
	})
	if err == nil || !strings.Contains(err.Error(), "decode image") {
		t.Fatalf("rewriteImage() = %v, expected decode failure", err)
	}
}

func TestRewriteImage_CreateTempFailure(t *testing.T) {
	validPath := filepath.Join(t.TempDir(), "valid.jpg")
	writeJPEGTestImage(t, validPath, 8, 8)

	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("create blocker: %v", err)
	}

	f, err := os.Open(validPath)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer f.Close()

	_, _, err = rewriteImage(rewriteParams{
		f:        f,
		path:     filepath.Join(blocker, "out.jpg"),
		filename: "out.jpg",
		width:    8, height: 8,
		cfg:    DefaultConfig(),
		logger: slog.Default(),
	})
	if err == nil || !strings.Contains(err.Error(), "create optimized temporary file") {
		t.Fatalf("rewriteImage() = %v, expected temp create failure", err)
	}
}

func TestProcessMarker_DiscardMarker(t *testing.T) {
	buf := []byte{0x00, 0x02}
	gotOrientation, stop, err := processMarker(bytes.NewReader(buf), 0xe0)
	if err != nil {
		t.Fatalf("processMarker() = %v", err)
	}
	if stop {
		t.Fatalf("expected stop=false, got true")
	}
	if gotOrientation != 0 {
		t.Fatalf("expected zero orientation, got %d", gotOrientation)
	}
}

func TestProcessMarker_DiscardIoError(t *testing.T) {
	buf := []byte{0x00, 0x05, 0x01}
	if _, _, err := processMarker(bytes.NewReader(buf), 0xe0); err == nil {
		t.Fatal("expected io error while discarding marker payload")
	}
}

func TestProcessCollagePair_ObserverFailureReturnsNameWithError(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.jpg"), 8, 12)
	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 12

	name, err := processCollagePair(collageJob{
		artworkDir: dir,
		f1:         "upload_a.jpg",
		f2:         "upload_b.jpg",
		cfg:        cfg,
		catalog:    &recordingCatalog{},
		onRename: func(_, _ string) error {
			return errors.New("rename failed")
		},
		logger: discardLogger(),
	})
	if err == nil {
		t.Fatal("expected observer failure error")
	}
	if name == "" {
		t.Fatal("expected collage file name even on observer error")
	}
}

func TestProcessCollages_ObserverErrorIsCollected(t *testing.T) {
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "upload_a.jpg"), 8, 12)
	writeTestImage(t, filepath.Join(dir, "upload_b.jpg"), 8, 12)
	localFiles := map[string]struct{}{
		"upload_a.jpg": {},
		"upload_b.jpg": {},
	}

	observerErr := errors.New("observer failed")
	var count int64
	err := processCollages(collageBatch{
		artworkDir:     dir,
		localFiles:     localFiles,
		cfg:            DefaultConfig(),
		catalog:        &recordingCatalog{},
		onRename:       func(_, _ string) error { return observerErr },
		logger:         discardLogger(),
		optimizedCount: &count,
	})
	if err == nil || !strings.Contains(err.Error(), observerErr.Error()) {
		t.Fatalf("processCollages() = %v", err)
	}

	if count != 1 {
		t.Fatalf("expected exactly one optimized file, got %d", count)
	}
	if _, ok := localFiles["upload_a.jpg"]; ok {
		t.Fatalf("source upload_a.jpg should have been removed from localFiles")
	}
	if _, ok := localFiles["upload_b.jpg"]; ok {
		t.Fatalf("source upload_b.jpg should have been removed from localFiles")
	}
}

func TestProcessCollages_OddPortraitListUnpaired(t *testing.T) {
	dir := t.TempDir()
	writeJPEGTestImage(t, filepath.Join(dir, "upload_a.jpg"), 4, 8)
	writeJPEGTestImage(t, filepath.Join(dir, "upload_b.jpg"), 4, 8)
	writeJPEGTestImage(t, filepath.Join(dir, "upload_c.jpg"), 4, 8)

	localFiles := map[string]struct{}{
		"upload_a.jpg": {},
		"upload_b.jpg": {},
		"upload_c.jpg": {},
	}

	cfg := DefaultConfig()
	cfg.MuseumModeEnabled = true

	err := processCollages(collageBatch{
		artworkDir:     dir,
		localFiles:     localFiles,
		cfg:            cfg,
		catalog:        &recordingCatalog{},
		onRename:       nil,
		logger:         discardLogger(),
		optimizedCount: new(int64),
	})
	if err != nil {
		t.Fatalf("processCollages() = %v", err)
	}
	if len(localFiles) != 1 {
		t.Fatalf("expected one unpaired file to remain, got %d", len(localFiles))
	}
}

func TestDither_UpperClampPath(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5, 1))
	for i := 0; i < 5; i++ {
		src.SetRGBA(i, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	}

	out := dither(src)
	if out == nil {
		t.Fatal("dither returned nil")
	}

	px0 := out.Pix[0]  // x=0 red
	px3 := out.Pix[12] // x=3 red
	px4 := out.Pix[16] // x=4 red
	if px0 == 255 || px3 == 0 || px4 == 0 {
		t.Fatalf("unexpected dither output: px0=%d px3=%d px4=%d", px0, px3, px4)
	}
}

func TestCenterCrop_SmartWideUsesSmartBranch(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x * 2) % 256),
				G: uint8((y * 3) % 256),
				B: 80,
				A: 255,
			})
		}
	}

	cropped := centerCrop(img, 16, 9, true)
	b := cropped.Bounds()
	if b.Dx() != 16 || b.Dy() != 9 {
		t.Fatalf("expected smart crop output 16x9, got %dx%d", b.Dx(), b.Dy())
	}
}

func TestOptimizeFile_DecodeImageConfigFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jpg")
	if err := os.WriteFile(path, []byte("not a jpeg"), 0o600); err != nil {
		t.Fatalf("seed bad image: %v", err)
	}

	cfg := DefaultConfig()
	cfg.MaxWidth = 10
	cfg.MaxHeight = 10
	if _, _, err := OptimizeFile(path, cfg, discardLogger()); err == nil || !strings.Contains(err.Error(), "decode image config") {
		t.Fatalf("OptimizeFile() = %v", err)
	}
}

func TestParseExif_TooShortForCount(t *testing.T) {
	// IFD offset points inside payload, but there are fewer than 2 bytes for IFD count.
	tiff := make([]byte, 9)
	tiff[0], tiff[1] = 'I', 'I'
	tiff[2], tiff[3] = 0x2a, 0x00
	// ifdOffset = 8
	tiff[4], tiff[5], tiff[6], tiff[7] = 0x08, 0x00, 0x00, 0x00

	if _, err := parseExif(tiff); err == nil || !strings.Contains(err.Error(), "tiff too short for IFD count") {
		t.Fatalf("parseExif() = %v", err)
	}
}

func TestFindOrientationTag_BreaksOnTruncatedEntry(t *testing.T) {
	// One orientation entry is claimed but the entry data is truncated.
	tiff := make([]byte, 14)
	tiff[0], tiff[1] = 'I', 'I'
	tiff[2], tiff[3] = 0x2a, 0x00
	tiff[4], tiff[5], tiff[6], tiff[7] = 0x08, 0x00, 0x00, 0x00
	tiff[8], tiff[9] = 1, 0
	tiff[10], tiff[11], tiff[12], tiff[13] = 0, 0, 0, 0

	got, err := parseExif(tiff)
	if err != nil || got != 1 {
		t.Fatalf("parseExif() = (%d, %v)", got, err)
	}
}

func TestReadOrientation_ProcessesMultipleFFFillBytes(t *testing.T) {
	raw := []byte{
		0xff, 0xd8,
		0xff, 0xff, 0xff, 0xff, 0xe0, 0x00, 0x04, 0x00, 0x00,
		0xff, 0xe1, 0x00, 0x08, 'N', 'O', 'T', 'E', 'X', 'I',
		0xff, 0xd9,
	}
	orientation, err := ReadOrientation(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadOrientation() = %v", err)
	}
	if orientation != 1 {
		t.Fatalf("expected default orientation, got %d", orientation)
	}
}

func TestGenerateSaliencyMap_ClampsColorWeight(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	for y := 1; y < 3; y++ {
		for x := 1; x < 3; x++ {
			img.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}

	if c := ciede2000(labColor{l: 0, a: 0, b: 0}, labColor{l: 100, a: 128, b: 128}); c <= 100 {
		t.Fatalf("expected color delta above clamp threshold, got %f", c)
	}

	saliency := generateSaliencyMap(img)
	for _, v := range saliency {
		if v > 1.0 {
			return
		}
	}
	t.Fatal("expected saliency map to include a value greater than 1.0")
}

func TestProcessCollages_IgnoresPortraitProbeErrors(t *testing.T) {
	dir := t.TempDir()
	files := map[string]struct{}{
		"upload_missing.jpg": {},
	}
	if got := collectRawPortraits(dir, files, true); len(got) != 0 {
		t.Fatalf("collectRawPortraits() = %v", got)
	}
}

func TestBlurImage_DownscalesTinySource(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	out := blurImage(img, 16)
	if out.Bounds() != img.Bounds() {
		t.Fatalf("unexpected blur bounds: %v", out.Bounds())
	}
}

func TestClampOffset_Branches(t *testing.T) {
	if got := clampOffset(-3, 4, 10); got != 0 {
		t.Fatalf("expected low clamp to 0, got %d", got)
	}
	if got := clampOffset(9, 4, 10); got != 6 {
		t.Fatalf("expected high clamp to 6, got %d", got)
	}
	if got := clampOffset(4, 3, 10); got != 4 {
		t.Fatalf("expected center offset unchanged, got %d", got)
	}
}

func TestRefineOffset_SearchDisabled(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	if got := refineOffset(img, 5, 18, 18, true); got != 5 {
		t.Fatalf("expected no search adjustment, got %d", got)
	}
}

func TestApplyMuseumMode_ClampsIntensity(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 40, G: 80, B: 120, A: 255})
		}
	}

	zero := applyMuseumMode(img, -1)
	if zero == nil {
		t.Fatal("expected image when intensity is clamped up from negative")
	}
	high := applyMuseumMode(img, 20)
	if high == nil {
		t.Fatal("expected image when intensity is clamped down from high")
	}
	if zero == nil || high == nil {
		t.Fatal("unexpected nil output")
	}
}

func TestApplyMuseumMode_ClampsContrastGammaUpperBound(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			if (x+y)%2 == 0 {
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
			}
		}
	}

	rms := calculateRMSContrast(img)
	if rms <= 58 {
		t.Fatalf("expected high RMS contrast, got %f", rms)
	}

	out := applyMuseumMode(img, 2)
	if out == nil {
		t.Fatal("expected output image")
	}
	if len(out.Pix) != len(img.Pix) {
		t.Fatalf("expected unchanged pixel buffer length, got %d, want %d", len(out.Pix), len(img.Pix))
	}
	changed := false
	for i := range out.Pix {
		if out.Pix[i] != img.Pix[i] {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("expected museum mode to modify at least one pixel")
	}
}

func TestPolishPixel_NoiseClampBranches(t *testing.T) {
	negativeState, negativeNoise, ok := findPolishSeed(func(noise float32) bool {
		return noise < 0
	})
	if !ok {
		t.Fatal("could not find negative polish noise seed")
	}

	state := negativeState
	p0r, p0g, p0b := polishPixel(0, 0, 0, &state)
	if p0r != 0 || p0g != 0 || p0b != 0 {
		t.Fatalf("expected polishPixel low clamp to zero, got r=%d g=%d b=%d (noise=%.6f)", p0r, p0g, p0b, negativeNoise)
	}

	positiveState, positiveNoise, ok := findPolishSeed(func(noise float32) bool {
		return noise > 1
	})
	if !ok {
		t.Fatal("could not find positive polish noise seed")
	}

	state = positiveState
	p1r, p1g, p1b := polishPixel(255, 255, 255, &state)
	if p1r <= 235 || p1g <= 235 || p1b <= 235 || p1r == 255 || p1g == 255 || p1b == 255 {
		t.Fatalf("expected polishPixel high input to remain below output ceiling, got r=%d g=%d b=%d (noise=%.6f)", p1r, p1g, p1b, positiveNoise)
	}
}

func findPolishSeed(predicate func(float32) bool) (uint32, float32, bool) {
	for seed := uint32(0); seed < 1<<20; seed++ {
		state := seed
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		noise := (float32(state)/float32(0xFFFFFFFF) - 0.5) * 5.0
		if predicate(noise) {
			return seed, noise, true
		}
	}
	return 0, 0, false
}
