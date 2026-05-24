package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
