package optimize

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func TestTransformKeyTracksCurrentImageAlgorithmRevision(t *testing.T) {
	t.Parallel()
	if transformRevision != "frame-image-v7" {
		t.Fatalf("transformRevision = %q, want frame-image-v7", transformRevision)
	}
	if got := TransformKey(DefaultConfig()); len(got) != 64 {
		t.Fatalf("TransformKey() length = %d, want SHA-256 hex digest", len(got))
	}
}

func TestOptimizeFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.jpg")

	// Create a test image (200x200)
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, img, nil)
	_ = f.Close()

	cfg := DefaultConfig()
	cfg.MaxWidth = 100
	cfg.MaxHeight = 100
	cfg.SmartCropEnabled = true

	newName, mod, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile failed: %v", err)
	}

	if !mod {
		t.Error("expected modified to be true")
	}

	if filepath.Ext(newName) != extJPG || newName == "test.jpg" {
		t.Errorf("expected content-addressed JPEG filename, got %s", newName)
	}

	// Check if file still exists and is valid
	if err := ValidateImage(context.Background(), filepath.Join(tmpDir, newName)); err != nil {
		t.Errorf("optimized image is invalid: %v", err)
	}
}

func TestOptimizeFile_MuseumMode(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "museum.jpg")

	// Create a test image
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, img, nil)
	_ = f.Close()

	cfg := DefaultConfig()
	cfg.MaxWidth = 100
	cfg.MaxHeight = 100
	cfg.MuseumModeEnabled = true
	cfg.MuseumModeIntensity = 5

	newName, mod, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile museum mode failed: %v", err)
	}
	if !mod {
		t.Error("expected modified to be true in museum mode")
	}
	if filepath.Ext(newName) != extJPG || newName == "museum.jpg" {
		t.Errorf("expected content-addressed JPEG filename, got %s", newName)
	}
}

func TestValidateImageRejectsHeaderValidTruncatedJPEG(t *testing.T) {
	t.Parallel()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 32, 32)), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	var truncated []byte
	for cut := len(data) - 1; cut > len(data)/2; cut-- {
		candidate := data[:cut]
		if _, err := jpeg.DecodeConfig(bytes.NewReader(candidate)); err == nil {
			if _, err := jpeg.Decode(bytes.NewReader(candidate)); err != nil {
				truncated = append([]byte(nil), candidate...)
				break
			}
		}
	}
	if truncated == nil {
		t.Fatal("could not construct header-valid truncated JPEG")
	}
	path := filepath.Join(t.TempDir(), "truncated.jpg")
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateImage(context.Background(), path); err == nil {
		t.Fatal("ValidateImage() accepted a truncated pixel stream")
	}
}

func TestOptimizeFileColorProfilePolicy(t *testing.T) {
	t.Parallel()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 8, 6)), nil); err != nil {
		t.Fatal(err)
	}
	payload := []byte("ICC_PROFILE\x00\x01\x01")
	segment := append([]byte{0xff, 0xe2, 0x00, byte(len(payload) + 2)}, payload...)
	tagged := append(append(append([]byte(nil), encoded.Bytes()[:2]...), segment...), encoded.Bytes()[2:]...)

	path := filepath.Join(t.TempDir(), "tagged.jpg")
	if err := os.WriteFile(path, tagged, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 8, 6
	cfg.ColorProfilePolicy = "reject-embedded"
	if _, _, err := OptimizeFile(path, cfg, slog.Default()); err == nil || !strings.Contains(err.Error(), "JPEG ICC") {
		t.Fatalf("OptimizeFile(reject embedded profile) error = %v", err)
	}
}

