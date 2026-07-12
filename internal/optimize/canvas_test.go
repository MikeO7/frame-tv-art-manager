package optimize

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"
)

func TestApplyCanvasTexture(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 128, A: 255})
		}
	}

	processed := applyCanvasTexture(img, 5)
	if processed == nil || processed.Bounds() != img.Bounds() {
		t.Fatalf("unexpected canvas output bounds: %#v", processed.Bounds())
	}
}

func TestCalculateScharrImpastoAndSoftLight(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 20), G: uint8(y * 40), B: 30, A: 255})
		}
	}

	value := calculateScharrImpasto(img, 1, 1)
	if math.IsNaN(value) {
		t.Fatal("calculateScharrImpasto returned NaN")
	}

	slowBlend := applySoftLight(0.2, 0.4, 0.2)
	highBlend := applySoftLight(0.9, 0.6, 0.2)
	if slowBlend == highBlend {
		t.Fatalf("expected different soft-light branches, got %d == %d", slowBlend, highBlend)
	}
	if slowBlend == 0 || highBlend == 0 {
		t.Fatalf("unexpected soft-light edge case: %d %d", slowBlend, highBlend)
	}

	lowClamp := applySoftLight(0.0, 0.2, 1.0)
	if lowClamp != 0 {
		t.Fatalf("applySoftLight expected lower clamp to 0, got %d", lowClamp)
	}
	highClamp := applySoftLight(1.0, 1.0, 1.0)
	if highClamp != 255 {
		t.Fatalf("applySoftLight expected upper clamp to 255, got %d", highClamp)
	}
	overflowClamp := applySoftLight(0.9, 1.0, 3.0)
	if overflowClamp != 255 {
		t.Fatalf("applySoftLight expected overflow clamp to 255, got %d", overflowClamp)
	}
}

func TestApplySoftLight_NegativeInputClampsToZero(t *testing.T) {
	clamped := applySoftLight(-0.42, 0.5, 1.0)
	if clamped != 0 {
		t.Fatalf("applySoftLight(-0.42, 0.5, 1.0) = %d, want 0", clamped)
	}
}

func TestApplyCanvasTexture_IntensityClamps(t *testing.T) {
	imgLow := image.NewRGBA(image.Rect(0, 0, 10, 10))
	imgHigh := image.NewRGBA(image.Rect(0, 0, 10, 10))

	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			c := color.RGBA{
				R: uint8((x*25 + y*10) % 256),
				G: uint8((x * 11) % 256),
				B: 100,
				A: 255,
			}
			imgLow.Set(x, y, c)
			imgHigh.Set(x, y, c)
		}
	}

	low := applyCanvasTexture(imgLow, 1)
	high := applyCanvasTexture(imgHigh, 500)
	if low == nil || high == nil {
		t.Fatal("applyCanvasTexture returned nil output")
	}
	if low.Bounds() != imgLow.Bounds() || high.Bounds() != imgHigh.Bounds() {
		t.Fatal("applyCanvasTexture changed image bounds unexpectedly")
	}

	if bytes.Equal(low.Pix, high.Pix) {
		t.Fatal("intensity 1 and 500 produced identical outputs; opacity clamp branch likely not covered")
	}
}
