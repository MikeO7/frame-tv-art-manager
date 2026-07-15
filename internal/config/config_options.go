package config

import "github.com/MikeO7/frame-tv-art-manager/internal/optimize"

// OptimizeOptions returns image optimization settings derived from config.
func (c *Config) OptimizeOptions() optimize.Config {
	return optimize.Config{
		Enabled:             c.OptimizeEnabled,
		SmartCropEnabled:    c.SmartCropEnabled,
		SmartCropMinGain:    c.SmartCropMinGain,
		MaxWidth:            c.OptimizeMaxWidth,
		MaxHeight:           c.OptimizeMaxHeight,
		MaxOutputPixels:     int64(c.OptimizeMaxPixels),
		MaxWorkingBytes:     int64(c.OptimizeMemoryMB) * 1024 * 1024,
		OptimizeJPEGQuality: c.OptimizeJPEGQuality,
		OptimizePNG:         c.OptimizePNG,
		LinearLightResize:   c.LinearLightResize,
		SharpenAmount:       c.SharpenAmount,
		SharpenThreshold:    c.SharpenThreshold,
		DitherEnabled:       c.DitherEnabled,
		ColorProfilePolicy:  c.ColorProfilePolicy,
		MuseumModeEnabled:   c.MuseumModeEnabled,
		MuseumModeIntensity: c.MuseumModeIntensity,
		PortraitMode:        c.PortraitMode,
	}
}
