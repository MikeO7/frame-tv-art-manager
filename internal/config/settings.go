package config

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type environment struct {
	values   map[string]string
	warnings []Warning
}

// Warning reports a compatibility fallback without echoing the supplied value.
type Warning struct {
	Variable string
	Fallback string
	Message  string
}

// LoadEnviron parses a supplied process environment without reading or
// mutating global process state.
func LoadEnviron(environ []string) (*Config, []Warning, error) {
	env := environment{values: make(map[string]string, len(environ))}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			env.values[key] = value
		}
	}

	cfg, err := env.loadConfig()
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(env.warnings, func(i, j int) bool {
		return env.warnings[i].Variable < env.warnings[j].Variable
	})
	return cfg, append([]Warning(nil), env.warnings...), nil
}

//nolint:funlen // keeping the complete environment schema together makes defaults auditable
func (env *environment) loadConfig() (*Config, error) {
	cfg := &Config{
		ArtworkDir:          env.str("ARTWORK_DIR", "/data/artwork"),
		MaxArtworkImages:    env.integer("MAX_ARTWORK_IMAGES", 0),
		MaxDownloadSizeMB:   env.integer("MAX_DOWNLOAD_SIZE_MB", 20),
		TokenDir:            env.str("TOKEN_DIR", "/data/tokens"),
		SyncIntervalMin:     env.integer("SYNC_INTERVAL_MINUTES", 5),
		ShutdownTimeout:     safeDuration(env.integer("SHUTDOWN_TIMEOUT_SECONDS", 30), time.Second),
		MatteStyle:          env.str("MATTE_STYLE", "none"),
		ClientName:          env.str("CLIENT_NAME", "Frame Art Manager"),
		DryRun:              env.boolean("DRY_RUN", false),
		LogLevel:            strings.ToLower(env.str("LOG_LEVEL", "info")),
		SlideshowEnabled:    env.boolean("SLIDESHOW_ENABLED", false),
		SlideshowInterval:   env.integer("SLIDESHOW_INTERVAL", 15),
		SlideshowType:       strings.ToLower(env.str("SLIDESHOW_TYPE", "shuffle")),
		SlideshowOverride:   env.present("SLIDESHOW_ENABLED") || env.present("SLIDESHOW_INTERVAL") || env.present("SLIDESHOW_TYPE"),
		SolarEnabled:        env.boolean("SOLAR_BRIGHTNESS_ENABLED", false),
		Timezone:            env.str("LOCATION_TIMEZONE", "UTC"),
		BrightnessMin:       env.integer("BRIGHTNESS_MIN", 2),
		BrightnessMax:       env.integer("BRIGHTNESS_MAX", 10),
		RemoveUnknownImages: env.boolean("REMOVE_UNKNOWN_IMAGES", false),
		AutoOffTime:         env.str("AUTO_OFF_TIME", ""),
		AutoOffGraceHours:   env.decimal("AUTO_OFF_GRACE_HOURS", 2),
		TVMAC:               env.str("TV_MAC", ""),
		EnableRESTGate:      env.boolean("ENABLE_REST_GATE", false),
		VerifyTLS:           env.boolean("VERIFY_TLS", false) && !env.boolean("SKIP_TLS_VERIFY", false),
		SourcesFile:         env.str("ARTWORK_SOURCES_FILE", ""),
		UnsplashAppID:       env.str("UNSPLASH_APP_ID", ""),
		UnsplashAccessKey:   env.str("UNSPLASH_ACCESS_KEY", ""),
		UnsplashSecretKey:   env.str("UNSPLASH_SECRET_KEY", ""),
		NasaAPIKey:          env.str("NASA_API_KEY", "DEMO_KEY"),
		PexelsAPIKey:        env.str("PEXELS_API_KEY", ""),
		PixabayAPIKey:       env.str("PIXABAY_API_KEY", ""),
		OptimizeEnabled:     env.boolean("IMAGE_OPTIMIZE_ENABLED", true),
		SmartCropEnabled:    env.boolean("SMART_CROP_ENABLED", false),
		SmartCropMinGain:    env.decimal("SMART_CROP_MIN_GAIN", 0.03),
		OptimizeMaxWidth:    env.integer("IMAGE_MAX_WIDTH", 3840),
		OptimizeMaxHeight:   env.integer("IMAGE_MAX_HEIGHT", 2160),
		OptimizeMaxPixels:   env.integer("IMAGE_MAX_OUTPUT_PIXELS", 12_000_000),
		OptimizeMemoryMB:    env.integer("IMAGE_MAX_WORKING_MEMORY_MB", 512),
		OptimizeJPEGQuality: env.integer("IMAGE_JPEG_QUALITY", 95),
		OptimizePNG:         env.boolean("IMAGE_OPTIMIZE_PNG", true),
		LinearLightResize:   env.boolean("IMAGE_LINEAR_LIGHT_RESIZE", true),
		SharpenAmount:       env.decimal("IMAGE_SHARPEN_AMOUNT", 0.25),
		SharpenThreshold:    env.integer("IMAGE_SHARPEN_THRESHOLD", 4),
		DitherEnabled:       env.boolean("IMAGE_DITHER_ENABLED", false),
		ColorProfilePolicy:  strings.ToLower(env.str("IMAGE_COLOR_PROFILE_POLICY", colorProfileAssumeSRGB)),
		MuseumModeEnabled:   env.boolean("IMAGE_MUSEUM_MODE", false),
		MuseumModeIntensity: env.integer("IMAGE_MUSEUM_INTENSITY", 5),
		HealthPort:          env.integer("HEALTH_PORT", 8080),
		HealthBindAddress:   env.str("HEALTH_BIND_ADDRESS", "0.0.0.0"),
		UploadEnabled:       env.boolean("UPLOAD_ENABLED", false),
		UploadToken:         env.str("UPLOAD_TOKEN", ""),
		ConnectionTimeout:   safeDuration(env.integer("CONNECTION_TIMEOUT_SECONDS", 60), time.Second),
		APITimeout:          safeDuration(env.integer("API_TIMEOUT_SECONDS", 60), time.Second),
		UploadDelay:         safeDuration(env.integer("UPLOAD_DELAY_MS", 3000), time.Millisecond),
		UploadAttempts:      env.integer("UPLOAD_ATTEMPTS", 3),
		GateTimeout:         safeDuration(env.integer("GATE_TIMEOUT_MS", 10000), time.Millisecond),
		PUID:                env.integer("PUID", 0),
		PGID:                env.integer("PGID", 0),
		PortraitMode:        strings.ToLower(env.str("PORTRAIT_MODE", "crop")),
	}

	if err := env.parseRequiredValues(cfg); err != nil {
		return nil, err
	}
	if len(cfg.TVIPs) > 1 && cfg.TVMAC != "" {
		env.warn("TV_MAC", "disabled", "TV_MAC is ambiguous for multiple TVs; automatic Wake is disabled")
		cfg.TVMAC = ""
	}
	if err := normalizePaths(cfg); err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func safeDuration(value int, unit time.Duration) time.Duration {
	if value > 0 && int64(value) > math.MaxInt64/int64(unit) {
		return time.Duration(math.MaxInt64)
	}
	if value < 0 && int64(value) < math.MinInt64/int64(unit) {
		return time.Duration(math.MinInt64)
	}
	return time.Duration(value) * unit
}

func (env *environment) parseRequiredValues(cfg *Config) error {
	rawIPs := env.values["TV_IPS"]
	if rawIPs == "" {
		return fmt.Errorf("TV_IPS environment variable is required")
	}
	for _, address := range strings.Split(rawIPs, ",") {
		if address = strings.TrimSpace(address); address != "" {
			cfg.TVIPs = append(cfg.TVIPs, address)
		}
	}
	if len(cfg.TVIPs) == 0 {
		return fmt.Errorf("TV_IPS must contain at least one non-empty IP address")
	}

	if value := env.values["BRIGHTNESS"]; value != "" {
		brightness, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid BRIGHTNESS value %q: %w", value, err)
		}
		cfg.ManualBrightness = &brightness
	}
	latitude, hasLatitude, err := env.optionalDecimal("LOCATION_LATITUDE")
	if err != nil {
		return err
	}
	longitude, hasLongitude, err := env.optionalDecimal("LOCATION_LONGITUDE")
	if err != nil {
		return err
	}
	if hasLatitude {
		cfg.Latitude = &latitude
	}
	if hasLongitude {
		cfg.Longitude = &longitude
	}
	return nil
}

