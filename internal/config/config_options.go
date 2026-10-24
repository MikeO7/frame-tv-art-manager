package config

import (
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

// RotationInterval returns how long each artwork stays on a full TV before
// rotation may replace it. Zero disables rotation.
func (c *Config) RotationInterval() time.Duration {
	return time.Duration(c.RotationIntervalDays * float64(24*time.Hour)).Round(time.Second)
}

// OptimizeOptions returns image optimization settings derived from config.
func (c *Config) OptimizeOptions() optimize.Config {
	return optimize.Config{
		Enabled:                        c.OptimizeEnabled,
		SmartCropEnabled:               c.SmartCropEnabled,
		SmartCropMinGain:               c.SmartCropMinGain,
		SmartCropProtection:            c.SmartCropProtection,
		SmartCropProtectionStrength:    c.SmartCropProtectionStrength,
		SmartCropProvider:              c.SmartCropProvider,
		SmartCropProviderURL:           c.SmartCropProviderURL,
		SmartCropProviderMinConfidence: c.SmartCropProviderMinConfidence,
		SmartCropProviderTimeout:       c.SmartCropProviderTimeout,
		MaxWidth:                       c.OptimizeMaxWidth,
		MaxHeight:                      c.OptimizeMaxHeight,
		MaxOutputPixels:                int64(c.OptimizeMaxPixels),
		MaxWorkingBytes:                int64(c.OptimizeMemoryMB) * 1024 * 1024,
		OptimizeJPEGQuality:            c.OptimizeJPEGQuality,
		OptimizePNG:                    c.OptimizePNG,
		LinearLightResize:              c.LinearLightResize,
		SharpenAmount:                  c.SharpenAmount,
		SharpenThreshold:               c.SharpenThreshold,
		ColorProfilePolicy:             c.ColorProfilePolicy,
		HDRToneMap:                     c.HDRToneMap,
		HDRSourcePeakNits:              c.HDRSourcePeakNits,
		HDRTargetPeakNits:              c.HDRTargetPeakNits,
		MuseumModeEnabled:              c.MuseumModeEnabled,
		MuseumModeIntensity:            c.MuseumModeIntensity,
		PortraitMode:                   c.PortraitMode,
	}
}
