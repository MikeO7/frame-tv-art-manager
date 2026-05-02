package config

import (
	"os"
	"testing"
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
}
