package optimize

import "time"

// DefaultConfig is the transform fixture used across package tests. It must
// match the production defaults derived by config.OptimizeOptions; see
// TestDefaultConfigMatchesProductionDefaults.
func DefaultConfig() Config {
	return Config{
		Enabled:                        true,
		SmartCropEnabled:               false,
		SmartCropMinGain:               0.03,
		SmartCropProtection:            true,
		SmartCropProtectionStrength:    0.35,
		SmartCropProvider:              "local",
		SmartCropProviderMinConfidence: 0.7,
		SmartCropProviderTimeout:       8 * time.Second,
		MaxWidth:                       3840,
		MaxHeight:                      2160,
		MaxOutputPixels:                defaultMaxOutputPixels,
		MaxWorkingBytes:                512 * 1024 * 1024,
		OptimizeJPEGQuality:            95,
		OptimizePNG:                    true,
		LinearLightResize:              true,
		SharpenAmount:                  0.25,
		SharpenThreshold:               4,
		ColorProfilePolicy:             profileConvertSRGB,
		HDRToneMap:                     true,
		HDRSourcePeakNits:              1000,
		HDRTargetPeakNits:              100,
		MuseumModeEnabled:              false,
		MuseumModeIntensity:            5,
		PortraitMode:                   "crop",
	}
}
