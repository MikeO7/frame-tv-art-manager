package optimize

import "testing"

// The background is blurred at an eighth of the frame, so it is resized
// straight to that size: resizing to the whole frame first and reducing it
// again costs a full-frame resample for detail the blur discards.
func TestPadOnBlurBlursTheDirectlyReducedBackground(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.MaxUpscale, cfg.PadStyle = 384, 216, 1, padStyleBlur
	source := imageFromPattern(120, 240)
	padded := padOnBlur(source, cfg)

	crop := cropRectForAspectWithProtection(source, 384.0/216.0, false, 0, false, 0)
	want := blurImage(resizeCrop(source, crop, 384/blurReduction, 216/blurReduction, cfg.LinearLightResize), 8, 384, 216)
	muteBackground(want)
	// The picture is 108 pixels wide in the middle; the columns left of it
	// show only the background.
	for y := range 216 {
		for x := range 130 {
			if got, expected := padded.RGBAAt(x, y), want.RGBAAt(x, y); got != expected {
				t.Fatalf("background at (%d, %d) = %v, want %v from the reduced crop", x, y, got, expected)
			}
		}
	}
}

func BenchmarkPadOnBlur(b *testing.B) {
	cfg := DefaultConfig()
	cfg.PadStyle = padStyleBlur
	source := imageFromPattern(3000, 4000)
	for b.Loop() {
		padOnBlur(source, cfg)
	}
}
