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
		"AUTO_OFF_GRACE_HOURS=unknown",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if len(cfg.TVIPs) != 2 || cfg.TVIPs[0] != "192.0.2.10" {
		t.Fatalf("TV addresses = %v", cfg.TVIPs)
	}
	if cfg.SlideshowOverride || cfg.SlideshowEnabledSet {
		t.Fatal("an empty slideshow variable must count as unset, like every other variable")
	}
	if cfg.MaxDownloadSizeMB != 20 {
		t.Fatalf("MaxDownloadSizeMB = %d, want 20", cfg.MaxDownloadSizeMB)
	}
	if cfg.AutoOffGraceHours != 2 {
		t.Fatalf("AutoOffGraceHours = %v, want 2", cfg.AutoOffGraceHours)
	}
	assertWarningVariables(t, warnings, "AUTO_OFF_GRACE_HOURS", "MAX_DOWNLOAD_SIZE_MB")
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
	if cfg.TVMAC != nil {
		t.Fatalf("TVMAC = %v, want disabled", cfg.TVMAC)
	}
	assertWarningVariables(t, warnings, "TV_MAC")
}

func TestLoadEnvironWarnsAboutDeprecatedUnsplashKeys(t *testing.T) {
	cfg, warnings, err := LoadEnviron([]string{
		"TV_IPS=192.0.2.10",
		"UNSPLASH_APP_ID=app",
		"UNSPLASH_ACCESS_KEY=access",
		"UNSPLASH_SECRET_KEY=secret",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v, want deprecated keys accepted", err)
	}
	if cfg.UnsplashAccessKey != "access" {
		t.Fatalf("UnsplashAccessKey = %q", cfg.UnsplashAccessKey)
	}
	assertWarningVariables(t, warnings, "UNSPLASH_APP_ID", "UNSPLASH_SECRET_KEY")
	for _, warning := range warnings {
		if strings.Contains(warning.Message, "secret") || strings.Contains(warning.Fallback, "app") {
			t.Fatalf("warning %+v echoes a configured value", warning)
		}
	}
}

func TestLoadEnvironDisablesScheduledWakeWithoutMAC(t *testing.T) {
	cfg, warnings, err := LoadEnviron([]string{"TV_IPS=192.0.2.10", "AUTO_ON_TIME=07:00"})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if cfg.AutoOnTime != "" {
		t.Fatalf("AutoOnTime = %q, want disabled without TV_MAC", cfg.AutoOnTime)
	}
	assertWarningVariables(t, warnings, "AUTO_ON_TIME")
}

func TestLoadEnvironCanonicalizesTVAddressAndMAC(t *testing.T) {
	cfg, _, err := LoadEnviron([]string{
		"TV_IPS=2001:0db8:0:0:0:0:0:1",
		"TV_MAC=AA:BB:CC:DD:EE:FF",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if len(cfg.TVIPs) != 1 || cfg.TVIPs[0] != "2001:db8::1" {
		t.Fatalf("TV addresses = %v, want canonical IPv6", cfg.TVIPs)
	}
	if cfg.TVMAC.String() != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("TVMAC = %v, want parsed six-byte address", cfg.TVMAC)
	}
}

func TestLoadEnvironBooleanSpellings(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"yes", true},
		{"on", true},
		{"Y", true},
		{" true ", true},
		{"false", false},
		{"0", false},
		{"No", false},
		{"off", false},
		{"n", false},
		{"", false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			cfg, _, err := LoadEnviron([]string{"TV_IPS=192.0.2.10", "DRY_RUN=" + test.value})
			if err != nil {
				t.Fatalf("LoadEnviron() error = %v", err)
			}
			if cfg.DryRun != test.want {
				t.Fatalf("DryRun = %v, want %v", cfg.DryRun, test.want)
			}
		})
	}
}

