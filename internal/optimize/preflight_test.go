package optimize

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiresStageSkipsVerifiedNoWork(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	inputs := []StageInput{
		{Name: "gallery.jpg", Path: "unused", Width: 3840, Height: 2160, Derivative: "optimized", TransformKey: TransformKey(cfg)},
		{Name: "already-valid.png", Path: "unused", Width: 3840, Height: 2160, Derivative: "optimized", TransformKey: TransformKey(cfg)},
	}
	required, err := RequiresStage(context.Background(), StageRequest{Inputs: inputs, Config: cfg})
	if err != nil || required {
		t.Fatalf("RequiresStage() = (%v, %v), want (false, nil)", required, err)
	}
}

func TestRequiresStageDoesNotTrustOptimizedFilenameDimensions(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	required, err := RequiresStage(context.Background(), StageRequest{
		Inputs: []StageInput{{
			Name: "gallery.jpg", Path: "unused", Width: 1, Height: 1,
			Derivative: "optimized", TransformKey: TransformKey(cfg),
		}},
		Config: cfg,
	})
	if err != nil || !required {
		t.Fatalf("RequiresStage() = (%v, %v), want (true, nil)", required, err)
	}
}

func TestRequiresStageInvalidatesChangedTransformConfiguration(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	input := StageInput{
		Name: "gallery.jpg", Path: "unused", Width: cfg.MaxWidth, Height: cfg.MaxHeight,
		Derivative: "optimized", TransformKey: TransformKey(cfg),
	}

	required, err := RequiresStage(context.Background(), StageRequest{Inputs: []StageInput{input}, Config: cfg})
	if err != nil || required {
		t.Fatalf("RequiresStage(same config) = (%v, %v), want no work", required, err)
	}
	cfg.OptimizeJPEGQuality--
	required, err = RequiresStage(context.Background(), StageRequest{Inputs: []StageInput{input}, Config: cfg})
	if err != nil || !required {
		t.Fatalf("RequiresStage(changed quality) = (%v, %v), want transform", required, err)
	}
}

func TestRequiresStageEnforcesColorPolicyOnExactTargetRawImage(t *testing.T) {
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
	_, err := RequiresStage(context.Background(), StageRequest{
		Inputs: []StageInput{{Name: "tagged.jpg", Path: path, Width: 8, Height: 6}},
		Config: cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "JPEG ICC") {
		t.Fatalf("RequiresStage(reject embedded profile) error = %v", err)
	}
}

func TestRequiresStagePreservesTransformAndCollageSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cfg    Config
		inputs []StageInput
		want   bool
	}{
		{
			name: "disabled optimization skips ordinary transform",
			cfg:  func() Config { value := DefaultConfig(); value.Enabled = false; return value }(),
			inputs: []StageInput{
				{Name: "raw.jpg", Path: "unused", Width: 3840, Height: 2160},
			},
		},
		{
			name: "uncertain raw png upload stages conservatively",
			cfg:  DefaultConfig(),
			inputs: []StageInput{
				{Name: "upload-one.png", Path: "unused", Width: 100, Height: 200},
			},
			want: true,
		},
		{
			name: "portrait pair stages while optimization disabled",
			cfg: func() Config {
				value := DefaultConfig()
				value.Enabled = false
				value.PortraitMode = portraitModeCollage
				return value
			}(),
			inputs: []StageInput{
				{Name: "upload-one.png", Path: "unused", Width: 100, Height: 200},
				{Name: "upload-two.png", Path: "unused", Width: 100, Height: 200},
			},
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := RequiresStage(context.Background(), StageRequest{Inputs: test.inputs, Config: test.cfg})
			if err != nil || got != test.want {
				t.Fatalf("RequiresStage() = (%v, %v), want (%v, nil)", got, err, test.want)
			}
		})
	}
}

func TestRawCollageCandidateRequiresExplicitCollageMode(t *testing.T) {
	t.Parallel()
	for _, input := range []StageInput{
		{Name: "upload-legacy.jpg"},
		{Name: "opaque.jpg"},
		{Name: "provider.jpg"},
	} {
		cfg := DefaultConfig()
		if isRawCollageCandidate(input, cfg) {
			t.Fatalf("isRawCollageCandidate(%+v, crop) = true", input)
		}
		cfg.PortraitMode = portraitModeCollage
		if !isRawCollageCandidate(input, cfg) {
			t.Fatalf("isRawCollageCandidate(%+v, collage) = false", input)
		}
	}

	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	if isRawCollageCandidate(StageInput{Name: "derived.jpg", Derivative: "optimized"}, cfg) {
		t.Fatal("optimized derivative was treated as a raw collage candidate")
	}
}

