package config

import (
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

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
