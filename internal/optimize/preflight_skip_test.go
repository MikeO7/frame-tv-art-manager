package optimize

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// jpegWithSegment encodes a width x height JPEG with one extra marker segment
// placed straight after the start-of-image marker.
func jpegWithSegment(t *testing.T, width, height int, marker byte, payload []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, imageFromPattern(width, height), nil); err != nil {
		t.Fatal(err)
	}
	segment := append([]byte{0xff, marker, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	return append(append(append([]byte(nil), encoded.Bytes()[:2]...), segment...), encoded.Bytes()[2:]...)
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A file the transform only skips must not be reported as work: the stage
// would copy the whole collection, skip it again, and repeat every sync.
func TestUnparseableEXIFIsOptimizedInsteadOfSkippedEverySync(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	brokenExif := append([]byte(exifSignature), "ZZ******"...)
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 16, 12

	exact := filepath.Join(dir, "exact.jpg")
	writeBytes(t, exact, jpegWithSegment(t, 16, 12, jpegMarkerAPP1, brokenExif))
	required, err := RequiresStage(context.Background(), StageRequest{
		Inputs: []StageInput{{Name: "exact.jpg", Path: exact, Width: 16, Height: 12}}, Config: cfg,
	})
	if err != nil || required {
		t.Fatalf("RequiresStage(exact size, broken EXIF) = (%v, %v), want (false, nil): it is upright and needs no work", required, err)
	}

	larger := filepath.Join(dir, "larger.jpg")
	writeBytes(t, larger, jpegWithSegment(t, 32, 24, jpegMarkerAPP1, brokenExif))
	name, modified, err := OptimizeFile(larger, cfg, discardLogger())
	if err != nil || !modified {
		t.Fatalf("OptimizeFile(broken EXIF) = (%q, %v, %v), want the picture optimized", name, modified, err)
	}
	if isPortrait, err := isPortraitFile(context.Background(), exact); err != nil || isPortrait {
		t.Fatalf("isPortraitFile(broken EXIF) = (%v, %v), want (false, nil)", isPortrait, err)
	}
}

func TestRequiresStageLeavesArtworkTheTransformSkipsForSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.OptimizePNG = 16, 12, true
	// The budget admits a 40x30 picture with eight-bit samples and nothing
	// deeper or larger.
	cfg.MaxWorkingBytes = estimatedWorkingBytes(workingInputOfSize(40, 30, 4), cfg)

	writeTestImage(t, filepath.Join(dir, "fits.jpg"), 40, 30)
	writeTestImage(t, filepath.Join(dir, "large.jpg"), 400, 300)
	deep := image.NewRGBA64(image.Rect(0, 0, 40, 30))
	deep.SetRGBA64(3, 3, color.RGBA64{R: 40000, G: 9000, B: 100, A: 65535})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, deep); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, filepath.Join(dir, "deep.png"), encoded.Bytes())

	input := func(name string, width, height int) StageInput {
		return StageInput{Name: name, Path: filepath.Join(dir, name), Width: width, Height: height}
	}
	tests := []struct {
		name   string
		inputs []StageInput
		mutate func(*Config)
		want   bool
	}{
		{name: "fits the budget", inputs: []StageInput{input("fits.jpg", 40, 30)}, want: true},
		{name: "over the budget", inputs: []StageInput{input("large.jpg", 400, 300)}},
		{name: "over the budget only for its sample depth", inputs: []StageInput{input("deep.png", 40, 30)}},
		{name: "more pixels than the decoder accepts", inputs: []StageInput{{Name: "huge.jpg", Path: "unused", Width: 9000, Height: 5000}}},
		{name: "unreadable header in the undecided band", inputs: []StageInput{{Name: "gone.png", Path: filepath.Join(dir, "gone.png"), Width: 40, Height: 30}}, want: true},
		{
			name:   "two portraits too large to collage",
			inputs: []StageInput{{Name: "a.jpg", Path: "unused", Width: 300, Height: 400}, {Name: "b.jpg", Path: "unused", Width: 300, Height: 400}},
			mutate: func(cfg *Config) { cfg.PortraitMode = portraitModeCollage },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testCfg := cfg
			if test.mutate != nil {
				test.mutate(&testCfg)
			}
			var logged bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
			got, err := RequiresStage(context.Background(), StageRequest{Inputs: test.inputs, Config: testCfg, Logger: logger})
			if err != nil || got != test.want {
				t.Fatalf("RequiresStage() = (%v, %v), want (%v, nil)", got, err, test.want)
			}
			if skipped := strings.Contains(logged.String(), "too large to transform"); testCfg.PortraitMode != portraitModeCollage && skipped == test.want {
				t.Fatalf("skip logged = %v for a file that requires the stage = %v; log: %s", skipped, test.want, logged.String())
			}
		})
	}

	// The stage agrees with the preflight about every one of the real files.
	pass, err := runTestPass(context.Background(), dir, cfg, "fits.jpg", "large.jpg", "deep.png")
	if err != nil || pass.optimized != 1 || len(pass.renames) != 1 || pass.renames[0].OldName != "fits.jpg" {
		t.Fatalf("run() = %v, optimized %d, renames %v; want only fits.jpg transformed", err, pass.optimized, pass.renames)
	}
}

