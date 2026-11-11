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
		mean, rms := lumaMeanAndRMS(src)
		// The partition sums are floating point, so a flat picture measures a
		// few millionths of a level instead of exactly zero.
		if math.IsNaN(rms) || rms < 0 || rms > 1e-4 {
			t.Fatalf("RMS contrast of solid %v = %v, want zero to within rounding", fill, rms)
		}
		want := polishedSolid(fill, mean)
		got := applyMuseumMode(src, 0).RGBAAt(1920, 1080)
		// Paper grain moves every channel by at most 2.5 levels, and the
		// 14-bit encode table and rounding by at most one more.
		const grain = 3.5
		for channel, shift := range [3]float64{float64(got.R) - want[0], float64(got.G) - want[1], float64(got.B) - want[2]} {
			if math.Abs(shift) > grain {
				t.Fatalf("solid %v became %v, want %.1f in channel %d give or take the grain", fill, got, want[channel], channel)
			}
		}
	}
}

// polishedSolid is what museum mode makes of one flat colour before paper
// grain: the strongest contrast curve pivoted on the colour's own luminance,
// the 3% pigment desaturation in linear light, the highlight roll-off, and
// the 8% desaturation of the polish.
func polishedSolid(fill color.RGBA, mean float64) [3]float64 {
	var linear [3]float64
	for channel, value := range [3]uint8{fill.R, fill.G, fill.B} {
		linear[channel] = srgbDecode(pivotedContrast(float64(value)/255, mean/255, 1.15))
	}
	linearAverage := (linear[0] + linear[1] + linear[2]) / 3
	var encoded [3]float64
	for channel := range linear {
		encoded[channel] = srgbEncode(linear[channel]*0.97+linearAverage*0.03) * 255
	}
	if brightest := max(encoded[0], encoded[1], encoded[2]); brightest > highlightKnee {
		scale := float64(rollOffHighlight(float32(brightest))) / brightest
		encoded = [3]float64{encoded[0] * scale, encoded[1] * scale, encoded[2] * scale}
	}
	average := (encoded[0] + encoded[1] + encoded[2]) / 3
	for channel := range encoded {
		encoded[channel] = encoded[channel]*0.92 + average*0.08
	}
	return encoded
}

func TestReadOrientationTreatsUnsetValueAsUpright(t *testing.T) {
	got, err := readOrientation(bytes.NewReader(exifJPEG(binary.LittleEndian, 3, 0)))
	if err != nil || got != 1 {
		t.Fatalf("ReadOrientation() = (%d, %v), want (1, nil)", got, err)
	}
}