func TestOptimizeFileMuseumMode(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "museum_dither.jpg")

	// Create a test image with a smooth gradient to make noise visible
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, img, &jpeg.Options{Quality: 100})
	_ = f.Close()

	// Run with museum mode ON
	cfgMuseum := DefaultConfig()
	cfgMuseum.MaxWidth = 100
	cfgMuseum.MaxHeight = 100
	cfgMuseum.MuseumModeEnabled = true
	cfgMuseum.MuseumModeIntensity = 1

	museumName, museumMod, err := OptimizeFile(path, cfgMuseum, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile museum mode failed: %v", err)
	}
	if !museumMod {
		t.Error("expected modified to be true with museum mode")
	}
	if err := ValidateImage(context.Background(), filepath.Join(tmpDir, museumName)); err != nil {
		t.Errorf("museum mode output is invalid: %v", err)
	}

	// Run without museum mode for comparison — this path uses dither instead
	path2 := filepath.Join(tmpDir, "no_museum.jpg")
	f2, err := os.Create(filepath.Clean(path2))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f2, img, &jpeg.Options{Quality: 100})
	_ = f2.Close()

	cfgNormal := DefaultConfig()
	cfgNormal.MaxWidth = 100
	cfgNormal.MaxHeight = 100
	cfgNormal.MuseumModeEnabled = false

	normalName, normalMod, err := OptimizeFile(path2, cfgNormal, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile normal mode failed: %v", err)
	}
	if normalMod || normalName != filepath.Base(path2) {
		t.Errorf("exact image without effects = (%q, %v), want unchanged", normalName, normalMod)
	}
	if err := ValidateImage(context.Background(), filepath.Join(tmpDir, normalName)); err != nil {
		t.Errorf("normal mode output is invalid: %v", err)
	}
}

func TestOptimizeFile_DisabledAndUnsupported(t *testing.T) {
	tmpDir := t.TempDir()
	pathDisabled := filepath.Join(tmpDir, "disabled.jpg")
	f, err := os.Create(filepath.Clean(pathDisabled))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 100, 100)), nil)
	_ = f.Close()

	cfg := DefaultConfig()
	cfg.Enabled = false
	gotName, mod, err := OptimizeFile(pathDisabled, cfg, slog.Default())
	if err != nil || gotName != "disabled.jpg" || mod {
		t.Fatalf("OptimizeFile disabled = (%s, %v, %v)", gotName, mod, err)
	}

	pathPNG := filepath.Join(tmpDir, "image.png")
	if err := os.WriteFile(pathPNG, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.OptimizePNG = false
	gotName, mod, err = OptimizeFile(pathPNG, cfg, slog.Default())
	if err != nil || gotName != "image.png" || mod {
		t.Fatalf("OptimizeFile unsupported extension = (%s, %v, %v)", gotName, mod, err)
	}
}

func TestHandleRename_NoChange(t *testing.T) {
	filename := "upload.jpg"
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// ensure handleRename reports no rename when filename already matches target
	_, changed, err := handleRename(renameRequest{
		path:     path,
		filename: filename,
		dir:      filepath.Dir(path),
		modified: false,
		finalW:   4,
		finalH:   4,
		logger:   slog.Default(),
	})
	if err != nil || changed {
		t.Fatalf("handleRename() = (%v, %v, %v)", changed, err, path)
	}
}

func TestHandleRename_NoChangeWhenAlreadyOptimizedButModified(t *testing.T) {
	filename := "upload.jpg"
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	_, changed, err := handleRename(renameRequest{
		path:     path,
		filename: filename,
		dir:      filepath.Dir(path),
		modified: true,
		finalW:   4,
		finalH:   4,
		logger:   slog.Default(),
	})
	if err != nil {
		t.Fatalf("handleRename() = %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true to preserve modified state")
	}
}

func TestOptimizeFileFastPathVerifiesDecodedDimensions(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "lying_32x18_opt.h_local.jpg")
	writeTestImage(t, path, 1, 1)

	cfg := DefaultConfig()
	cfg.MaxWidth = 32
	cfg.MaxHeight = 18
	name, changed, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile() error = %v", err)
	}
	if !changed {
		t.Fatal("OptimizeFile() changed = false, want true")
	}
	output, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = output.Close() }()
	decoded, _, err := image.Decode(output)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got := decoded.Bounds().Size(); got.X != 32 || got.Y != 18 {
		t.Fatalf("output dimensions = %v, want (32,18)", got)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatalf("seek output: %v", err)
	}
	if orientation, err := ReadOrientation(output); err != nil || orientation != 1 {
		t.Fatalf("output orientation = (%d, %v), want canonical orientation 1", orientation, err)
	}
}

