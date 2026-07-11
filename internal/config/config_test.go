package config

import (
	"os"
	"path/filepath"
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

func TestEnvHelpers(t *testing.T) {
	os.Clearenv()
	t.Setenv("STR", "hello")
	t.Setenv("INT", "123")
	t.Setenv("BOOL", "true")
	t.Setenv("FLOAT", "1.23")

	if envStr("STR", "def") != "hello" {
		t.Error("envStr failed")
	}
	if envStr("MISSING", "def") != "def" {
		t.Error("envStr default failed")
	}

	if envInt("INT", 0) != 123 {
		t.Error("envInt failed")
	}
	if envInt("MISSING", 456) != 456 {
		t.Error("envInt default failed")
	}
	if envInt("STR", 789) != 789 { // invalid int should return default
		t.Error("envInt invalid failed")
	}

	if !envBool("BOOL") {
		t.Error("envBool failed")
	}
	if envBool("MISSING") {
		t.Error("envBool missing failed")
	}

	if envFloat("FLOAT", 0) != 1.23 {
		t.Error("envFloat failed")
	}
	if envFloat("MISSING", 4.56) != 4.56 {
		t.Error("envFloat default failed")
	}

	if envBoolWithDefault("MISSING", true) != true {
		t.Error("envBoolWithDefault missing should return default true")
	}
	t.Setenv("BOOL_DEFAULT", "yes")
	if !envBoolWithDefault("BOOL_DEFAULT", false) {
		t.Error("envBoolWithDefault yes failed")
	}
	t.Setenv("BOOL_DEFAULT", "maybe")
	if envBoolWithDefault("BOOL_DEFAULT", true) != true {
		t.Error("envBoolWithDefault invalid should return default")
	}
	t.Setenv("BOOL_DEFAULT", "0")
	if envBoolWithDefault("BOOL_DEFAULT", true) {
		t.Error("envBoolWithDefault 0 should be false")
	}
	t.Setenv("BOOL_DEFAULT", "NO")
	if envBoolWithDefault("BOOL_DEFAULT", true) {
		t.Error("envBoolWithDefault should be case insensitive")
	}
	t.Setenv("FLOAT_INVALID", "not-a-float")
	if envFloat("FLOAT_INVALID", 9.5) != 9.5 {
		t.Error("envFloat invalid should return default")
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

func TestConfig_SyncPolicy(t *testing.T) {
	cfg := &Config{
		DryRun:              true,
		RemoveUnknownImages: true,
		SlideshowOverride:   true,
		ArtworkDir:          "/art",
		UploadDelay:         2 * time.Second,
		UploadAttempts:      0,
		SyncIntervalMin:     10,
		MatteStyle:          "shadowbox_warm",
	}

	policy := cfg.SyncPolicy()
	if !policy.DryRun || !policy.RemoveUnknownImages || !policy.SlideshowOverride {
		t.Error("expected boolean flags to propagate")
	}
	if policy.ArtworkDir != "/art" {
		t.Errorf("ArtworkDir = %q", policy.ArtworkDir)
	}
	if policy.UploadAttempts != 1 {
		t.Errorf("UploadAttempts should default to 1, got %d", policy.UploadAttempts)
	}
	if policy.UploadDelay != 2*time.Second {
		t.Errorf("UploadDelay = %v", policy.UploadDelay)
	}
	if policy.MatteStyle != "shadowbox_warm" {
		t.Errorf("MatteStyle = %q", policy.MatteStyle)
	}
}

func TestConfig_TVConnectOptions(t *testing.T) {
	cfg := &Config{
		TVMAC:             "aa:bb:cc:dd:ee:ff",
		EnableRESTGate:    true,
		ClientName:        "Test Client",
		TokenDir:          "/tokens",
		ConnectionTimeout: 30 * time.Second,
		APITimeout:        45 * time.Second,
		GateTimeout:       5 * time.Second,
		MatteStyle:        "none",
	}

	opts := cfg.TVConnectOptions()
	if opts.TVMAC != cfg.TVMAC || !opts.EnableRESTGate {
		t.Error("expected TV MAC and REST gate to propagate")
	}
	if opts.ClientName != "Test Client" || opts.TokenDir != "/tokens" {
		t.Error("expected client metadata to propagate")
	}
	if opts.ConnectionTimeout != 30*time.Second || opts.APITimeout != 45*time.Second {
		t.Error("expected timeouts to propagate")
	}
}

func TestConfig_OptimizeOptions(t *testing.T) {
	cfg := &Config{
		OptimizeEnabled:     true,
		SmartCropEnabled:    true,
		OptimizeMaxWidth:    3840,
		OptimizeMaxHeight:   2160,
		OptimizeJPEGQuality: 90,
		MuseumModeEnabled:   true,
		MuseumModeIntensity: 7,
	}

	opts := cfg.OptimizeOptions()
	want := optimize.Config{
		Enabled:             true,
		SmartCropEnabled:    true,
		MaxWidth:            3840,
		MaxHeight:           2160,
		OptimizeJPEGQuality: 90,
		MuseumModeEnabled:   true,
		MuseumModeIntensity: 7,
	}
	if opts != want {
		t.Errorf("OptimizeOptions() = %+v, want %+v", opts, want)
	}
}

const shadowboxWarm = "shadowbox_warm"

func TestLoadMatteConfig_NoFile(t *testing.T) {
	mc := LoadMatteConfig(t.TempDir())
	got := mc.GetMatte("photo.jpg", shadowboxWarm)
	if got != shadowboxWarm {
		t.Errorf("expected global matte fallback, got %q", got)
	}
}

func TestLoadMatteConfig_WithOverrides(t *testing.T) {
	dir := t.TempDir()
	content := `{
		"sunset.jpg": "shadowbox_polar",
		"portrait.jpg": "modern_apricot",
		"_default": "none"
	}`
	if err := os.WriteFile(filepath.Join(dir, "mattes.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	mc := LoadMatteConfig(dir)

	tests := []struct {
		name     string
		file     string
		global   string
		expected string
	}{
		{"per-file override wins", "sunset.jpg", shadowboxWarm, "shadowbox_polar"},
		{"second override", "portrait.jpg", shadowboxWarm, "modern_apricot"},
		{"_default used when no file match", "mountain.jpg", shadowboxWarm, "none"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mc.GetMatte(tc.file, tc.global)
			if got != tc.expected {
				t.Errorf("GetMatte(%q, %q) = %q, want %q", tc.file, tc.global, got, tc.expected)
			}
		})
	}
}

func TestLoadMatteConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mattes.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Should not panic — falls through to global.
	mc := LoadMatteConfig(dir)
	got := mc.GetMatte("photo.jpg", shadowboxWarm)
	if got != shadowboxWarm {
		t.Errorf("invalid JSON should fall back to global matte, got %q", got)
	}
}

func TestMatteConfig_String(t *testing.T) {
	tests := []struct {
		name         string
		overrides    map[string]string
		defaultMatte string
		want         string
	}{
		{
			name:      "empty",
			overrides: nil,
			want:      "global (no per-file overrides)",
		},
		{
			name:         "only default",
			defaultMatte: "none",
			want:         "0 per-file overrides, default=\"none\"",
		},
		{
			name: "overrides and default",
			overrides: map[string]string{
				"art1.jpg": "matte1",
			},
			defaultMatte: "matte2",
			want:         "1 per-file overrides, default=\"matte2\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MatteConfig{
				Overrides:    tt.overrides,
				DefaultMatte: tt.defaultMatte,
			}
			if got := mc.String(); got != tt.want {
				t.Errorf("MatteConfig.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
