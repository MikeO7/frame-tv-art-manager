package optimize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiresStageSkipsVerifiedNoWork(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	inputs := []StageInput{
		{Name: "gallery_3840x2160_opt.h_aaaa.jpg", Path: "unused", Width: 3840, Height: 2160},
		{Name: "already-valid.png", Path: "unused", Width: 3840, Height: 2160},
	}
	required, err := RequiresStage(context.Background(), StageRequest{Inputs: inputs, Config: cfg})
	if err != nil || required {
		t.Fatalf("RequiresStage() = (%v, %v), want (false, nil)", required, err)
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
			name: "raw jpeg needs optimized name",
			cfg:  DefaultConfig(),
			inputs: []StageInput{
				{Name: "raw.jpg", Path: "unused", Width: 3840, Height: 2160},
			},
			want: true,
		},
		{
			name: "disabled optimization skips ordinary transform",
			cfg:  func() Config { value := DefaultConfig(); value.Enabled = false; return value }(),
			inputs: []StageInput{
				{Name: "raw.jpg", Path: "unused", Width: 3840, Height: 2160},
			},
		},
		{
			name: "odd raw png upload waits for partner",
			cfg:  DefaultConfig(),
			inputs: []StageInput{
				{Name: "upload-one.png", Path: "unused", Width: 100, Height: 200},
			},
		},
		{
			name: "portrait pair stages while optimization disabled",
			cfg:  func() Config { value := DefaultConfig(); value.Enabled = false; return value }(),
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
	portrait, err = preflightPortrait(context.Background(), StageInput{
		Name: "upload-portrait.png", Path: "unused", Width: 8, Height: 12,
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
