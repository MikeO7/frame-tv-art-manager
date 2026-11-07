package config

import (
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func TestLoadEnvironRejectsInvalidSettings(t *testing.T) {
	t.Parallel()
	const tv = "TV_IPS=127.0.0.1"
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{name: "missing TV addresses", want: "TV_IPS environment variable is required"},
		{name: "blank TV addresses", environ: []string{"TV_IPS= , , "}, want: "at least one non-empty IP address"},
		{name: "solar without coordinates", environ: []string{tv, "SOLAR_BRIGHTNESS_ENABLED=true"}, want: "LOCATION_LATITUDE and LOCATION_LONGITUDE are required"},
		{name: "log level", environ: []string{tv, "LOG_LEVEL=invalid"}, want: "LOG_LEVEL"},
		{name: "inverted brightness range", environ: []string{tv, "BRIGHTNESS_MIN=10", "BRIGHTNESS_MAX=5"}, want: "BRIGHTNESS_MIN (10) must be less than BRIGHTNESS_MAX (5)"},
		{name: "slideshow type", environ: []string{tv, "SLIDESHOW_TYPE=random"}, want: "SLIDESHOW_TYPE"},
		{name: "manual brightness", environ: []string{tv, "BRIGHTNESS=not-a-number"}, want: "BRIGHTNESS"},
		{name: "brightness above the legacy scale", environ: []string{tv, "BRIGHTNESS=51"}, want: "BRIGHTNESS must be between 0 and 10"},
		{name: "solar maximum above the legacy scale", environ: []string{tv, "BRIGHTNESS_MAX=60"}, want: "BRIGHTNESS_MAX must be between 0 and 10"},
		{name: "negative brightness", environ: []string{tv, "BRIGHTNESS=-1"}, want: "BRIGHTNESS must be between 0 and 10"},
		{name: "latitude syntax", environ: []string{tv, "LOCATION_LATITUDE=north"}, want: "LOCATION_LATITUDE"},
		{name: "longitude syntax", environ: []string{tv, "LOCATION_LONGITUDE=west"}, want: "LOCATION_LONGITUDE"},
		{name: "portrait mode", environ: []string{tv, "PORTRAIT_MODE=stretch"}, want: "PORTRAIT_MODE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg, _, err := LoadEnviron(test.environ)
			if err == nil || !strings.Contains(err.Error(), test.want) || cfg != nil {
				t.Fatalf("LoadEnviron() = %v, %v; want error containing %q", cfg, err, test.want)
			}
		})
	}
}

func TestOptimizeOptionsMapsEveryImageSetting(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		OptimizeEnabled:                true,
		SmartCropEnabled:               true,
		SmartCropMinGain:               0.05,
		SmartCropProtection:            true,
		SmartCropProtectionStrength:    0.4,
		SmartCropProvider:              "local",
		SmartCropProviderMinConfidence: 0.75,
		SmartCropProviderTimeout:       9 * time.Second,
		OptimizeMaxWidth:               3840,
		OptimizeMaxHeight:              2160,
		OptimizeMaxPixels:              12_000_000,
		OptimizeMemoryMB:               512,
		OptimizeJPEGQuality:            90,
		JPEGSubsampling:                "420",
		OptimizePNG:                    true,
		LinearLightResize:              true,
		SharpenAmount:                  0.3,
		SharpenThreshold:               5,
		ColorProfilePolicy:             "assume-srgb",
		HDRToneMap:                     true,
		HDRSourcePeakNits:              1200,
		HDRTargetPeakNits:              100,
		MuseumModeEnabled:              true,
		MuseumModeIntensity:            7,
	}

	opts := cfg.OptimizeOptions()
	want := optimize.Config{
		Enabled:                        true,
		SmartCropEnabled:               true,
		SmartCropMinGain:               0.05,
		SmartCropProtection:            true,
		SmartCropProtectionStrength:    0.4,
		SmartCropProvider:              "local",
		SmartCropProviderMinConfidence: 0.75,
		SmartCropProviderTimeout:       9 * time.Second,
		MaxWidth:                       3840,
		MaxHeight:                      2160,
		MaxOutputPixels:                12_000_000,
		MaxWorkingBytes:                512 * 1024 * 1024,
		OptimizeJPEGQuality:            90,
		OptimizePNG:                    true,
		LinearLightResize:              true,
		SharpenAmount:                  0.3,
		SharpenThreshold:               5,
		ColorProfilePolicy:             "assume-srgb",
		HDRToneMap:                     true,
		HDRSourcePeakNits:              1200,
		HDRTargetPeakNits:              100,
		MuseumModeEnabled:              true,
		MuseumModeIntensity:            7,
		JPEGSubsampling:                jpegenc.Subsampling420,
	}
	if opts != want {
		t.Errorf("OptimizeOptions() = %+v, want %+v", opts, want)
	}
}

func TestLoadEnvironRescalesLegacyBrightness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		environ            []string
		manual, minimum    int
		maximum            int
		warnedVariables    []string
		expectManualIsNone bool
	}{
		{name: "frame scale is kept", environ: []string{"BRIGHTNESS=7"}, manual: 7, minimum: 2, maximum: 10},
		{name: "legacy manual value", environ: []string{"BRIGHTNESS=30"}, manual: 6, minimum: 2, maximum: 10, warnedVariables: []string{"BRIGHTNESS"}},
		{name: "legacy top of scale", environ: []string{"BRIGHTNESS=50"}, manual: 10, minimum: 2, maximum: 10, warnedVariables: []string{"BRIGHTNESS"}},
		{name: "just above frame scale rounds", environ: []string{"BRIGHTNESS=11"}, manual: 2, minimum: 2, maximum: 10, warnedVariables: []string{"BRIGHTNESS"}},
		{
			name: "legacy solar range", environ: []string{"BRIGHTNESS_MIN=15", "BRIGHTNESS_MAX=45"},
			minimum: 3, maximum: 9, warnedVariables: []string{"BRIGHTNESS_MAX", "BRIGHTNESS_MIN"}, expectManualIsNone: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg, warnings, err := LoadEnviron(append([]string{"TV_IPS=127.0.0.1"}, test.environ...))
			if err != nil {
				t.Fatalf("LoadEnviron() error = %v", err)
			}
			if test.expectManualIsNone != (cfg.ManualBrightness == nil) {
				t.Fatalf("ManualBrightness = %v, want none: %v", cfg.ManualBrightness, test.expectManualIsNone)
			}
			if cfg.ManualBrightness != nil && *cfg.ManualBrightness != test.manual {
				t.Errorf("ManualBrightness = %d, want %d", *cfg.ManualBrightness, test.manual)
			}
			if cfg.BrightnessMin != test.minimum || cfg.BrightnessMax != test.maximum {
				t.Errorf("range = %d-%d, want %d-%d", cfg.BrightnessMin, cfg.BrightnessMax, test.minimum, test.maximum)
			}
			var warned []string
			for _, warning := range warnings {
				if strings.HasPrefix(warning.Variable, "BRIGHTNESS") {
					warned = append(warned, warning.Variable)
				}
			}
			if strings.Join(warned, ",") != strings.Join(test.warnedVariables, ",") {
				t.Errorf("warned = %v, want %v", warned, test.warnedVariables)
			}
		})
	}
}