func TestLoadEnvironRejectsMalformedBooleans(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    []string
	}{
		{name: "dry run", environ: []string{"DRY_RUN=perhaps"}, want: []string{"DRY_RUN"}},
		{name: "every malformed switch is reported", environ: []string{"UPLOAD_ENABLED=enable", "SLIDESHOW_ENABLED=2"}, want: []string{"UPLOAD_ENABLED", "SLIDESHOW_ENABLED"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := LoadEnviron(append([]string{"TV_IPS=192.0.2.10"}, test.environ...))
			if err == nil {
				t.Fatal("LoadEnviron() error = nil, want malformed boolean error")
			}
			for _, variable := range test.want {
				if !strings.Contains(err.Error(), variable) {
					t.Fatalf("LoadEnviron() error = %v, want it to name %s", err, variable)
				}
			}
			if strings.Contains(err.Error(), "perhaps") || strings.Contains(err.Error(), "sure") {
				t.Fatalf("LoadEnviron() error = %v must not echo the supplied value", err)
			}
		})
	}
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
		{name: "unsupported chroma subsampling", value: "IMAGE_JPEG_SUBSAMPLING=422", want: "IMAGE_JPEG_SUBSAMPLING"},
		{name: "unsafe output allocation", value: "IMAGE_MAX_OUTPUT_PIXELS=8000000", want: "IMAGE_MAX_OUTPUT_PIXELS"},
		{name: "unsafe working memory", value: "IMAGE_MAX_WORKING_MEMORY_MB=64", want: "IMAGE_MAX_WORKING_MEMORY_MB"},
		{name: "invalid crop confidence", value: "SMART_CROP_MIN_GAIN=2", want: "SMART_CROP_MIN_GAIN"},
		{name: "invalid sharpen amount", value: "IMAGE_SHARPEN_AMOUNT=3", want: "IMAGE_SHARPEN_AMOUNT"},
		{name: "invalid sharpen threshold", value: "IMAGE_SHARPEN_THRESHOLD=256", want: "IMAGE_SHARPEN_THRESHOLD"},
		{name: "invalid profile policy", value: "IMAGE_COLOR_PROFILE_POLICY=convert", want: "IMAGE_COLOR_PROFILE_POLICY"},
		{name: "invalid port", value: "HEALTH_PORT=70000", want: "HEALTH_PORT"},
		{name: "invalid bind address", value: "HEALTH_BIND_ADDRESS=all", want: "HEALTH_BIND_ADDRESS"},
		{name: "allowed host with scheme", value: "HEALTH_ALLOWED_HOSTS=https://frame.example.com", want: "HEALTH_ALLOWED_HOSTS"},
		{name: "allowed host with port", value: "HEALTH_ALLOWED_HOSTS=frame.lan,frame.example.com:8443", want: "HEALTH_ALLOWED_HOSTS"},
		{name: "allowed host with bad label", value: "HEALTH_ALLOWED_HOSTS=-frame.example.com", want: "HEALTH_ALLOWED_HOSTS"},
		{name: "allowed host with empty label", value: "HEALTH_ALLOWED_HOSTS=frame..example.com", want: "HEALTH_ALLOWED_HOSTS"},
		{name: "invalid timeout", value: "API_TIMEOUT_SECONDS=0", want: "API_TIMEOUT_SECONDS"},
		{name: "invalid latitude", value: "LOCATION_LATITUDE=91", want: "LOCATION_LATITUDE"},
		{name: "NaN latitude", value: "LOCATION_LATITUDE=NaN", want: "LOCATION_LATITUDE"},
		{name: "infinite longitude", value: "LOCATION_LONGITUDE=Inf", want: "LOCATION_LONGITUDE"},
		{name: "invalid auto off", value: "AUTO_OFF_TIME=25:00", want: "AUTO_OFF_TIME"},
		{name: "invalid auto on", value: "AUTO_ON_TIME=7am", want: "AUTO_ON_TIME"},
		{name: "oversized auto on grace", value: "AUTO_ON_GRACE_HOURS=25", want: "AUTO_ON_GRACE_HOURS"},
		{name: "whole-day auto on grace", value: "AUTO_ON_GRACE_HOURS=24", want: "AUTO_ON_GRACE_HOURS must be greater than 0 and less than 24"},
		{name: "zero auto on grace", value: "AUTO_ON_GRACE_HOURS=0", want: "AUTO_ON_GRACE_HOURS"},
		{name: "whole-day auto off grace", value: "AUTO_OFF_GRACE_HOURS=24", want: "AUTO_OFF_GRACE_HOURS must be greater than 0 and less than 24"},
		{name: "multi-day auto off grace", value: "AUTO_OFF_GRACE_HOURS=168", want: "AUTO_OFF_GRACE_HOURS"},
		{name: "zero auto off grace", value: "AUTO_OFF_GRACE_HOURS=0", want: "AUTO_OFF_GRACE_HOURS"},
		{name: "invalid MAC", value: "TV_MAC=not-a-mac", want: "TV_MAC"},
		{name: "non-six-byte MAC", value: "TV_MAC=00:00:00:00:fe:80:00:00", want: "TV_MAC"},
		{name: "IPv6 wake broadcast", value: "TV_WOL_BROADCAST=ff02::1", want: "TV_WOL_BROADCAST"},
		{name: "named wake broadcast", value: "TV_WOL_BROADCAST=broadcast.lan", want: "TV_WOL_BROADCAST"},
		{name: "unknown slideshow mode", value: "SLIDESHOW_MODE=both", want: "SLIDESHOW_MODE"},
		{name: "oversized slideshow interval", value: "SLIDESHOW_INTERVAL=1441", want: "SLIDESHOW_INTERVAL"},
		{name: "oversized import", value: "MAX_DOWNLOAD_SIZE_MB=1025", want: "MAX_DOWNLOAD_SIZE_MB"},
		{name: "zero local file size", value: "MAX_LOCAL_FILE_SIZE_MB=0", want: "MAX_LOCAL_FILE_SIZE_MB"},
		{name: "oversized local file size", value: "MAX_LOCAL_FILE_SIZE_MB=513", want: "MAX_LOCAL_FILE_SIZE_MB"},
		{name: "unsafe import allocation", value: "MAX_DOWNLOAD_SIZE_MB=101", want: "MAX_DOWNLOAD_SIZE_MB"},
		{name: "artwork-shaped control file", value: "ARTWORK_SOURCES_FILE=/srv/sources.jpg", want: "ARTWORK_SOURCES_FILE"},
		{name: "oversized timeout", value: "API_TIMEOUT_SECONDS=9223372036854775807", want: "API_TIMEOUT_SECONDS"},
		{name: "oversized shutdown", value: "SHUTDOWN_TIMEOUT_SECONDS=601", want: "SHUTDOWN_TIMEOUT_SECONDS"},
		{name: "negative rotation", value: "ROTATION_INTERVAL_DAYS=-1", want: "ROTATION_INTERVAL_DAYS"},
		{name: "oversized rotation", value: "ROTATION_INTERVAL_DAYS=366", want: "ROTATION_INTERVAL_DAYS"},
		{name: "NaN rotation", value: "ROTATION_INTERVAL_DAYS=NaN", want: "ROTATION_INTERVAL_DAYS"},
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

