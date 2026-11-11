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

	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
)

const (
	colorProfileAssumeSRGB  = "assume-srgb"
	colorProfileConvertSRGB = "convert-srgb"
	colorProfileReject      = "reject-embedded"
	smartCropProviderLocal  = "local"
	smartCropProviderHTTP   = "http"
)

// LoadWithWarnings reads and validates the process environment and returns
// compatibility warnings for the composition root to report.
func LoadWithWarnings() (*Config, []warning, error) {
	return LoadEnviron(os.Environ())
}

// validate enforces cross-field constraints and enumerated value membership.
// It also canonicalizes TV_IPS and parses mac into cfg.TVMAC so consumers can
// rely on both without revalidating them.
func validate(cfg *Config, mac string) error {
	if err := validateRanges(cfg); err != nil {
		return err
	}
	if err := validateLocations(cfg, mac); err != nil {
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
	if cfg.UploadEnabled && cfg.HealthPort == 0 {
		return errors.New("UPLOAD_ENABLED=true requires HEALTH_PORT, because /upload is served by the health server")
	}
	if err := validateMCPSettings(cfg); err != nil {
		return err
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

// minimumMCPTokenLength keeps the bearer token out of practical guessing
// range even before the per-client failure limit applies.
const minimumMCPTokenLength = 24

func validateMCPSettings(cfg *Config) error {
	if !cfg.MCPEnabled {
		return nil
	}
	if cfg.HealthPort == 0 {
		return errors.New("MCP_ENABLED=true requires HEALTH_PORT, because /mcp is served by the health server")
	}
	if len(cfg.MCPToken) < minimumMCPTokenLength {
		return fmt.Errorf("MCP_TOKEN must contain at least %d characters when MCP_ENABLED=true", minimumMCPTokenLength)
	}
	if strings.ContainsFunc(cfg.MCPToken, func(character rune) bool { return character <= ' ' || character == 0x7f }) {
		return errors.New("MCP_TOKEN must not contain spaces or control characters")
	}
	return nil
}

func validateEnums(cfg *Config) error {
	switch cfg.SlideshowMode {
	case SlideshowModeApp:
	case SlideshowModeTV:
		if !supportedSlideshowInterval(cfg.SlideshowInterval) {
			return fmt.Errorf("SLIDESHOW_INTERVAL must be one of 3, 15, 60, 720, or 1440 when SLIDESHOW_MODE=tv; got %d", cfg.SlideshowInterval)
		}
	default:
		return fmt.Errorf("SLIDESHOW_MODE must be app or tv; got %q", cfg.SlideshowMode)
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

	switch cfg.PadStyle {
	case "mat", "blur":
	default:
		return fmt.Errorf("IMAGE_PAD_STYLE must be mat or blur; got %q", cfg.PadStyle)
	}

	switch cfg.MatTone {
	case "auto", "light", "dark":
	default:
		return fmt.Errorf("IMAGE_MAT_TONE must be auto, light, or dark; got %q", cfg.MatTone)
	}

	if _, err := jpegenc.ParseSubsampling(cfg.JPEGSubsampling); err != nil {
		return fmt.Errorf("IMAGE_JPEG_SUBSAMPLING must be 444 or 420: %w", err)
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
		{between(cfg.MaxLocalFileSizeMB, 1, 512), "MAX_LOCAL_FILE_SIZE_MB must be between 1 and 512"},
		{between(cfg.SyncIntervalMin, 1, 43_200), "SYNC_INTERVAL_MINUTES must be between 1 and 43200"},
		{cfg.ShutdownTimeout > 0 && cfg.ShutdownTimeout <= 10*time.Minute, "SHUTDOWN_TIMEOUT_SECONDS must be between 1 and 600"},
		{between(cfg.SlideshowInterval, 1, 1440), "SLIDESHOW_INTERVAL must be between 1 and 1440"},
		{
			cfg.AutoOffGraceHours > 0 && cfg.AutoOffGraceHours < 24,
			"AUTO_OFF_GRACE_HOURS must be greater than 0 and less than 24; a window of 24 hours or more would keep the TV off all day",
		},
		{
			cfg.AutoOnGraceHours > 0 && cfg.AutoOnGraceHours < 24,
			"AUTO_ON_GRACE_HOURS must be greater than 0 and less than 24; a window of 24 hours or more would never close",
		},
		{len(cfg.MatteStyle) <= 128, "MATTE_STYLE must be at most 128 characters"},
		{len(cfg.PortraitMatte) <= 128, "PORTRAIT_MATTE must be at most 128 characters"},
		{between(cfg.RotationIntervalDays, 0, 365), "ROTATION_INTERVAL_DAYS must be between 0 and 365"},
		{between(cfg.OptimizeMaxWidth, 1, 16384), "IMAGE_MAX_WIDTH must be between 1 and 16384"},
		{between(cfg.OptimizeMaxHeight, 1, 16384), "IMAGE_MAX_HEIGHT must be between 1 and 16384"},
		{between(cfg.OptimizeMaxPixels, 1, 100_000_000), "IMAGE_MAX_OUTPUT_PIXELS must be between 1 and 100000000"},
		{
			int64(cfg.OptimizeMaxWidth)*int64(cfg.OptimizeMaxHeight) <= int64(cfg.OptimizeMaxPixels),
			"target image dimensions exceed IMAGE_MAX_OUTPUT_PIXELS",
		},
		{between(cfg.OptimizeMemoryMB, 128, 4096), "IMAGE_MAX_WORKING_MEMORY_MB must be between 128 and 4096"},
		{between(cfg.OptimizeJPEGQuality, 1, 100), "IMAGE_JPEG_QUALITY must be between 1 and 100"},
		{between(cfg.SmartCropMinGain, 0, 1), "SMART_CROP_MIN_GAIN must be between 0 and 1"},
		{between(cfg.SmartCropProtectionStrength, 0, 2), "SMART_CROP_PROTECTION_STRENGTH must be between 0 and 2"},
		{supportedSmartCropProvider(cfg.SmartCropProvider), "SMART_CROP_PROVIDER must be local or http"},
		{
			cfg.SmartCropProvider != smartCropProviderHTTP || validHTTPURL(cfg.SmartCropProviderURL),
			"SMART_CROP_PROVIDER_URL must be an absolute HTTP(S) URL for http provider",
		},
		{between(cfg.SmartCropProviderMinConfidence, 0, 1), "SMART_CROP_PROVIDER_MIN_CONFIDENCE must be between 0 and 1"},
		{cfg.SmartCropProviderTimeout > 0 && cfg.SmartCropProviderTimeout <= time.Minute, "SMART_CROP_PROVIDER_TIMEOUT_SECONDS must be between 1 and 60"},
		{between(cfg.SharpenAmount, 0, 2), "IMAGE_SHARPEN_AMOUNT must be between 0 and 2"},
		{between(cfg.SharpenThreshold, 0, 255), "IMAGE_SHARPEN_THRESHOLD must be between 0 and 255"},
		{supportedColorProfilePolicy(cfg.ColorProfilePolicy), "IMAGE_COLOR_PROFILE_POLICY must be convert-srgb, assume-srgb, or reject-embedded"},
		{between(cfg.HDRSourcePeakNits, 100, 10_000), "IMAGE_HDR_SOURCE_PEAK_NITS must be between 100 and 10000"},
		{between(cfg.HDRTargetPeakNits, 80, 500), "IMAGE_HDR_TARGET_PEAK_NITS must be between 80 and 500"},
		{between(cfg.MuseumModeIntensity, 1, 10), "IMAGE_MUSEUM_INTENSITY must be between 1 and 10"},
		{between(cfg.MaxCropPercent, 0, 100), "IMAGE_MAX_CROP_PERCENT must be between 0 and 100"},
		{between(cfg.MaxUpscale, 1, 16), "IMAGE_MAX_UPSCALE must be between 1 and 16"},
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

// maxFrameBrightness is the top of the Art Mode brightness scale that Frame
// TVs report through get_artmode_settings.
const maxFrameBrightness = 10

func validateBrightnessRanges(cfg *Config) error {
	if cfg.ManualBrightness != nil && (*cfg.ManualBrightness < 0 || *cfg.ManualBrightness > maxFrameBrightness) {
		return fmt.Errorf("BRIGHTNESS must be between 0 and %d", maxFrameBrightness)
	}
	if cfg.BrightnessMin < 0 || cfg.BrightnessMax > maxFrameBrightness {
		return fmt.Errorf("BRIGHTNESS_MIN and BRIGHTNESS_MAX must be between 0 and %d", maxFrameBrightness)
	}
	return nil
}

func validateLocations(cfg *Config, mac string) error {
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
	if cfg.AutoOnTime != "" {
		if _, err := time.Parse("15:04", cfg.AutoOnTime); err != nil {
			return fmt.Errorf("AUTO_ON_TIME %q: %w", cfg.AutoOnTime, err)
		}
	}
	if cfg.WakeBroadcast != "" {
		if ip := net.ParseIP(cfg.WakeBroadcast); ip == nil || ip.To4() == nil {
			return fmt.Errorf("TV_WOL_BROADCAST %q must be an IPv4 address", cfg.WakeBroadcast)
		}
	}
	if mac == "" {
		return nil
	}
	parsed, err := net.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("TV_MAC %q: %w", mac, err)
	}
	if len(parsed) != 6 {
		return fmt.Errorf("TV_MAC %q must contain six bytes", mac)
	}
	cfg.TVMAC = parsed
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

func between[T int | float64](value, minimum, maximum T) bool {
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
