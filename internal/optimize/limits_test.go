package optimize

import (
	"image"
	"image/color"
	"testing"
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
	if combined := input.plus(input); combined.pixels != 2*input.pixels || combined.sourceBytes != 2*input.sourceBytes {
		t.Fatalf("plus() = %+v", combined)
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