func assertWarningVariables(t *testing.T, warnings []warning, variables ...string) {
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

func TestLoadEnvironRotationInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "default is two weeks", want: 14 * 24 * time.Hour},
		{name: "custom days", value: "ROTATION_INTERVAL_DAYS=7", want: 7 * 24 * time.Hour},
		{name: "fractional days", value: "ROTATION_INTERVAL_DAYS=0.5", want: 12 * time.Hour},
		{name: "zero disables", value: "ROTATION_INTERVAL_DAYS=0", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environ := []string{"TV_IPS=192.0.2.10"}
			if test.value != "" {
				environ = append(environ, test.value)
			}
			cfg, _, err := LoadEnviron(environ)
			if err != nil {
				t.Fatalf("LoadEnviron() error = %v", err)
			}
			if got := cfg.RotationInterval(); got != test.want {
				t.Fatalf("RotationInterval() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestLoadEnvironSlideshowIntervalDependsOnMode(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		valid   bool
		show    time.Duration
	}{
		{name: "app mode accepts any minute count", environ: []string{"SLIDESHOW_INTERVAL=30"}, valid: true, show: 30 * time.Minute},
		{name: "app mode is the default", environ: nil, valid: true, show: 15 * time.Minute},
		{name: "app mode off", environ: []string{"SLIDESHOW_ENABLED=false"}, valid: true},
		{name: "tv mode needs a TV interval", environ: []string{"SLIDESHOW_MODE=tv", "SLIDESHOW_INTERVAL=30"}},
		{name: "tv mode never rotates itself", environ: []string{"SLIDESHOW_MODE=TV", "SLIDESHOW_INTERVAL=60"}, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, _, err := LoadEnviron(append([]string{"TV_IPS=192.0.2.10"}, test.environ...))
			if (err == nil) != test.valid {
				t.Fatalf("LoadEnviron() error = %v, want valid %t", err, test.valid)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "SLIDESHOW_INTERVAL") {
					t.Fatalf("LoadEnviron() error = %v, want SLIDESHOW_INTERVAL", err)
				}
				return
			}
			if got := cfg.ShowInterval(); got != test.show {
				t.Fatalf("ShowInterval() = %s, want %s", got, test.show)
			}
		})
	}
}

func TestMatteStyleIsTrimmedAndBoundedAtStartup(t *testing.T) {
	t.Parallel()
	cfg, _, err := LoadEnviron([]string{"TV_IPS=192.0.2.10", "MATTE_STYLE= shadowbox_polar "})
	if err != nil || cfg.MatteStyle != "shadowbox_polar" {
		t.Fatalf("LoadEnviron() matte = %q, %v; want the surrounding spaces removed", cfg.MatteStyle, err)
	}
	_, _, err = LoadEnviron([]string{"TV_IPS=192.0.2.10", "MATTE_STYLE=" + strings.Repeat("m", 129)})
	if err == nil || !strings.Contains(err.Error(), "MATTE_STYLE") {
		t.Fatalf("LoadEnviron() error = %v, want an overlong MATTE_STYLE rejected at startup", err)
	}
}

