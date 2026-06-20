package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Load reads configuration from environment variables, applies defaults,
// and validates the result. Returns an error if required values are missing
// or constraints are violated.
//
// Example:
//
//	cfg, err := config.Load()
//	if err != nil {
//	    log.Fatal("Invalid configuration:", err)
//	}
//	fmt.Println("Artwork directory:", cfg.ArtworkDir)
func Load() (*Config, error) {
	cfg := loadDefaults()

	for _, parse := range []func(*Config) error{
		parseTVIPs,
		parseManualBrightness,
		parseSolarCoordinates,
	} {
		if err := parse(cfg); err != nil {
			return nil, err
		}
	}

	cfg.SlideshowOverride = slideshowOverridden()

	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadDefaults builds a Config from environment variables, falling back to
// documented defaults. It performs no validation and never fails; dynamic
// fields that can error (IPs, coordinates) are parsed by dedicated helpers.
func loadDefaults() *Config {
	return &Config{
		ArtworkDir:        envStr("ARTWORK_DIR", "/data/artwork"),
		MaxArtworkImages:  envInt("MAX_ARTWORK_IMAGES", 0),
		MaxDownloadSizeMB: envInt("MAX_DOWNLOAD_SIZE_MB", 20),
		TokenDir:          envStr("TOKEN_DIR", "/data/tokens"),
		SyncIntervalMin:   envInt("SYNC_INTERVAL_MINUTES", 5),
		MatteStyle:        envStr("MATTE_STYLE", "none"),
		ClientName:        envStr("CLIENT_NAME", "Frame Art Manager"),
		DryRun:            envBool("DRY_RUN"),
		LogLevel:          strings.ToLower(envStr("LOG_LEVEL", "info")),

		SlideshowEnabled:  envBool("SLIDESHOW_ENABLED"),
		SlideshowInterval: envInt("SLIDESHOW_INTERVAL", 15),
		SlideshowType:     strings.ToLower(envStr("SLIDESHOW_TYPE", "shuffle")),

		SolarEnabled:        envBool("SOLAR_BRIGHTNESS_ENABLED"),
		Timezone:            envStr("LOCATION_TIMEZONE", "UTC"),
		BrightnessMin:       envInt("BRIGHTNESS_MIN", 2),
		BrightnessMax:       envInt("BRIGHTNESS_MAX", 10),
		RemoveUnknownImages: envBool("REMOVE_UNKNOWN_IMAGES"),
		AutoOffTime:         envStr("AUTO_OFF_TIME", ""),
		AutoOffGraceHours:   envFloat("AUTO_OFF_GRACE_HOURS", 2),
		TVMAC:               envStr("TV_MAC", ""),
		EnableRESTGate:      envBool("ENABLE_REST_GATE"),
		// SKIP_TLS_VERIFY, when true, forces verification off regardless of VERIFY_TLS.
		VerifyTLS:           envBool("VERIFY_TLS") && !envBool("SKIP_TLS_VERIFY"),
		SourcesFile:         envStr("ARTWORK_SOURCES_FILE", ""),
		UnsplashAppID:       envStr("UNSPLASH_APP_ID", ""),
		UnsplashAccessKey:   envStr("UNSPLASH_ACCESS_KEY", ""),
		UnsplashSecretKey:   envStr("UNSPLASH_SECRET_KEY", ""),
		NasaAPIKey:          envStr("NASA_API_KEY", "DEMO_KEY"),
		PexelsAPIKey:        envStr("PEXELS_API_KEY", ""),
		PixabayAPIKey:       envStr("PIXABAY_API_KEY", ""),
		OptimizeEnabled:     envBoolWithDefault("IMAGE_OPTIMIZE_ENABLED", true),
		SmartCropEnabled:    envBoolWithDefault("SMART_CROP_ENABLED", false),
		OptimizeMaxWidth:    envInt("IMAGE_MAX_WIDTH", 3840),
		OptimizeMaxHeight:   envInt("IMAGE_MAX_HEIGHT", 2160),
		OptimizeJPEGQuality: envInt("IMAGE_JPEG_QUALITY", 95),
		MuseumModeEnabled:   envBoolWithDefault("IMAGE_MUSEUM_MODE", false),
		MuseumModeIntensity: envInt("IMAGE_MUSEUM_INTENSITY", 5),
		HealthPort:          envInt("HEALTH_PORT", 8080),
		UploadEnabled:       envBool("UPLOAD_ENABLED"),
		ConnectionTimeout:   time.Duration(envInt("CONNECTION_TIMEOUT_SECONDS", 60)) * time.Second,
		APITimeout:          time.Duration(envInt("API_TIMEOUT_SECONDS", 60)) * time.Second,
		UploadDelay:         time.Duration(envInt("UPLOAD_DELAY_MS", 3000)) * time.Millisecond,
		UploadAttempts:      envInt("UPLOAD_ATTEMPTS", 3),
		GateTimeout:         time.Duration(envInt("GATE_TIMEOUT_MS", 10000)) * time.Millisecond,
		PUID:                envInt("PUID", 0),
		PGID:                envInt("PGID", 0),
		PortraitMode:        strings.ToLower(envStr("PORTRAIT_MODE", "crop")),
	}
}

// parseTVIPs extracts the required, comma-separated TV_IPS list, trimming
// whitespace and rejecting an empty result.
func parseTVIPs(cfg *Config) error {
	raw := os.Getenv("TV_IPS")
	if raw == "" {
		return fmt.Errorf("TV_IPS environment variable is required")
	}
	for _, ip := range strings.Split(raw, ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			cfg.TVIPs = append(cfg.TVIPs, ip)
		}
	}
	if len(cfg.TVIPs) == 0 {
		return fmt.Errorf("TV_IPS must contain at least one non-empty IP address")
	}
	return nil
}

// parseManualBrightness reads the optional BRIGHTNESS override into a pointer
// so an explicit value is distinguishable from "unset".
func parseManualBrightness(cfg *Config) error {
	v := os.Getenv("BRIGHTNESS")
	if v == "" {
		return nil
	}
	b, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("invalid BRIGHTNESS value %q: %w", v, err)
	}
	cfg.ManualBrightness = &b
	return nil
}

// parseSolarCoordinates reads optional latitude/longitude overrides. Presence
// is enforced later by validate when solar brightness is enabled.
func parseSolarCoordinates(cfg *Config) error {
	if v := os.Getenv("LOCATION_LATITUDE"); v != "" {
		lat, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("invalid LOCATION_LATITUDE %q: %w", v, err)
		}
		cfg.Latitude = &lat
	}
	if v := os.Getenv("LOCATION_LONGITUDE"); v != "" {
		lon, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("invalid LOCATION_LONGITUDE %q: %w", v, err)
		}
		cfg.Longitude = &lon
	}
	return nil
}

// slideshowOverridden reports whether any slideshow env var was explicitly set,
// in which case the manager overrides the TV's current slideshow settings.
func slideshowOverridden() bool {
	return os.Getenv("SLIDESHOW_ENABLED") != "" ||
		os.Getenv("SLIDESHOW_INTERVAL") != "" ||
		os.Getenv("SLIDESHOW_TYPE") != ""
}

// validate enforces cross-field constraints and enumerated value membership.
func validate(cfg *Config) error {
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
