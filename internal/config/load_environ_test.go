package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadEnvironIsPresenceAwareAndPure(t *testing.T) {
	t.Setenv("TV_IPS", "203.0.113.99")

	cfg, warnings, err := LoadEnviron([]string{
		"TV_IPS=192.0.2.10, 192.0.2.11",
		"SLIDESHOW_ENABLED=",
		"MAX_DOWNLOAD_SIZE_MB=invalid",
		"DRY_RUN=perhaps",
		"AUTO_OFF_GRACE_HOURS=unknown",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if len(cfg.TVIPs) != 2 || cfg.TVIPs[0] != "192.0.2.10" {
		t.Fatalf("TV addresses = %v", cfg.TVIPs)
	}
	if !cfg.SlideshowOverride {
		t.Fatal("an explicitly empty slideshow variable must opt into override")
	}
	if cfg.MaxDownloadSizeMB != 20 {
		t.Fatalf("MaxDownloadSizeMB = %d, want 20", cfg.MaxDownloadSizeMB)
	}
	if cfg.DryRun {
		t.Fatal("malformed DRY_RUN must retain the compatibility fallback")
	}
	if cfg.AutoOffGraceHours != 2 {
		t.Fatalf("AutoOffGraceHours = %v, want 2", cfg.AutoOffGraceHours)
	}
	assertWarningVariables(t, warnings, "AUTO_OFF_GRACE_HOURS", "DRY_RUN", "MAX_DOWNLOAD_SIZE_MB")
}

func TestLoadEnvironBuildsCanonicalConfig(t *testing.T) {
	cfg, warnings, err := LoadEnviron([]string{
		"TV_IPS=192.0.2.10",
		"ARTWORK_DIR=/srv/art",
		"TOKEN_DIR=/srv/state",
		"SYNC_INTERVAL_MINUTES=9",
		"SHUTDOWN_TIMEOUT_SECONDS=12",
		"HEALTH_PORT=9090",
		"IMAGE_MAX_WIDTH=1920",
		"IMAGE_MAX_HEIGHT=1080",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", warnings)
	}
	if cfg.SyncIntervalMin != 9 || cfg.ShutdownTimeout != 12*time.Second {
		t.Fatalf("runtime configuration = %+v", cfg)
	}
	if cfg.ArtworkDir != "/srv/art" || cfg.TokenDir != "/srv/state" {
		t.Fatalf("storage configuration = %+v", cfg)
	}
	if cfg.OptimizeMaxWidth != 1920 || cfg.OptimizeMaxHeight != 1080 {
		t.Fatalf("transform configuration = %+v", cfg)
	}
	if cfg.HealthPort != 9090 {
		t.Fatalf("HTTP configuration = %+v", cfg)
	}
}

func TestLoadEnvironDisablesAmbiguousMultiTVWake(t *testing.T) {
	cfg, warnings, err := LoadEnviron([]string{
		"TV_IPS=192.0.2.10,192.0.2.11",
		"TV_MAC=AA:BB:CC:DD:EE:FF",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if cfg.TVMAC != "" {
		t.Fatalf("TVMAC = %q, want disabled", cfg.TVMAC)
	}
	assertWarningVariables(t, warnings, "TV_MAC")
}

func TestLoadEnvironRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "invalid address", value: "TV_IPS=not-an-ip", want: "TV_IPS"},
		{name: "invalid timezone", value: "LOCATION_TIMEZONE=Not/A_Zone", want: "LOCATION_TIMEZONE"},
		{name: "invalid quality", value: "IMAGE_JPEG_QUALITY=101", want: "IMAGE_JPEG_QUALITY"},
		{name: "unsafe output allocation", value: "IMAGE_MAX_OUTPUT_PIXELS=8000000", want: "IMAGE_MAX_OUTPUT_PIXELS"},
		{name: "unsafe working memory", value: "IMAGE_MAX_WORKING_MEMORY_MB=64", want: "IMAGE_MAX_WORKING_MEMORY_MB"},
		{name: "invalid crop confidence", value: "SMART_CROP_MIN_GAIN=2", want: "SMART_CROP_MIN_GAIN"},
		{name: "invalid sharpen amount", value: "IMAGE_SHARPEN_AMOUNT=3", want: "IMAGE_SHARPEN_AMOUNT"},
		{name: "invalid sharpen threshold", value: "IMAGE_SHARPEN_THRESHOLD=256", want: "IMAGE_SHARPEN_THRESHOLD"},
		{name: "invalid profile policy", value: "IMAGE_COLOR_PROFILE_POLICY=convert", want: "IMAGE_COLOR_PROFILE_POLICY"},
		{name: "invalid port", value: "HEALTH_PORT=70000", want: "HEALTH_PORT"},
		{name: "invalid bind address", value: "HEALTH_BIND_ADDRESS=all", want: "HEALTH_BIND_ADDRESS"},
		{name: "invalid timeout", value: "API_TIMEOUT_SECONDS=0", want: "API_TIMEOUT_SECONDS"},
		{name: "invalid latitude", value: "LOCATION_LATITUDE=91", want: "LOCATION_LATITUDE"},
		{name: "NaN latitude", value: "LOCATION_LATITUDE=NaN", want: "LOCATION_LATITUDE"},
		{name: "infinite longitude", value: "LOCATION_LONGITUDE=Inf", want: "LOCATION_LONGITUDE"},
		{name: "invalid auto off", value: "AUTO_OFF_TIME=25:00", want: "AUTO_OFF_TIME"},
		{name: "unsupported slideshow interval", value: "SLIDESHOW_INTERVAL=30", want: "SLIDESHOW_INTERVAL"},
		{name: "oversized import", value: "MAX_DOWNLOAD_SIZE_MB=1025", want: "MAX_DOWNLOAD_SIZE_MB"},
		{name: "unsafe import allocation", value: "MAX_DOWNLOAD_SIZE_MB=101", want: "MAX_DOWNLOAD_SIZE_MB"},
		{name: "artwork-shaped control file", value: "ARTWORK_SOURCES_FILE=/srv/sources.jpg", want: "ARTWORK_SOURCES_FILE"},
		{name: "oversized timeout", value: "API_TIMEOUT_SECONDS=9223372036854775807", want: "API_TIMEOUT_SECONDS"},
		{name: "oversized shutdown", value: "SHUTDOWN_TIMEOUT_SECONDS=601", want: "SHUTDOWN_TIMEOUT_SECONDS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environ := []string{"TV_IPS=192.0.2.10", test.value}
			if strings.HasPrefix(test.value, "TV_IPS=") {
				environ = []string{test.value}
			}
			_, _, err := LoadEnviron(environ)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadEnviron() error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestLoadEnvironNormalizesRelativePaths(t *testing.T) {
	cfg, _, err := LoadEnviron([]string{
		"TV_IPS=192.0.2.10",
		"ARTWORK_DIR=relative/art",
		"TOKEN_DIR=relative/state",
		"ARTWORK_SOURCES_FILE=relative/sources.txt",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	for name, path := range map[string]string{
		"artwork": cfg.ArtworkDir,
		"state":   cfg.TokenDir,
		"sources": cfg.SourcesFile,
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path = %q, want absolute", name, path)
		}
	}
}

func assertWarningVariables(t *testing.T, warnings []Warning, variables ...string) {
	t.Helper()
	if len(warnings) != len(variables) {
		t.Fatalf("warnings = %+v, want variables %v", warnings, variables)
	}
	for index, variable := range variables {
		if warnings[index].Variable != variable {
			t.Fatalf("warning[%d] = %+v, want %s", index, warnings[index], variable)
		}
		if strings.Contains(warnings[index].Message, "=") {
			t.Fatalf("warning leaks a supplied value: %+v", warnings[index])
		}
	}
}
