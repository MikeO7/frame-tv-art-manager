package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MatteConfig holds per-image matte overrides loaded from a mattes.json
// file in the artwork directory.
//
// File format:
//
//	{
//	  "sunset.jpg": "shadowbox_polar",
//	  "portrait.jpg": "modern_apricot",
//	  "_default": "none"
//	}
//
// If a file has no entry, the _default is used. If no _default is set,
// the global MATTE_STYLE from config is used.
type MatteConfig struct {
	overrides    map[string]string
	defaultMatte string
}

// LoadMatteConfig reads a mattes.json file from the artwork directory.
// Returns a no-op config if the file doesn't exist.
func LoadMatteConfig(artworkDir string) *MatteConfig {
	mc := &MatteConfig{
		overrides: make(map[string]string),
	}

	path := filepath.Join(artworkDir, "mattes.json")
	raw, err := os.ReadFile(filepath.Clean(path)) //nolint:gosec // Path is controlled
	if err != nil {
		return mc // file doesn't exist, use global matte
	}

	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		return mc
	}

	for k, v := range data {
		if k == "_default" {
			mc.defaultMatte = v
		} else {
			mc.overrides[k] = v
		}
	}

	return mc
}

// GetMatte returns the matte style for a specific filename.
// Priority: per-file override > mattes.json _default > globalMatte.
func (mc *MatteConfig) GetMatte(filename, globalMatte string) string {
	if matte, ok := mc.overrides[filename]; ok {
		return matte
	}
	if mc.defaultMatte != "" {
		return mc.defaultMatte
	}
	return globalMatte
}

// String returns a summary of the matte configuration for logging.
func (mc *MatteConfig) String() string {
	if len(mc.overrides) == 0 && mc.defaultMatte == "" {
		return "global (no per-file overrides)"
	}
	return fmt.Sprintf("%d per-file overrides, default=%q",
		len(mc.overrides), mc.defaultMatte)
}