func TestLoadEnvironAcceptsGraceWindowsShorterThanADay(t *testing.T) {
	cfg, _, err := LoadEnviron([]string{"TV_IPS=192.0.2.10", "AUTO_OFF_GRACE_HOURS=23.5", "AUTO_ON_GRACE_HOURS=23.5"})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	if cfg.AutoOffGraceHours != 23.5 || cfg.AutoOnGraceHours != 23.5 {
		t.Fatalf("grace hours = %v / %v, want 23.5", cfg.AutoOffGraceHours, cfg.AutoOnGraceHours)
	}
}

func TestLoadEnvironTreatsEmptySlideshowVariablesAsUnset(t *testing.T) {
	tests := []struct {
		name         string
		environ      []string
		wantOverride bool
		wantEnabled  bool
	}{
		{name: "empty switch", environ: []string{"SLIDESHOW_ENABLED="}},
		{name: "blank switch", environ: []string{"SLIDESHOW_ENABLED=  "}},
		{name: "every variable empty", environ: []string{"SLIDESHOW_ENABLED=", "SLIDESHOW_INTERVAL=", "SLIDESHOW_TYPE="}},
		{name: "explicit off", environ: []string{"SLIDESHOW_ENABLED=false"}, wantOverride: true, wantEnabled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, _, err := LoadEnviron(append([]string{"TV_IPS=192.0.2.10"}, test.environ...))
			if err != nil {
				t.Fatalf("LoadEnviron() error = %v", err)
			}
			if cfg.SlideshowOverride != test.wantOverride || cfg.SlideshowEnabledSet != test.wantEnabled {
				t.Fatalf("override = %v, enabled set = %v; want %v, %v",
					cfg.SlideshowOverride, cfg.SlideshowEnabledSet, test.wantOverride, test.wantEnabled)
			}
			if cfg.SlideshowIntervalSet || cfg.SlideshowTypeSet {
				t.Fatal("empty interval and type must count as unset")
			}
		})
	}
}

func TestLoadEnvironWarnsWhenPowerScheduleHasNoTimezone(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    []string
	}{
		{name: "auto off without timezone", environ: []string{"AUTO_OFF_TIME=23:00"}, want: []string{"AUTO_OFF_TIME"}},
		{
			name:    "both times without timezone",
			environ: []string{"AUTO_OFF_TIME=23:00", "AUTO_ON_TIME=07:00", "TV_MAC=AA:BB:CC:DD:EE:FF"},
			want:    []string{"AUTO_OFF_TIME", "AUTO_ON_TIME"},
		},
		{name: "empty timezone counts as unset", environ: []string{"AUTO_OFF_TIME=23:00", "LOCATION_TIMEZONE="}, want: []string{"AUTO_OFF_TIME"}},
		{name: "timezone set", environ: []string{"AUTO_OFF_TIME=23:00", "LOCATION_TIMEZONE=America/Denver"}},
		{name: "explicit UTC is a choice", environ: []string{"AUTO_OFF_TIME=23:00", "LOCATION_TIMEZONE=UTC"}},
		{name: "no schedule", environ: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, warnings, err := LoadEnviron(append([]string{"TV_IPS=192.0.2.10"}, test.environ...))
			if err != nil {
				t.Fatalf("LoadEnviron() error = %v, a missing timezone must not fail startup", err)
			}
			if cfg.Timezone == "" {
				t.Fatal("Timezone must keep its default")
			}
			assertWarningVariables(t, warnings, test.want...)
			for _, item := range warnings {
				if !strings.Contains(item.Message, "UTC") || !strings.Contains(item.Message, "LOCATION_TIMEZONE") {
					t.Fatalf("warning = %+v, want it to name UTC and LOCATION_TIMEZONE", item)
				}
			}
		})
	}
}

func TestLoadEnvironWarnsThatSkipTLSVerifyIsIgnored(t *testing.T) {
	for _, value := range []string{"true", "false", "sure"} {
		t.Run(value, func(t *testing.T) {
			cfg, warnings, err := LoadEnviron([]string{"TV_IPS=192.0.2.10", "VERIFY_TLS=true", "SKIP_TLS_VERIFY=" + value})
			if err != nil {
				t.Fatalf("LoadEnviron() error = %v, a retired variable must not fail startup", err)
			}
			if !cfg.VerifyTLS {
				t.Fatal("SKIP_TLS_VERIFY must no longer change VERIFY_TLS")
			}
			assertWarningVariables(t, warnings, "SKIP_TLS_VERIFY")
			if !strings.Contains(warnings[0].Message, "VERIFY_TLS") || !strings.Contains(warnings[0].Message, "ignored") {
				t.Fatalf("warning = %+v, want it to say the variable is ignored and name VERIFY_TLS", warnings[0])
			}
		})
	}
	_, warnings, err := LoadEnviron([]string{"TV_IPS=192.0.2.10", "SKIP_TLS_VERIFY="})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	assertWarningVariables(t, warnings)
}
