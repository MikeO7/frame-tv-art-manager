//nolint:goconst
package sync

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
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
	if err := os.WriteFile(filepath.Join(dir, "mattes.json"), []byte(content), 0644); err != nil { //nolint:gosec // Test file
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
	if err := os.WriteFile(filepath.Join(dir, "mattes.json"), []byte("not json"), 0644); err != nil { //nolint:gosec // Test file
		t.Fatal(err)
	}

	// Should not panic — falls through to global.
	mc := LoadMatteConfig(dir)
	got := mc.GetMatte("photo.jpg", shadowboxWarm)
	if got != shadowboxWarm {
		t.Errorf("invalid JSON should fall back to global matte, got %q", got)
	}
}

func TestMappingRoundtrip(t *testing.T) {
	dir := t.TempDir()

	m, err := LoadMapping(dir, "192.168.1.100")
	if err != nil {
		t.Fatalf("LoadMapping: %v", err)
	}

	m.Set("photo.jpg", "cid001")
	m.Set("sunset.jpg", "cid002")

	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify.
	m2, err := LoadMapping(dir, "192.168.1.100")
	if err != nil {
		t.Fatalf("reload LoadMapping: %v", err)
	}

	cid, ok := m2.GetContentID("photo.jpg")
	if !ok || cid != "cid001" {
		t.Errorf("photo.jpg: got (%q, %v), want (cid001, true)", cid, ok)
	}

	m2.Delete("photo.jpg")
	if _, ok := m2.GetContentID("photo.jpg"); ok {
		t.Error("photo.jpg should be deleted")
	}

	all := m2.AllContentIDs()
	if len(all) != 1 {
		t.Errorf("expected 1 entry after delete, got %d", len(all))
	}
}

func TestDiffSets(t *testing.T) {
	a := map[string]struct{}{"1": {}, "2": {}, "3": {}}
	b := map[string]struct{}{"2": {}, "4": {}}

	got := diffSets(a, b)
	if _, ok := got["1"]; !ok {
		t.Error("expected 1 in diff")
	}
	if _, ok := got["3"]; !ok {
		t.Error("expected 3 in diff")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 items, got %d", len(got))
	}
}

func TestSetToSlice(t *testing.T) {
	s := map[string]struct{}{"a": {}, "b": {}}
	got := setToSlice(s)
	if len(got) != 2 {
		t.Errorf("expected length 2, got %d", len(got))
	}
}

func TestMapValues(t *testing.T) {
	m := map[string]string{"k1": "v1", "k2": "v2"}
	got := mapValues(m)
	if len(got) != 2 {
		t.Errorf("expected length 2, got %d", len(got))
	}
}

func TestBoolCount(t *testing.T) {
	if boolCount(true, 5) != 5 {
		t.Error("expected 5 when true")
	}
	if boolCount(false, 5) != 0 {
		t.Error("expected 0 when false")
	}
}

func TestDetermineBrightness(t *testing.T) {
	manual := 5
	cfg := &config.Config{
		ManualBrightness: &manual,
		SolarEnabled:     false,
	}
	e := &Engine{cfg: cfg, logger: slog.Default()}

	got := e.determineBrightness(slog.Default())
	if got == nil || *got != 5 {
		t.Errorf("expected 5, got %v", got)
	}

	cfg.ManualBrightness = nil
	got = e.determineBrightness(slog.Default())
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}