func TestOptimizeFileAppliesOrientationBeforeExactSizeDecision(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "rotated.jpg")
	writeJPEGWithExifOrientation(t, path, 32, 18, 6)

	cfg := DefaultConfig()
	cfg.MaxWidth = 32
	cfg.MaxHeight = 18
	name, changed, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile() error = %v", err)
	}
	if !changed {
		t.Fatal("OptimizeFile() changed = false, want orientation rewrite")
	}
	output, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = output.Close() }()
	decoded, _, err := image.Decode(output)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got := decoded.Bounds().Size(); got.X != 32 || got.Y != 18 {
		t.Fatalf("output dimensions = %v, want (32,18)", got)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatalf("seek output: %v", err)
	}
	if orientation, err := ReadOrientation(output); err != nil || orientation != 1 {
		t.Fatalf("output orientation = (%d, %v), want canonical orientation 1", orientation, err)
	}
}

func TestHandleRename_RenamesWhenNeeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	newName, changed, err := handleRename(renameRequest{
		path:     path,
		filename: "source.jpg",
		dir:      dir,
		modified: true,
		finalW:   1280,
		finalH:   720,
		logger:   slog.Default(),
		cfg:      DefaultConfig(),
	})
	if err != nil || !changed {
		t.Fatalf("handleRename() = (%s, %v, %v), want rename success", newName, changed, err)
	}
	if newName == "source.jpg" {
		t.Fatal("expected filename to change after rename")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("expected source file to be renamed")
	}
	if _, err := os.Stat(filepath.Join(dir, newName)); err != nil {
		t.Fatalf("expected renamed output to exist: %v", err)
	}
}