func TestRequiresStageFallsBackForUncertainMetadataAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	required, err := RequiresStage(context.Background(), StageRequest{
		Inputs: []StageInput{{Name: "upload-missing.jpg", Path: filepath.Join(t.TempDir(), "missing.jpg"), Width: 100, Height: 200}},
		Config: cfg,
	})
	if err != nil || !required {
		t.Fatalf("RequiresStage(uncertain) = (%v, %v), want conservative staging", required, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	required, err = RequiresStage(ctx, StageRequest{Config: cfg})
	if required || !errors.Is(err, context.Canceled) {
		t.Fatalf("RequiresStage(canceled) = (%v, %v), want canceled", required, err)
	}
}

func TestRequiresStageReadsOnlyCandidateMetadata(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	first := filepath.Join(directory, "upload-one.jpg")
	second := filepath.Join(directory, "upload-two.jpg")
	writeTestImage(t, first, 8, 12)
	writeTestImage(t, second, 8, 12)
	cfg := DefaultConfig()
	cfg.Enabled = false
	cfg.PortraitMode = portraitModeCollage

	required, err := RequiresStage(context.Background(), StageRequest{
		Inputs: []StageInput{
			{Name: filepath.Base(first), Path: first, Width: 8, Height: 12},
			{Name: filepath.Base(second), Path: second, Width: 8, Height: 12},
		},
		Config: cfg,
	})
	if err != nil || !required {
		t.Fatalf("RequiresStage(portrait pair) = (%v, %v), want staging", required, err)
	}
}

func TestPreflightPortraitSafetyBranches(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	jpegPath := filepath.Join(directory, "upload-landscape.jpg")
	writeTestImage(t, jpegPath, 12, 8)

	portrait, err := preflightPortrait(context.Background(), StageInput{
		Name: filepath.Base(jpegPath), Path: jpegPath, Width: 12, Height: 8,
	})
	if err != nil || portrait {
		t.Fatalf("preflightPortrait(JPEG) = (%v, %v), want landscape", portrait, err)
	}
	pngPath := filepath.Join(directory, "upload-portrait.png")
	writePNGForCollageTests(t, pngPath, 8, 12)
	portrait, err = preflightPortrait(context.Background(), StageInput{
		Name: filepath.Base(pngPath), Path: pngPath, Width: 8, Height: 12,
	})
	if err != nil || !portrait {
		t.Fatalf("preflightPortrait(PNG) = (%v, %v), want portrait", portrait, err)
	}
	if _, err := preflightPortrait(context.Background(), StageInput{
		Name: "upload-invalid.png", Path: "unused", Width: 0, Height: 12,
	}); err == nil || !strings.Contains(err.Error(), "invalid image dimensions") {
		t.Fatalf("preflightPortrait(invalid dimensions) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preflightPortrait(ctx, StageInput{Name: "upload.png", Width: 1, Height: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflightPortrait(canceled) error = %v", err)
	}

	target := filepath.Join(directory, "target.jpg")
	writeTestImage(t, target, 8, 12)
	symlink := filepath.Join(directory, "upload-link.jpg")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, _, err := openPreflightInput(StageInput{Name: filepath.Base(symlink), Path: symlink}); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("openPreflightInput(symlink) error = %v", err)
	}

	rotated := filepath.Join(directory, "upload-rotated.jpg")
	writeJPEGWithExifOrientation(t, rotated, 12, 8, 6)
	portrait, err = preflightPortrait(context.Background(), StageInput{
		Name: filepath.Base(rotated), Path: rotated, Width: 12, Height: 8,
	})
	if err != nil || !portrait {
		t.Fatalf("preflightPortrait(rotated JPEG) = (%v, %v), want portrait", portrait, err)
	}

	malformed := filepath.Join(directory, "upload-malformed.jpg")
	if err := os.WriteFile(malformed, []byte{0xff, 0xd8}, 0o600); err != nil {
		t.Fatalf("write malformed JPEG: %v", err)
	}
	if _, err := preflightPortrait(context.Background(), StageInput{
		Name: filepath.Base(malformed), Path: malformed, Width: 12, Height: 8,
	}); err == nil || !strings.Contains(err.Error(), "read collage metadata") {
		t.Fatalf("preflightPortrait(malformed JPEG) error = %v", err)
	}

	for _, input := range []StageInput{
		{Name: "upload-missing.jpg", Path: filepath.Join(directory, "missing.jpg")},
		{Name: "upload-directory.jpg", Path: directory},
	} {
		if _, _, err := openPreflightInput(input); err == nil {
			t.Fatalf("openPreflightInput(%s) error = nil", input.Name)
		}
	}
}

func TestPreflightOrientationReadsStableFileAndCancellation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "exact.jpg")
	writeTestImage(t, path, 8, 6)
	orientation, err := preflightOrientation(context.Background(), StageInput{
		Name: filepath.Base(path), Path: path, Width: 8, Height: 6,
	})
	if err != nil || orientation != 1 {
		t.Fatalf("preflightOrientation() = (%d, %v)", orientation, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preflightOrientation(ctx, StageInput{Name: "exact.jpg", Path: path}); !errors.Is(err, context.Canceled) {
		t.Fatalf("preflightOrientation(canceled) error = %v", err)
	}
}
