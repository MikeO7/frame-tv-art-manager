package config

import (
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

// RotationInterval returns how long each artwork stays on a full TV before
// rotation may replace it. Zero disables rotation.
// Slideshow modes: the manager changes the picture itself, or the TV's own
// slideshow setting is managed.
const (
	SlideshowModeApp = "app"
	SlideshowModeTV  = "tv"
)

// ShowInterval is how long the manager leaves each picture on screen before
// selecting the next one. It is zero when the TV decides what is shown:
// SLIDESHOW_MODE=tv, or SLIDESHOW_ENABLED=false.
func (c *Config) ShowInterval() time.Duration {
	if c.SlideshowMode != SlideshowModeApp || (c.SlideshowEnabledSet && !c.SlideshowEnabled) {
		return 0
	}
	return time.Duration(c.SlideshowInterval) * time.Minute
}

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
		PadStyle:                       c.PadStyle,
		MaxCropPercent:                 c.MaxCropPercent,
		MaxUpscale:                     c.MaxUpscale,
		MatTone:                        c.MatTone,
		JPEGSubsampling:                jpegSubsampling(c.JPEGSubsampling),
	}
}

// jpegSubsampling maps the validated IMAGE_JPEG_SUBSAMPLING value to the
// encoder setting; anything other than "420" keeps full-resolution chroma.
func jpegSubsampling(name string) jpegenc.Subsampling {
	if name == jpegenc.Subsampling420.String() {
		return jpegenc.Subsampling420
	}
	return jpegenc.Subsampling444
}
