// Package config loads and validates all application configuration from
// environment variables. It produces a single Config struct that is passed
// by pointer to every other package — no global state.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application settings. It is populated by Load() and then
// treated as read-only for the lifetime of the process.
type Config struct {
	// TVIPs is the list of TV IP addresses to sync artwork to.
	TVIPs []string

	// ArtworkDir is the local directory containing artwork images.
	ArtworkDir string

	// TokenDir is the directory for storing TV auth tokens and mappings.
	TokenDir string

	// SyncIntervalMin is the number of minutes between sync cycles.
	SyncIntervalMin int

	// MatteStyle is the matte/border style applied to uploaded artwork.
	// Use "none" for full-screen, or "style_color" (e.g. "shadowbox_polar").
	MatteStyle string

	// ClientName is the identity sent to the TV during WebSocket handshake.
	// A stable name prevents the TV from prompting Allow/Deny on every connect.
	ClientName string

	// DryRun logs all operations without actually executing them.
	DryRun bool

	// LogLevel controls structured logging verbosity: debug, info, warn, error.
	LogLevel string

	// --- Slideshow ---

	// SlideshowEnabled turns on slideshow override when true.
	SlideshowEnabled bool

	// SlideshowInterval is the number of minutes between slide transitions.
	SlideshowInterval int

	// SlideshowType is either "shuffle" or "sequential".
	SlideshowType string

	// SlideshowOverride is true if any slideshow env var was explicitly set.
	// When false, the TV's current slideshow settings are preserved.
	SlideshowOverride bool

	// --- Brightness ---

	// ManualBrightness is a fixed brightness value (0–50). Nil if unset.
	ManualBrightness *int

	// SolarEnabled enables automatic brightness based on sun elevation.
	SolarEnabled bool

	// Latitude is required when SolarEnabled is true.
	Latitude *float64

	// Longitude is required when SolarEnabled is true.
	Longitude *float64

	// Timezone is an IANA timezone string (e.g. "America/Denver").
	Timezone string

	// BrightnessMin is the brightness when the sun is below the horizon.
	BrightnessMin int

	// BrightnessMax is the brightness when the sun is at zenith.
	BrightnessMax int

	// --- Cleanup ---

	// RemoveUnknownImages deletes images on the TV that aren't tracked by
	// our filename→content_id mapping.
	RemoveUnknownImages bool

	// --- Auto-Off ---

	// AutoOffTime is a 24-hour time string (e.g. "22:00") at which TVs in
	// art mode should be powered off. Empty string disables the feature.
	AutoOffTime string

	// AutoOffGraceHours is how long after AutoOffTime to keep trying.
	AutoOffGraceHours float64

	// --- Wake-on-LAN ---

	// TVMAC is a MAC address (e.g. "AA:BB:CC:DD:EE:FF") for Wake-on-LAN.
	// Empty string disables WoL.
	TVMAC string

	// --- REST Gate ---

	// EnableRESTGate enables the Silent REST Gate (GET http://<ip>:8001/ms/art)
	// that checks if the TV is in Art Mode before attempting WSS connection.
	// Default false because not all firmware versions support this endpoint.
	EnableRESTGate bool

	// --- Image Sources ---

	// SourcesFile is the path to a text file containing image URLs to download.
	// One URL per line. Lines starting with # are comments. Empty disables.
	SourcesFile string

	// UnsplashAccessKey is the API key for Unsplash image downloads.
	UnsplashAccessKey string

	// NasaApiKey is the API key for NASA APOD downloads.
	// Defaults to DEMO_KEY if empty.
	NasaApiKey string

	// PexelsApiKey is the API key for Pexels image downloads.
	PexelsApiKey string

	// PixabayApiKey is the API key for Pixabay image downloads.
	PixabayApiKey string

	// --- Image Optimization ---

	// OptimizeEnabled enables automatic image resizing for oversized images.
	OptimizeEnabled bool

	// OptimizeMaxWidth is the maximum image width (default 3840 for 4K).
	OptimizeMaxWidth int

	// OptimizeMaxHeight is the maximum image height (default 2160 for 4K).
	OptimizeMaxHeight int

	// OptimizeJPEGQuality is the JPEG encoding quality (1-100, default 92).
	OptimizeJPEGQuality int

	// SmartCropEnabled enables entropy-based cropping to fit 16:9 perfectly.
	SmartCropEnabled bool

	// --- Health Server ---

	// HealthPort is the HTTP port for /health and /status endpoints.
	// 0 disables the health server.
	HealthPort int

	// --- Timeouts ---

	// ConnectionTimeout is the max time to wait for a WSS handshake.
	ConnectionTimeout time.Duration

	// APITimeout is the max time to wait for art API responses.
	APITimeout time.Duration

	// UploadDelay is the pause between consecutive image uploads.
	UploadDelay time.Duration

	// UploadAttempts is how many times to retry a failed upload.
	UploadAttempts int

	// GateTimeout is the HTTP timeout for the REST gate probe.
	GateTimeout time.Duration

	// --- Ownership ---

	// PUID and PGID for directory ownership (optional).
	PUID int
	PGID int
}

// Load reads configuration from environment variables, applies defaults,
// and validates the result. Returns an error if required values are missing
// or constraints are violated.
//nolint:gocyclo // Config loading is naturally complex due to many fields
func Load() (*Config, error) {
	cfg := &Config{
		ArtworkDir:          envStr("ARTWORK_DIR", "/data/artwork"),
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
		UnsplashAccessKey:   envStr("UNSPLASH_ACCESS_KEY", ""),
		NasaApiKey:         envStr("NASA_API_KEY", "DEMO_KEY"),
		PexelsApiKey:       envStr("PEXELS_API_KEY", ""),
		PixabayApiKey:      envStr("PIXABAY_API_KEY", ""),
		OptimizeEnabled:     envBoolWithDefault("IMAGE_OPTIMIZE_ENABLED", true),
		OptimizeMaxWidth:    envInt("IMAGE_MAX_WIDTH", 3840),
		OptimizeMaxHeight:   envInt("IMAGE_MAX_HEIGHT", 2160),
		SmartCropEnabled:    envBoolWithDefault("SMART_CROP_ENABLED", true),
		OptimizeJPEGQuality: envInt("IMAGE_JPEG_QUALITY", 92),
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

// --- helpers ---

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string) bool {
	return envBoolWithDefault(key, false)
}

func envBoolWithDefault(key string, def bool) bool {
	v := strings.ToLower(os.Getenv(key))
	if v == "" {
		return def
	}
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