func (env *environment) str(key, fallback string) string {
	if value := env.values[key]; value != "" {
		return value
	}
	return fallback
}

func (env *environment) integer(key string, fallback int) int {
	value := env.values[key]
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		env.warn(key, strconv.Itoa(fallback), fmt.Sprintf("%s is malformed; using the compatibility fallback", key))
		return fallback
	}
	return parsed
}

func (env *environment) boolean(key string, fallback bool) bool {
	value := strings.ToLower(env.values[key])
	if value == "" {
		return fallback
	}
	switch value {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		env.warn(key, strconv.FormatBool(fallback), fmt.Sprintf("%s is malformed; using the compatibility fallback", key))
		return fallback
	}
}

func (env *environment) decimal(key string, fallback float64) float64 {
	value := env.values[key]
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		env.warn(key, strconv.FormatFloat(fallback, 'f', -1, 64), fmt.Sprintf("%s is malformed; using the compatibility fallback", key))
		return fallback
	}
	return parsed
}

func (env *environment) optionalDecimal(key string) (float64, bool, error) {
	value := env.values[key]
	if value == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s %q: %w", key, value, err)
	}
	return parsed, true, nil
}

func (env *environment) present(key string) bool {
	_, ok := env.values[key]
	return ok
}

func (env *environment) warn(variable, fallback, message string) {
	env.warnings = append(env.warnings, Warning{Variable: variable, Fallback: fallback, Message: message})
}
