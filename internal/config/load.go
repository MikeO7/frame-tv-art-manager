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
//nolint:gocyclo,gocognit,funlen // Config loading is naturally complex due to many fields
func Load() (*Config, error) {
	cfg := &Config{
		ArtworkDir:          envStr("ARTWORK_DIR", "/data/artwork"),
		MaxArtworkImages:    envInt("MAX_ARTWORK_IMAGES", 100),
		MaxDownloadSizeMB:   envInt("MAX_DOWNLOAD_SIZE_MB", 20),
		TokenDir:            envStr("TOKEN_DIR", "/data/tokens"),
		SyncIntervalMin:     envInt("SYNC_INTERVAL_MINUTES", 5),
		MatteStyle:          envStr("MATTE_STYLE", "none"),
		ClientName:          envStr("CLIENT_NAME", "Frame Art Manager"),
		DryRun:              envBool("DRY_RUN"),
		LogLevel:            strings.ToLower(envStr("LOG_LEVEL", "info")),
		SlideshowEnabled:    envBool("SLIDESHOW_ENABLED"),
		SlideshowInterval:   envInt("SLIDESHOW_INTERVAL", 15),
		SlideshowType:       strings.ToLower(envStr("SLIDESHOW_TYPE", "shuffle")),
		SolarEnabled:        envBool("SOLAR_BRIGHTNESS_ENABLED"),
		Timezone:            envStr("LOCATION_TIMEZONE", "UTC"),
		BrightnessMin:       envInt("BRIGHTNESS_MIN", 2),
		BrightnessMax:       envInt("BRIGHTNESS_MAX", 10),
		RemoveUnknownImages: envBool("REMOVE_UNKNOWN_IMAGES"),
		AutoOffTime:         envStr("AUTO_OFF_TIME", ""),
		AutoOffGraceHours:   envFloat("AUTO_OFF_GRACE_HOURS", 2),
		TVMAC:               envStr("TV_MAC", ""),
		EnableRESTGate:      envBool("ENABLE_REST_GATE"),
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
		HealthPort:          envInt("HEALTH_PORT", 0),
		ConnectionTimeout:   time.Duration(envInt("CONNECTION_TIMEOUT_SECONDS", 60)) * time.Second,
		APITimeout:          time.Duration(envInt("API_TIMEOUT_SECONDS", 60)) * time.Second,
		UploadDelay:         time.Duration(envInt("UPLOAD_DELAY_MS", 3000)) * time.Millisecond,
		UploadAttempts:      envInt("UPLOAD_ATTEMPTS", 3),
		GateTimeout:         time.Duration(envInt("GATE_TIMEOUT_MS", 10000)) * time.Millisecond,
		PUID:                envInt("PUID", 0),
		PGID:                envInt("PGID", 0),
	}

	// Parse TV IPs (required).
	raw := os.Getenv("TV_IPS")
	if raw == "" {
		return nil, fmt.Errorf("TV_IPS environment variable is required")
	}
	for _, ip := range strings.Split(raw, ",") {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			cfg.TVIPs = append(cfg.TVIPs, ip)
		}
	}
	if len(cfg.TVIPs) == 0 {
		return nil, fmt.Errorf("TV_IPS must contain at least one non-empty IP address")
	}

	// Slideshow override detection: true if any slideshow env var was set.
	cfg.SlideshowOverride = os.Getenv("SLIDESHOW_ENABLED") != "" ||
		os.Getenv("SLIDESHOW_INTERVAL") != "" ||
		os.Getenv("SLIDESHOW_TYPE") != ""

	// Manual brightness (optional).
	if v := os.Getenv("BRIGHTNESS"); v != "" {
		b, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BRIGHTNESS value %q: %w", v, err)
		}
		cfg.ManualBrightness = &b
	}

	// Solar latitude/longitude (required if solar enabled).
	if v := os.Getenv("LOCATION_LATITUDE"); v != "" {
		lat, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid LOCATION_LATITUDE %q: %w", v, err)
		}
		cfg.Latitude = &lat
	}
	if v := os.Getenv("LOCATION_LONGITUDE"); v != "" {
		lon, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid LOCATION_LONGITUDE %q: %w", v, err)
		}
		cfg.Longitude = &lon
	}

	// --- Validation ---

	if cfg.BrightnessMin >= cfg.BrightnessMax {
		return nil, fmt.Errorf(
			"BRIGHTNESS_MIN (%d) must be less than BRIGHTNESS_MAX (%d)",
			cfg.BrightnessMin, cfg.BrightnessMax,
		)
	}

	if cfg.SolarEnabled {
		if cfg.Latitude == nil || cfg.Longitude == nil {
			return nil, fmt.Errorf(
				"LOCATION_LATITUDE and LOCATION_LONGITUDE are required when SOLAR_BRIGHTNESS_ENABLED=true",
			)
		}
	}

	if cfg.SlideshowType != "shuffle" && cfg.SlideshowType != "sequential" {
		return nil, fmt.Errorf(
			"SLIDESHOW_TYPE must be 'shuffle' or 'sequential', got %q",
			cfg.SlideshowType,
		)
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[cfg.LogLevel] {
		return nil, fmt.Errorf(
			"LOG_LEVEL must be one of debug, info, warn, error; got %q",
			cfg.LogLevel,
		)
	}

	return cfg, nil
}
