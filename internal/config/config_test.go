package config

import (
	"os"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func TestLoad_Success(t *testing.T) {
	os.Clearenv()
	t.Setenv("TV_IPS", "192.168.1.10, 192.168.1.11")
	t.Setenv("ARTWORK_DIR", "/tmp/art")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOCATION_LATITUDE", "40.7128")
	t.Setenv("LOCATION_LONGITUDE", "-74.0060")
	t.Setenv("SOLAR_BRIGHTNESS_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.TVIPs) != 2 {
		t.Errorf("expected 2 IPs, got %d", len(cfg.TVIPs))
	}
	if cfg.TVIPs[0] != "192.168.1.10" {
		t.Errorf("expected 192.168.1.10, got %s", cfg.TVIPs[0])
	}
	if cfg.ArtworkDir != "/tmp/art" {
		t.Errorf("expected /tmp/art, got %s", cfg.ArtworkDir)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected debug, got %s", cfg.LogLevel)
	}
	if cfg.Latitude == nil || *cfg.Latitude != 40.7128 {
		t.Errorf("expected 40.7128 latitude")
	}
}

func TestLoad_MissingTVIPS(t *testing.T) {
	os.Clearenv()
	_, err := Load()
	if err == nil {
		t.Error("expected error due to missing TV_IPS")
	}
}

func TestLoad_InvalidSolar(t *testing.T) {
	os.Clearenv()
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("SOLAR_BRIGHTNESS_ENABLED", "true")
	// Missing lat/lon
	_, err := Load()
	if err == nil {
		t.Error("expected error due to missing solar lat/lon")
	}
}

func TestLoad_InvalidLogLevel(t *testing.T) {
	os.Clearenv()
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("LOG_LEVEL", "invalid")
	_, err := Load()
	if err == nil {
		t.Error("expected error due to invalid log level")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	os.Clearenv()
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("BRIGHTNESS_MIN", "10")
	t.Setenv("BRIGHTNESS_MAX", "5")
	if _, err := Load(); err == nil {
		t.Error("expected brightness min/max validation error")
	}

	os.Clearenv()
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("SLIDESHOW_TYPE", "random")
	if _, err := Load(); err == nil {
		t.Error("expected invalid slideshow type error")
	}

	os.Clearenv()
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("BRIGHTNESS", "not-a-number")
	if _, err := Load(); err == nil {
		t.Error("expected invalid manual brightness error")
	}

	os.Clearenv()
	t.Setenv("TV_IPS", " , , ")
	if _, err := Load(); err == nil {
		t.Error("expected error when TV_IPS has no valid entries")
	}

	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "invalid latitude", key: "LOCATION_LATITUDE", value: "north"},
		{name: "invalid longitude", key: "LOCATION_LONGITUDE", value: "west"},
		{name: "invalid portrait mode", key: "PORTRAIT_MODE", value: "stretch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Clearenv()
			t.Setenv("TV_IPS", "127.0.0.1")
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted %s=%q", tc.key, tc.value)
			}
		})
	}
}

func TestLoad_OptionalFields(t *testing.T) {
	os.Clearenv()
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("BRIGHTNESS", "7")
	t.Setenv("SLIDESHOW_ENABLED", "true")
	t.Setenv("LOCATION_LATITUDE", "51.5")
	t.Setenv("LOCATION_LONGITUDE", "-0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ManualBrightness == nil || *cfg.ManualBrightness != 7 {
		t.Error("expected manual brightness 7")
	}
	if !cfg.SlideshowOverride {
		t.Error("expected slideshow override when SLIDESHOW_ENABLED is set")
	}
	if cfg.Latitude == nil || cfg.Longitude == nil {
		t.Error("expected parsed coordinates")
	}
}

func TestConfig_OptimizeOptions(t *testing.T) {
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
