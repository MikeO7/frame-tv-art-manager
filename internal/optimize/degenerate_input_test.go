package optimize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestStageCatalogRendersMuseumCollageOnce(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	cfg.MuseumModeEnabled = true
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight

	inputs := make([]StageInput, 0, 2)
	digests := make(map[[sha256.Size]byte]bool)
	for _, name := range []string{"a.png", "b.png"} {
		path := filepath.Join(dir, name)
		writePNGForCollageTests(t, path, 60, 90)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		digests[digest] = true
		inputs = append(inputs, StageInput{Name: name, Path: path, Digest: digest})
	}
	stage, err := StageCatalog(context.Background(), StageRequest{Inputs: inputs, Config: cfg, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("StageCatalog: %v", err)
	}
	t.Cleanup(func() {
		if err := stage.Close(); err != nil {
			t.Errorf("close stage: %v", err)
		}
	})
	if stage.Optimized != 1 || len(stage.Derivatives) != 1 {
		t.Fatalf("optimized = %d, derivatives = %+v; want one collage", stage.Optimized, stage.Derivatives)
	}
	collage := stage.Derivatives[0]
	if collage.Kind != DerivativeCollage || len(collage.Originals) != 2 ||
		!digests[collage.Originals[0]] || !digests[collage.Originals[1]] {
		t.Fatalf("collage lineage = %+v, want both portraits as originals", collage)
	}
	entries, err := os.ReadDir(stage.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != collage.Name {
		t.Fatalf("staged files = %v, want only %s", entries, collage.Name)
	}
}

func TestSmartCropHandlesExtremeStrips(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
	}{
		{name: "tall strip", width: 20, height: 6000},
		{name: "wide strip", width: 6000, height: 20},
		{name: "single column", width: 1, height: 50},
		{name: "single row", width: 50, height: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := imageFromPattern(test.width, test.height)
			rect := cropRectForAspectWithProtection(src, 16.0/9.0, true, 0, true, 1)
			if rect.Empty() || !rect.In(src.Bounds()) {
				t.Fatalf("crop = %v, want a non-empty rectangle inside %v", rect, src.Bounds())
			}
		})
	}
}

func TestMuseumModeKeepsSolidColour(t *testing.T) {
	// Sizes and colours whose accumulated variance rounds below zero.
	for _, fill := range []color.RGBA{{R: 33, G: 15, B: 199, A: 255}, {R: 201, G: 77, B: 5, A: 255}, {R: 128, G: 128, B: 128, A: 255}} {
		src := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
		for offset := 0; offset < len(src.Pix); offset += 4 {
			src.Pix[offset], src.Pix[offset+1], src.Pix[offset+2], src.Pix[offset+3] = fill.R, fill.G, fill.B, fill.A
		}
		if rms := calculateRMSContrast(src); math.IsNaN(rms) || rms < 0 || rms > 1 {
			t.Fatalf("RMS contrast of solid %v = %v, want about zero", fill, rms)
		}
		got := applyMuseumMode(src, 0).RGBAAt(1920, 1080)
		if distance := max(absDiff(got.R, fill.R), absDiff(got.G, fill.G), absDiff(got.B, fill.B)); distance > 60 {
			t.Fatalf("solid %v became %v", fill, got)
		}
	}
}

func absDiff(left, right uint8) int {
	if left > right {
		return int(left - right)
	}
	return int(right - left)
}

func TestReadOrientationTreatsUnsetValueAsUpright(t *testing.T) {
	got, err := ReadOrientation(bytes.NewReader(exifJPEG(binary.LittleEndian, 3, 0)))
	if err != nil || got != 1 {
		t.Fatalf("ReadOrientation() = (%d, %v), want (1, nil)", got, err)
	}
}
