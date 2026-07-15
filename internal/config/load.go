package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	colorProfileAssumeSRGB  = "assume-srgb"
	colorProfileConvertSRGB = "convert-srgb"
	colorProfileReject      = "reject-embedded"
	smartCropProviderLocal  = "local"
	smartCropProviderHTTP   = "http"
)

// Load reads configuration from environment variables, applies defaults,
// and validates the result. Returns an error if required values are missing
// or constraints are violated.
func Load() (*Config, error) {
	cfg, _, err := LoadWithWarnings()
	return cfg, err
}

// LoadWithWarnings reads and validates the process environment and returns
// compatibility warnings for the composition root to report.
func LoadWithWarnings() (*Config, []Warning, error) {
	return LoadEnviron(os.Environ())
}

// validate enforces cross-field constraints and enumerated value membership.
func validate(cfg *Config) error {
	if err := validateRanges(cfg); err != nil {
		return err
	}
	if err := validateLocations(cfg); err != nil {
		return err
	}
	if cfg.BrightnessMin >= cfg.BrightnessMax {
		return fmt.Errorf(
			"BRIGHTNESS_MIN (%d) must be less than BRIGHTNESS_MAX (%d)",
			cfg.BrightnessMin, cfg.BrightnessMax,
		)
	}

	if cfg.SolarEnabled && (cfg.Latitude == nil || cfg.Longitude == nil) {
		return fmt.Errorf(
			"LOCATION_LATITUDE and LOCATION_LONGITUDE are required when SOLAR_BRIGHTNESS_ENABLED=true",
		)
	}
	if err := validateSecuritySettings(cfg); err != nil {
		return err
	}
	return validateEnums(cfg)
}

func validateSecuritySettings(cfg *Config) error {
	if cfg.UploadEnabled && len(cfg.UploadToken) < 16 {
		return fmt.Errorf("UPLOAD_TOKEN must contain at least 16 characters when UPLOAD_ENABLED=true")
	}
	if cfg.SourcesFile == "" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(cfg.SourcesFile)) {
	case ".jpg", ".jpeg", ".png":
		return fmt.Errorf("ARTWORK_SOURCES_FILE must not use a supported artwork extension")
	default:
		return nil
	}
}

func validateEnums(cfg *Config) error {
	if !supportedSlideshowInterval(cfg.SlideshowInterval) {
		return fmt.Errorf("SLIDESHOW_INTERVAL must be one of 3, 15, 60, 720, or 1440; got %d", cfg.SlideshowInterval)
	}
	switch cfg.SlideshowType {
	case "shuffle", "sequential":
	default:
		return fmt.Errorf("SLIDESHOW_TYPE must be 'shuffle' or 'sequential', got %q", cfg.SlideshowType)
	}

	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error; got %q", cfg.LogLevel)
	}

	switch cfg.PortraitMode {
	case "collage", "pad", "crop":
	default:
		return fmt.Errorf("PORTRAIT_MODE must be one of collage, pad, crop; got %q", cfg.PortraitMode)
	}

	return nil
}

func supportedSlideshowInterval(value int) bool {
	switch value {
	case 3, 15, 60, 720, 1440:
		return true
	default:
		return false
	}
}

func normalizePaths(cfg *Config) error {
	paths := []*string{&cfg.ArtworkDir, &cfg.TokenDir}
	if cfg.SourcesFile != "" {
		paths = append(paths, &cfg.SourcesFile)
	}
	for _, path := range paths {
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return fmt.Errorf("normalize path %q: %w", *path, err)
		}
		*path = filepath.Clean(absolute)
	}
	return nil
}

