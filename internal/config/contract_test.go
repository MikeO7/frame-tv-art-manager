package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	contractFalse      = "false"
	contractPad        = "pad"
	contractSequential = "sequential"
	contractTrue       = "true"
	contractWarn       = "warn"
)

func TestLoadDefaultContract(t *testing.T) {
	clearEnvironmentForContract(t)
	t.Setenv("TV_IPS", "192.0.2.10")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := &Config{
		TVIPs:               []string{"192.0.2.10"},
		ArtworkDir:          "/data/artwork",
		MaxArtworkImages:    0,
		MaxDownloadSizeMB:   20,
		TokenDir:            "/data/tokens",
		SyncIntervalMin:     5,
		ShutdownTimeout:     30 * time.Second,
		MatteStyle:          "none",
		ClientName:          "Frame Art Manager",
		DryRun:              false,
		LogLevel:            "info",
		SlideshowEnabled:    false,
		SlideshowInterval:   15,
		SlideshowType:       "shuffle",
		SlideshowOverride:   false,
		ManualBrightness:    nil,
		SolarEnabled:        false,
		Latitude:            nil,
		Longitude:           nil,
		Timezone:            "UTC",
		BrightnessMin:       2,
		BrightnessMax:       10,
		RemoveUnknownImages: false,
		AutoOffTime:         "",
		AutoOffGraceHours:   2,
		TVMAC:               "",
		EnableRESTGate:      false,
		VerifyTLS:           false,
		SourcesFile:         "",
		UnsplashAppID:       "",
		UnsplashAccessKey:   "",
		UnsplashSecretKey:   "",
		NasaAPIKey:          "DEMO_KEY",
		PexelsAPIKey:        "",
		PixabayAPIKey:       "",
		OptimizeEnabled:     true,
		SmartCropEnabled:    false,
		OptimizeMaxWidth:    3840,
		OptimizeMaxHeight:   2160,
		OptimizeJPEGQuality: 95,
		MuseumModeEnabled:   false,
		MuseumModeIntensity: 5,
		HealthPort:          8080,
		HealthBindAddress:   "0.0.0.0",
		UploadEnabled:       false,
		ConnectionTimeout:   60 * time.Second,
		APITimeout:          60 * time.Second,
		UploadDelay:         3 * time.Second,
		UploadAttempts:      3,
		GateTimeout:         10 * time.Second,
		PUID:                0,
		PGID:                0,
		PortraitMode:        "crop",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() defaults =\n%+v\nwant\n%+v", got, want)
	}
}

func TestLoadLegacyEnvironmentPrecedenceContract(t *testing.T) {
	clearEnvironmentForContract(t)
	t.Setenv("TV_IPS", "192.0.2.10")
	t.Setenv("VERIFY_TLS", contractTrue)
	t.Setenv("SKIP_TLS_VERIFY", contractTrue)
	t.Setenv("SLIDESHOW_ENABLED", "")
	t.Setenv("SLIDESHOW_INTERVAL", "15")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.VerifyTLS {
		t.Fatal("SKIP_TLS_VERIFY=true must override VERIFY_TLS=true")
	}
	if !got.SlideshowOverride {
		t.Fatal("presence of a SLIDESHOW_* variable must enable override")
	}
}

func TestLoadRejectsCanonicalDuplicateTVAddresses(t *testing.T) {
	clearEnvironmentForContract(t)
	t.Setenv("TV_IPS", "2001:db8::1,2001:0db8:0:0:0:0:0:1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Load() error = %v, want duplicate-address rejection", err)
	}
}