func TestHandleRenamePreservesExistingOperatorDestination(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	source := filepath.Join(directory, "source.jpg")
	if err := os.WriteFile(source, []byte("optimized bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	destinationName := artwork.BuildContentName("source.jpg", digest, extJPG, 8)
	destination := filepath.Join(directory, destinationName)
	if err := os.WriteFile(destination, []byte("operator bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	newName, changed, err := handleRename(renameRequest{
		path: source, filename: "source.jpg", dir: directory, modified: true,
		finalW: 1280, finalH: 720, logger: slog.Default(), ctx: context.Background(),
	})
	if err != nil || !changed {
		t.Fatalf("handleRename() = (%q, %v, %v), want collision-safe rename", newName, changed, err)
	}
	if newName == destinationName {
		t.Fatalf("handleRename() reused occupied destination %q", newName)
	}
	if got, err := os.ReadFile(filepath.Join(directory, newName)); err != nil || string(got) != "optimized bytes" {
		t.Fatalf("renamed source = %q, %v", got, err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "operator bytes" {
		t.Fatalf("destination = %q, %v", got, err)
	}
}

func TestHandleRename_RenameErrorPropagated(t *testing.T) {
	dir := t.TempDir()
	_, _, err := handleRename(renameRequest{
		path:     filepath.Join(dir, "missing.jpg"),
		filename: "missing.jpg",
		dir:      dir,
		modified: true,
		finalW:   4,
		finalH:   4,
		logger:   slog.Default(),
	})
	if err == nil {
		t.Fatalf("expected rename failure error")
	}
}

func TestOptimizeFile_DecodeImageErrorAfterConfig(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	valid := filepath.Join(dir, "valid.jpg")
	f, err := os.Create(valid)
	if err != nil {
		t.Fatalf("create valid image: %v", err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		_ = f.Close()
		t.Fatalf("encode image: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close image: %v", err)
	}

	raw, err := os.ReadFile(valid)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	corrupt := filepath.Join(dir, "corrupt.jpg")
	if err := os.WriteFile(corrupt, raw[:len(raw)-6], 0o600); err != nil {
		t.Fatalf("write corrupt image: %v", err)
	}

	cfg := DefaultConfig()
	cfg.MaxWidth = 16
	cfg.MaxHeight = 16
	_, _, err = OptimizeFile(corrupt, cfg, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "decode image") {
		t.Fatalf("OptimizeFile() = %v", err)
	}
}

func TestRewriteImage_PortraitModePadsInsteadOfCrop(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "portrait.jpg")
	writeTestImage(t, src, 3, 5)

	f, err := os.Open(src)
	if err != nil {
		t.Fatalf("open source image: %v", err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth = 8
	cfg.MaxHeight = 10
	cfg.PortraitMode = "pad"

	newW, newH, err := rewriteImage(rewriteParams{
		f:        f,
		path:     src,
		filename: "portrait.jpg",
		width:    3,
		height:   5,
		cfg:      cfg,
		logger:   slog.Default(),
	})
	if err != nil {
		t.Fatalf("rewriteImage() = %v", err)
	}
	if newW != cfg.MaxWidth || newH != cfg.MaxHeight {
		t.Fatalf("expected rewritten dimensions %dx%d, got %dx%d", cfg.MaxWidth, cfg.MaxHeight, newW, newH)
	}
	if err := ValidateImage(context.Background(), src); err != nil {
		t.Fatalf("rewritten image invalid: %v", err)
	}
}

func TestRewriteImageReportsEachIOFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(t *testing.T) (*os.File, string)
		want    string
	}{
		{
			name: "closed source", want: "seek to start",
			prepare: func(t *testing.T) (*os.File, string) {
				source := openRewriteSource(t, true)
				_ = source.Close()
				return source, filepath.Join(t.TempDir(), "renamed.jpg")
			},
		},
		{
			name: "undecodable source", want: "decode image",
			prepare: func(t *testing.T) (*os.File, string) {
				return openRewriteSource(t, false), filepath.Join(t.TempDir(), "bad.jpg")
			},
		},
		{
			name: "temporary file directory is a file", want: "create optimized temporary file",
			prepare: func(t *testing.T) (*os.File, string) {
				blocker := filepath.Join(t.TempDir(), "blocked")
				if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return openRewriteSource(t, true), filepath.Join(blocker, "out.jpg")
			},
		},
		{
			name: "destination is a directory", want: "replace optimized image",
			prepare: func(t *testing.T) (*os.File, string) {
				target := filepath.Join(t.TempDir(), "blocked")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				return openRewriteSource(t, true), target
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source, path := test.prepare(t)
			_, _, err := rewriteImage(rewriteParams{
				f: source, path: path, filename: filepath.Base(path), width: 8, height: 8,
				cfg: DefaultConfig(), logger: discardLogger(),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("rewriteImage() error = %v, want %q", err, test.want)
			}
		})
	}
}

func openRewriteSource(t *testing.T, valid bool) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jpg")
	if valid {
		writeTestImage(t, path, 8, 8)
	} else if err := os.WriteFile(path, []byte("not-an-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open source image: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestOptimizeFileRejectsUnreadableInputsAndUnsafeTargets(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.jpg")
	writeTestImage(t, valid, 8, 6)
	notImage := filepath.Join(directory, "bad.jpg")
	if err := os.WriteFile(notImage, []byte("not a jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		path      string
		configure func(*Config)
		want      string
	}{
		{name: "undecodable header", path: notImage, configure: func(cfg *Config) { cfg.MaxWidth, cfg.MaxHeight = 10, 10 }, want: "decode image config"},
		{name: "invalid target", path: valid, configure: func(cfg *Config) { cfg.MaxWidth = 0 }},
		{name: "working set over budget", path: valid, configure: func(cfg *Config) { cfg.MaxWidth, cfg.MaxHeight, cfg.MaxWorkingBytes = 8, 6, 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			test.configure(&cfg)
			if _, _, err := OptimizeFile(test.path, cfg, discardLogger()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OptimizeFile() error = %v, want %q", err, test.want)
			}
		})
	}

	file, err := os.Open(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := validateOptimizedPixels(context.Background(), file, 1, 1); err == nil {
		t.Fatal("validateOptimizedPixels(wrong dimensions) error = nil")
	}
}

func TestOptimizeFileContextHonorsCancellationBeforeIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "missing.jpg")

	name, modified, err := optimizeFileWithPolicy(ctx, path, filepath.Base(path), false, DefaultConfig(), discardLogger(), defaultPixelWorkers())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OptimizeFileContext() error = %v, want context.Canceled", err)
	}
	if name != "missing.jpg" || modified {
		t.Fatalf("OptimizeFileContext() = (%q, %t), want (missing.jpg, false)", name, modified)
	}
}
