package optimize

import (
	"image"
	"image/color"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
)

func TestValidateInputDimensions(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		wantError     bool
	}{
		{name: "display", width: 3840, height: 2160},
		{name: "limit", width: 8000, height: 5000},
		{name: "over limit", width: 8001, height: 5000, wantError: true},
		{name: "zero", width: 0, height: 2160, wantError: true},
		{name: "negative", width: -1, height: 2160, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateInputDimensions(test.width, test.height)
			if (err != nil) != test.wantError {
				t.Fatalf("validateInputDimensions(%d, %d) error = %v", test.width, test.height, err)
			}
		})
	}
}

func TestValidateOutputDimensionsEnforcesWorkingSetAdmission(t *testing.T) {
	t.Parallel()
	if err := validateOutputDimensions(3840, 2160, 12_000_000); err != nil {
		t.Fatalf("4K target rejected: %v", err)
	}
	if err := validateOutputDimensions(16384, 16384, 12_000_000); err == nil {
		t.Fatal("unsafe target dimensions were accepted")
	}
	if err := validateOutputDimensions(0, 1, 1); err == nil {
		t.Fatal("zero-width target was accepted")
	}
	if err := validateOutputDimensions(1, 1, 0); err != nil {
		t.Fatalf("unset pixel limit did not fall back to the default: %v", err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.MaxWorkingBytes = 1, 1, 0
	if err := validateWorkingSet(workingInput{pixels: 1, sourceBytes: 8}, cfg); err != nil {
		t.Fatalf("unset working-set budget did not fall back to the default: %v", err)
	}
}

func TestValidateWorkingSetAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		config    image.Config
		mutate    func(*Config)
		wantError bool
	}{
		{name: "24MP JPEG in default budget", config: image.Config{Width: 6000, Height: 4000, ColorModel: color.YCbCrModel}},
		{name: "8K JPEG in default budget", config: image.Config{Width: 7680, Height: 4320, ColorModel: color.YCbCrModel}},
		{
			name:      "40MP 16-bit PNG exceeds default budget",
			config:    image.Config{Width: 8000, Height: 5000, ColorModel: color.NRGBA64Model},
			wantError: true,
		},
		{
			name:      "unsafe linear transform",
			config:    image.Config{Width: 8000, Height: 5000, ColorModel: color.YCbCrModel},
			mutate:    func(cfg *Config) { cfg.MaxWorkingBytes = 128 * 1024 * 1024 },
			wantError: true,
		},
		{
			name:      "gamma-space kernel temporary is charged",
			config:    image.Config{Width: 8000, Height: 5000, ColorModel: color.YCbCrModel},
			mutate:    func(cfg *Config) { cfg.LinearLightResize = false },
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			if test.mutate != nil {
				test.mutate(&cfg)
			}
			err := validateWorkingSet(workingInputFor(test.config), cfg)
			if (err != nil) != test.wantError {
				t.Fatalf("validateWorkingSet(%dx%d) error = %v, wantError %t", test.config.Width, test.config.Height, err, test.wantError)
			}
		})
	}
}

func TestWorkingSetEstimateComponents(t *testing.T) {
	t.Parallel()
	input := workingInputFor(image.Config{Width: 6000, Height: 4000, ColorModel: color.YCbCrModel})
	if input.sourceBytes != 24_000_000*8 {
		t.Fatalf("JPEG source bytes = %d, want decode plus working copy", input.sourceBytes)
	}
	linear := DefaultConfig()
	gamma := linear
	gamma.LinearLightResize = false
	smart := linear
	smart.SmartCropEnabled = true
	if estimatedWorkingBytes(input, gamma) <= estimatedWorkingBytes(input, linear) {
		t.Fatal("gamma-space kernel temporary should exceed the banded linear scratch")
	}
	if estimatedWorkingBytes(input, smart) <= estimatedWorkingBytes(input, linear) {
		t.Fatal("smart-crop analysis was not charged")
	}
	// The encoder keeps six bytes of coefficients per pixel with full chroma,
	// more than the four-byte validation decode that takes their place, and
	// three with halved chroma, which is less.
	fullChroma, halvedChroma := linear, linear
	fullChroma.JPEGSubsampling, halvedChroma.JPEGSubsampling = jpegenc.Subsampling444, jpegenc.Subsampling420
	if got, want := estimatedWorkingBytes(input, fullChroma)-estimatedWorkingBytes(input, halvedChroma), int64(3840*2160*2*5/4); got != want {
		t.Fatalf("full-chroma encode adds %d bytes to the estimate, want %d for its kept coefficients", got, want)
	}
	if combined := input.plus(input); combined.pixels != 2*input.pixels || combined.sourceBytes != 2*input.sourceBytes ||
		combined.extent != 2*(6000+4000) {
		t.Fatalf("plus() = %+v", combined)
	}
}

// A long, thin picture has few pixels and an enormous resample tap table:
// four weights for every source pixel along the axis.
func TestWorkingSetChargesResampleTapTables(t *testing.T) {
	t.Parallel()
	linear := DefaultConfig()
	linear.SmartCropEnabled, linear.LinearLightResize = false, true
	strip := workingInputFor(image.Config{Width: 30_000_000, Height: 1, ColorModel: color.GrayModel})
	if err := validateWorkingSet(strip, linear); err == nil {
		t.Error("a 30,000,000x1 strip was admitted; its tap table alone is close to the whole budget")
	}
	square := workingInputFor(image.Config{Width: 5477, Height: 5477, ColorModel: color.GrayModel})
	if err := validateWorkingSet(square, linear); err != nil {
		t.Errorf("the same pixels as a 5477x5477 square were rejected: %v", err)
	}
	gamma := linear
	gamma.LinearLightResize = false
	if resampleTableBytes(strip.extent, gamma) <= resampleTableBytes(strip.extent, linear) {
		t.Error("the gamma-space resampler's wider taps were not charged")
	}
	for _, size := range [][2]int{{200_000, 3000}, {50, 3840}, {5000, 2160}, {3841, 3840}} {
		axis := newResampleAxis(size[0], size[1])
		tableBytes := int64(4*len(axis.weights) + 8*len(axis.start) + 8*len(axis.offset) + 4*len(axis.coreFirst) + 4*len(axis.coreEnd))
		if estimate := resampleTableBytes(int64(size[0]), linear); tableBytes > estimate {
			t.Errorf("tap table for %d -> %d holds %d bytes, over the %d-byte estimate", size[0], size[1], tableBytes, estimate)
		}
		// The weights are sized once, not grown by appending.
		if spare := cap(axis.weights) - len(axis.weights); spare > 2*size[1] {
			t.Errorf("tap table for %d -> %d has %d spare weights of %d", size[0], size[1], spare, len(axis.weights))
		}
	}
}

func TestDecodedBytesPerPixel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model color.Model
		want  int64
	}{
		{model: color.Palette{color.Black, color.White}, want: 1},
		{model: color.GrayModel, want: 1},
		{model: color.Gray16Model, want: 2},
		{model: color.YCbCrModel, want: 4},
		{model: color.CMYKModel, want: 4},
		{model: color.RGBA64Model, want: 8},
		{model: nil, want: 8},
	}
	for _, test := range tests {
		if got := decodedBytesPerPixel(test.model); got != test.want {
			t.Fatalf("decodedBytesPerPixel(%T) = %d, want %d", test.model, got, test.want)
		}
	}
}
