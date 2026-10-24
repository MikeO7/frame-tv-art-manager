package config

import (
	"strings"
	"testing"
	"time"

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
	}
	if opts != want {
		t.Errorf("OptimizeOptions() = %+v, want %+v", opts, want)
	}
}
