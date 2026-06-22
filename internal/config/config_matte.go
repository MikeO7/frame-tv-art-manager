package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MatteConfig holds per-image matte overrides loaded from a mattes.json file.
type MatteConfig struct {
	Overrides    map[string]string
	DefaultMatte string
}

// LoadMatteConfig reads a mattes.json file from the artwork directory.
func LoadMatteConfig(artworkDir string) *MatteConfig {
	mc := &MatteConfig{
		Overrides: make(map[string]string),
	}

	path := filepath.Join(artworkDir, "mattes.json")
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return mc
	}

	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		return mc
	}

	for k, v := range data {
		if k == "_default" {
			mc.DefaultMatte = v
		} else {
			mc.Overrides[k] = v
		}
	}

	return mc
}

// GetMatte returns the matte style for a specific filename.
func (mc *MatteConfig) GetMatte(filename, globalMatte string) string {
	if matte, ok := mc.Overrides[filename]; ok {
		return matte
	}
	if mc.DefaultMatte != "" {
		return mc.DefaultMatte
	}
	return globalMatte
}

// String returns a summary of the matte configuration for logging.
func (mc *MatteConfig) String() string {
	if len(mc.Overrides) == 0 && mc.DefaultMatte == "" {
		return "global (no per-file overrides)"
	}
	return fmt.Sprintf("%d per-file overrides, default=%q", len(mc.Overrides), mc.DefaultMatte)
}