func TestTransformSkipsForSizeReturnsCancellation(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 16, 12
	cfg.MaxWorkingBytes = estimatedWorkingBytes(workingInputOfSize(40, 30, 4), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := StageInput{Name: "deep.png", Path: "unused", Width: 40, Height: 30}
	if _, err := transformSkipsForSize(ctx, input, cfg); !isCancellation(err) {
		t.Fatalf("transformSkipsForSize(canceled) error = %v, want cancellation", err)
	}
	if skipped, err := transformSkipsForSize(ctx, StageInput{Name: "unknown.jpg"}, cfg); err != nil || skipped {
		t.Fatalf("transformSkipsForSize(unknown size) = (%v, %v), want (false, nil)", skipped, err)
	}
}

func TestRequiresStageStagesMalformedColorMetadataInsteadOfFailingTheSync(t *testing.T) {
	t.Parallel()
	// Segment 2 of 1 is impossible ICC framing.
	payload := []byte(jpegICCSignature + "\x02\x01profile")
	path := filepath.Join(t.TempDir(), "framing.jpg")
	writeBytes(t, path, jpegWithSegment(t, 16, 12, jpegMarkerAPP2, payload))
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 16, 12
	request := StageRequest{Inputs: []StageInput{{Name: "framing.jpg", Path: path, Width: 16, Height: 12}}, Config: cfg}
	required, err := RequiresStage(context.Background(), request)
	if err != nil || !required {
		t.Fatalf("RequiresStage(malformed ICC framing) = (%v, %v), want (true, nil) as at any other size", required, err)
	}
}

func TestCollectRawPortraitsLeavesOutPortraitsTooLargeToTransform(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestImage(t, filepath.Join(dir, "small_a.jpg"), 30, 40)
	writeTestImage(t, filepath.Join(dir, "small_b.jpg"), 30, 40)
	writeTestImage(t, filepath.Join(dir, "large.jpg"), 300, 400)
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.PortraitMode = 16, 12, portraitModeCollage
	cfg.MaxWorkingBytes = estimatedWorkingBytes(workingInputOfSize(30, 40, 4), cfg)
	pass := testPass(dir, cfg)
	localFiles := map[string]struct{}{"small_a.jpg": {}, "small_b.jpg": {}, "large.jpg": {}}
	got := pass.collectRawPortraits(context.Background(), localFiles)
	if want := []string{"small_a.jpg", "small_b.jpg"}; !slices.Equal(got, want) {
		t.Fatalf("collectRawPortraits() = %v, want %v", got, want)
	}
}