func validateRanges(cfg *Config) error {
	tests := []struct {
		valid bool
		name  string
	}{
		{cfg.MaxArtworkImages >= 0, "MAX_ARTWORK_IMAGES must be non-negative"},
		{cfg.MaxArtworkImages <= 100_000, "MAX_ARTWORK_IMAGES must not exceed 100000"},
		{between(cfg.MaxDownloadSizeMB, 1, 100), "MAX_DOWNLOAD_SIZE_MB must be between 1 and 100"},
		{between(cfg.SyncIntervalMin, 1, 43_200), "SYNC_INTERVAL_MINUTES must be between 1 and 43200"},
		{cfg.ShutdownTimeout > 0 && cfg.ShutdownTimeout <= 10*time.Minute, "SHUTDOWN_TIMEOUT_SECONDS must be between 1 and 600"},
		{between(cfg.SlideshowInterval, 1, 1440), "SLIDESHOW_INTERVAL must be between 1 and 1440"},
		{cfg.AutoOffGraceHours > 0 && cfg.AutoOffGraceHours <= 168, "AUTO_OFF_GRACE_HOURS must be between 0 and 168"},
		{between(cfg.OptimizeMaxWidth, 1, 16384), "IMAGE_MAX_WIDTH must be between 1 and 16384"},
		{between(cfg.OptimizeMaxHeight, 1, 16384), "IMAGE_MAX_HEIGHT must be between 1 and 16384"},
		{between(cfg.OptimizeMaxPixels, 1, 100_000_000), "IMAGE_MAX_OUTPUT_PIXELS must be between 1 and 100000000"},
		{
			int64(cfg.OptimizeMaxWidth)*int64(cfg.OptimizeMaxHeight) <= int64(cfg.OptimizeMaxPixels),
			"target image dimensions exceed IMAGE_MAX_OUTPUT_PIXELS",
		},
		{between(cfg.OptimizeMemoryMB, 128, 4096), "IMAGE_MAX_WORKING_MEMORY_MB must be between 128 and 4096"},
		{between(cfg.OptimizeJPEGQuality, 1, 100), "IMAGE_JPEG_QUALITY must be between 1 and 100"},
		{betweenFloat(cfg.SmartCropMinGain, 0, 1), "SMART_CROP_MIN_GAIN must be between 0 and 1"},
		{betweenFloat(cfg.SmartCropProtectionStrength, 0, 2), "SMART_CROP_PROTECTION_STRENGTH must be between 0 and 2"},
		{supportedSmartCropProvider(cfg.SmartCropProvider), "SMART_CROP_PROVIDER must be local or http"},
		{
			cfg.SmartCropProvider != smartCropProviderHTTP || validHTTPURL(cfg.SmartCropProviderURL),
			"SMART_CROP_PROVIDER_URL must be an absolute HTTP(S) URL for http provider",
		},
		{betweenFloat(cfg.SmartCropProviderMinConfidence, 0, 1), "SMART_CROP_PROVIDER_MIN_CONFIDENCE must be between 0 and 1"},
		{cfg.SmartCropProviderTimeout > 0 && cfg.SmartCropProviderTimeout <= time.Minute, "SMART_CROP_PROVIDER_TIMEOUT_SECONDS must be between 1 and 60"},
		{betweenFloat(cfg.SharpenAmount, 0, 2), "IMAGE_SHARPEN_AMOUNT must be between 0 and 2"},
		{between(cfg.SharpenThreshold, 0, 255), "IMAGE_SHARPEN_THRESHOLD must be between 0 and 255"},
		{supportedColorProfilePolicy(cfg.ColorProfilePolicy), "IMAGE_COLOR_PROFILE_POLICY must be convert-srgb, assume-srgb, or reject-embedded"},
		{betweenFloat(cfg.HDRSourcePeakNits, 100, 10_000), "IMAGE_HDR_SOURCE_PEAK_NITS must be between 100 and 10000"},
		{betweenFloat(cfg.HDRTargetPeakNits, 80, 500), "IMAGE_HDR_TARGET_PEAK_NITS must be between 80 and 500"},
		{between(cfg.PerceptualDuplicateDistance, 0, 64), "IMAGE_PERCEPTUAL_DUPLICATE_DISTANCE must be between 0 and 64"},
		{between(cfg.MuseumModeIntensity, 1, 10), "IMAGE_MUSEUM_INTENSITY must be between 1 and 10"},
		{between(cfg.HealthPort, 0, 65535), "HEALTH_PORT must be between 0 and 65535"},
		{cfg.ConnectionTimeout > 0 && cfg.ConnectionTimeout <= 10*time.Minute, "CONNECTION_TIMEOUT_SECONDS must be between 1 and 600"},
		{cfg.APITimeout > 0 && cfg.APITimeout <= 30*time.Minute, "API_TIMEOUT_SECONDS must be between 1 and 1800"},
		{cfg.UploadDelay >= 0 && cfg.UploadDelay <= 5*time.Minute, "UPLOAD_DELAY_MS must be between 0 and 300000"},
		{between(cfg.UploadAttempts, 1, 10), "UPLOAD_ATTEMPTS must be between 1 and 10"},
		{cfg.GateTimeout > 0 && cfg.GateTimeout <= time.Minute, "GATE_TIMEOUT_MS must be between 1 and 60000"},
		{cfg.PUID >= 0, "PUID must be non-negative"},
		{cfg.PGID >= 0, "PGID must be non-negative"},
	}
	for _, test := range tests {
		if !test.valid {
			return errors.New(test.name)
		}
	}
	return validateBrightnessRanges(cfg)
}