func TestLoadEveryEnvironmentValueContract(t *testing.T) {
	clearEnvironmentForContract(t)
	values := map[string]string{
		"TV_IPS": "192.0.2.10,192.0.2.11", "ARTWORK_DIR": "/srv/art",
		"MAX_ARTWORK_IMAGES": "123", "MAX_DOWNLOAD_SIZE_MB": "24",
		"TOKEN_DIR": "/srv/state", "SYNC_INTERVAL_MINUTES": "9", "SHUTDOWN_TIMEOUT_SECONDS": "13",
		"MATTE_STYLE": "shadowbox_polar", "CLIENT_NAME": "Contract Client",
		"DRY_RUN": contractTrue, "LOG_LEVEL": contractWarn,
		"SLIDESHOW_ENABLED": contractTrue, "SLIDESHOW_INTERVAL": "60",
		"SLIDESHOW_TYPE": contractSequential, "BRIGHTNESS": "7",
		"SOLAR_BRIGHTNESS_ENABLED": contractTrue, "LOCATION_LATITUDE": "40.25",
		"LOCATION_LONGITUDE": "-105.5", "LOCATION_TIMEZONE": "America/Denver",
		"BRIGHTNESS_MIN": "1", "BRIGHTNESS_MAX": "20",
		"REMOVE_UNKNOWN_IMAGES": contractTrue, "AUTO_OFF_TIME": "23:15",
		"AUTO_OFF_GRACE_HOURS": "1.5", "TV_MAC": "AA:BB:CC:DD:EE:FF",
		"ENABLE_REST_GATE": contractTrue, "VERIFY_TLS": contractTrue,
		"SKIP_TLS_VERIFY": contractFalse, "ARTWORK_SOURCES_FILE": "/srv/sources.yaml",
		"UNSPLASH_APP_ID": "app", "UNSPLASH_ACCESS_KEY": "access",
		"UNSPLASH_SECRET_KEY": "secret", "NASA_API_KEY": "nasa",
		"PEXELS_API_KEY": "pexels", "PIXABAY_API_KEY": "pixabay",
		"IMAGE_OPTIMIZE_ENABLED": contractFalse, "SMART_CROP_ENABLED": contractTrue,
		"IMAGE_MAX_WIDTH": "1920", "IMAGE_MAX_HEIGHT": "1080",
		"IMAGE_JPEG_QUALITY": "88", "IMAGE_MUSEUM_MODE": contractTrue,
		"IMAGE_MUSEUM_INTENSITY": "8", "HEALTH_PORT": "9090", "HEALTH_BIND_ADDRESS": "127.0.0.1",
		"UPLOAD_ENABLED": contractTrue, "UPLOAD_TOKEN": "contract-upload-token", "CONNECTION_TIMEOUT_SECONDS": "11",
		"API_TIMEOUT_SECONDS": "12", "UPLOAD_DELAY_MS": "250",
		"UPLOAD_ATTEMPTS": "2", "GATE_TIMEOUT_MS": "750",
		"PUID": "1000", "PGID": "1001", "PORTRAIT_MODE": contractPad,
	}
	for key, value := range values {
		t.Setenv(key, value)
	}

	brightness := 7
	latitude := 40.25
	longitude := -105.5
	want := &Config{
		TVIPs:               []string{"192.0.2.10", "192.0.2.11"},
		ArtworkDir:          "/srv/art",
		MaxArtworkImages:    123,
		MaxDownloadSizeMB:   24,
		TokenDir:            "/srv/state",
		SyncIntervalMin:     9,
		ShutdownTimeout:     13 * time.Second,
		MatteStyle:          "shadowbox_polar",
		ClientName:          "Contract Client",
		DryRun:              true,
		LogLevel:            contractWarn,
		SlideshowEnabled:    true,
		SlideshowInterval:   60,
		SlideshowType:       contractSequential,
		SlideshowOverride:   true,
		ManualBrightness:    &brightness,
		SolarEnabled:        true,
		Latitude:            &latitude,
		Longitude:           &longitude,
		Timezone:            "America/Denver",
		BrightnessMin:       1,
		BrightnessMax:       20,
		RemoveUnknownImages: true,
		AutoOffTime:         "23:15",
		AutoOffGraceHours:   1.5,
		TVMAC:               "",
		EnableRESTGate:      true,
		VerifyTLS:           true,
		SourcesFile:         "/srv/sources.yaml",
		UnsplashAppID:       "app",
		UnsplashAccessKey:   "access",
		UnsplashSecretKey:   "secret",
		NasaAPIKey:          "nasa",
		PexelsAPIKey:        "pexels",
		PixabayAPIKey:       "pixabay",
		OptimizeEnabled:     false,
		SmartCropEnabled:    true,
		OptimizeMaxWidth:    1920,
		OptimizeMaxHeight:   1080,
		OptimizeJPEGQuality: 88,
		MuseumModeEnabled:   true,
		MuseumModeIntensity: 8,
		HealthPort:          9090,
		HealthBindAddress:   "127.0.0.1",
		UploadEnabled:       true,
		UploadToken:         "contract-upload-token",
		ConnectionTimeout:   11 * time.Second,
		APITimeout:          12 * time.Second,
		UploadDelay:         250 * time.Millisecond,
		UploadAttempts:      2,
		GateTimeout:         750 * time.Millisecond,
		PUID:                1000,
		PGID:                1001,
		PortraitMode:        contractPad,
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() explicit values =\n%+v\nwant\n%+v", got, want)
	}
}

func clearEnvironmentForContract(t *testing.T) {
	t.Helper()
	original := os.Environ()
	os.Clearenv()
	t.Cleanup(func() {
		os.Clearenv()
		for _, entry := range original {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				if err := os.Setenv(key, value); err != nil {
					t.Errorf("restore environment variable %s: %v", key, err)
				}
			}
		}
	})
}
