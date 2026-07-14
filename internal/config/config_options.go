package config

import "github.com/MikeO7/frame-tv-art-manager/internal/optimize"

// OptimizeOptions returns image optimization settings derived from config.
func (c *Config) OptimizeOptions() optimize.Config {
	return optimize.Config{
		Enabled:             c.OptimizeEnabled,
		SmartCropEnabled:    c.SmartCropEnabled,
		MaxWidth:            c.OptimizeMaxWidth,
		MaxHeight:           c.OptimizeMaxHeight,
		OptimizeJPEGQuality: c.OptimizeJPEGQuality,
		MuseumModeEnabled:   c.MuseumModeEnabled,
		MuseumModeIntensity: c.MuseumModeIntensity,
		PortraitMode:        c.PortraitMode,
	}
}