func validateBrightnessRanges(cfg *Config) error {
	if cfg.ManualBrightness != nil && (*cfg.ManualBrightness < 0 || *cfg.ManualBrightness > 50) {
		return fmt.Errorf("BRIGHTNESS must be between 0 and 50")
	}
	if cfg.BrightnessMin < 0 || cfg.BrightnessMax > 50 {
		return fmt.Errorf("BRIGHTNESS_MIN and BRIGHTNESS_MAX must be between 0 and 50")
	}
	return nil
}

func validateLocations(cfg *Config) error {
	if err := validateAddresses(cfg); err != nil {
		return err
	}
	if err := validateCoordinates(cfg); err != nil {
		return err
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return fmt.Errorf("LOCATION_TIMEZONE %q: %w", cfg.Timezone, err)
	}
	if cfg.AutoOffTime != "" {
		if _, err := time.Parse("15:04", cfg.AutoOffTime); err != nil {
			return fmt.Errorf("AUTO_OFF_TIME %q: %w", cfg.AutoOffTime, err)
		}
	}
	if cfg.TVMAC != "" {
		if _, err := net.ParseMAC(cfg.TVMAC); err != nil {
			return fmt.Errorf("TV_MAC %q: %w", cfg.TVMAC, err)
		}
	}
	return nil
}

func validateAddresses(cfg *Config) error {
	if net.ParseIP(cfg.HealthBindAddress) == nil {
		return fmt.Errorf("HEALTH_BIND_ADDRESS must be an IP address")
	}
	seen := make(map[string]struct{}, len(cfg.TVIPs))
	for index, address := range cfg.TVIPs {
		parsed := net.ParseIP(address)
		if parsed == nil {
			return fmt.Errorf("TV_IPS contains invalid IP address %q", address)
		}
		canonical := parsed.String()
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("TV_IPS contains duplicate address %q", canonical)
		}
		seen[canonical] = struct{}{}
		cfg.TVIPs[index] = canonical
	}
	return nil
}

func validateCoordinates(cfg *Config) error {
	if cfg.Latitude != nil && (!isFinite(*cfg.Latitude) || *cfg.Latitude < -90 || *cfg.Latitude > 90) {
		return fmt.Errorf("LOCATION_LATITUDE must be between -90 and 90")
	}
	if cfg.Longitude != nil && (!isFinite(*cfg.Longitude) || *cfg.Longitude < -180 || *cfg.Longitude > 180) {
		return fmt.Errorf("LOCATION_LONGITUDE must be between -180 and 180")
	}
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func between(value, minimum, maximum int) bool {
	return value >= minimum && value <= maximum
}

func betweenFloat(value, minimum, maximum float64) bool {
	return value >= minimum && value <= maximum
}

func supportedSmartCropProvider(value string) bool {
	switch value {
	case smartCropProviderLocal, smartCropProviderHTTP:
		return true
	default:
		return false
	}
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == smartCropProviderHTTP || parsed.Scheme == "https")
}

func supportedColorProfilePolicy(value string) bool {
	switch value {
	case colorProfileConvertSRGB, colorProfileAssumeSRGB, colorProfileReject:
		return true
	default:
		return false
	}
}
