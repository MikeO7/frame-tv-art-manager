// Package config loads and validates all application configuration from
// environment variables. It produces a single Config struct that is passed
// by pointer to every other package — no global state.
package config

import (
	"time"
)

// Config holds all application settings. It is populated by Load() and then
// treated as read-only for the lifetime of the process.
type Config struct {
	// TVIPs is the list of TV IP addresses to sync artwork to.
	TVIPs []string

	// ArtworkDir is the local directory containing artwork images.
	ArtworkDir string

	// MaxArtworkImages is the maximum number of images allowed in ArtworkDir.
	// 0 means no local limit (fills to the TV's maximum available storage capacity).
	MaxArtworkImages int

	// MaxDownloadSizeMB is the maximum allowed size for a single download.
	MaxDownloadSizeMB int

	// TokenDir is the directory for storing TV auth tokens and mappings.
	TokenDir string

	// SyncIntervalMin is the number of minutes between sync cycles.
	SyncIntervalMin int

	// ShutdownTimeout bounds graceful shutdown of application children and resources.
	ShutdownTimeout time.Duration

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

	// VerifyTLS enables TLS/SSL certificate verification when connecting to the TV.
	// Default is false since Frame TVs use self-signed certificates.
	VerifyTLS bool

	// --- Image Sources ---

	// SourcesFile is the path to a text file containing image URLs to download.
	// One URL per line. Lines starting with # are comments. Empty disables.
	SourcesFile string

	// UnsplashAppID is the Application ID for Unsplash.
	UnsplashAppID string

	// UnsplashAccessKey is the Access Key for Unsplash.
	UnsplashAccessKey string

	// UnsplashSecretKey is the Secret Key for Unsplash.
	UnsplashSecretKey string

	// NasaAPIKey is the API key for NASA APOD downloads.
	// Defaults to DEMO_KEY if empty.
	NasaAPIKey string

	// PexelsAPIKey is the API key for Pexels image downloads.
	PexelsAPIKey string

	// PixabayAPIKey is the API key for Pixabay image downloads.
	PixabayAPIKey string

	// --- Image Optimization ---

	// OptimizeEnabled enables automatic image resizing for oversized images.
	OptimizeEnabled bool

	// SmartCropEnabled uses saliency analysis to choose the crop area (default false).
	SmartCropEnabled bool
	// SmartCropMinGain is the minimum fractional score improvement over center crop.
	SmartCropMinGain float64
	// SmartCropProtection enables penalties for crops that bisect protected detail regions.
	SmartCropProtection bool
	// SmartCropProtectionStrength controls protected-region boundary penalties.
	SmartCropProtectionStrength float64
	// SmartCropProvider selects local analysis or the optional HTTP crop provider.
	SmartCropProvider string
	// SmartCropProviderURL is required only for the opt-in HTTP crop provider.
	SmartCropProviderURL string
	// SmartCropProviderMinConfidence rejects uncertain external crop proposals.
	SmartCropProviderMinConfidence float64
	// SmartCropProviderTimeout bounds the optional external crop request.
	SmartCropProviderTimeout time.Duration

	// OptimizeMaxWidth is the exact target image width (default 3840 for 4K).
	OptimizeMaxWidth int

	// OptimizeMaxHeight is the exact target image height (default 2160 for 4K).
	OptimizeMaxHeight int
	// OptimizeMaxPixels bounds target pixels and peak transform memory.
	OptimizeMaxPixels int
	// OptimizeMemoryMB bounds the estimated peak bytes of one transform.
	OptimizeMemoryMB int

	// OptimizeJPEGQuality is the JPEG encoding quality (1-100, default 95).
	OptimizeJPEGQuality int
	// OptimizePNG enables the same geometry/effect pipeline for PNG inputs.
	OptimizePNG bool
	// LinearLightResize performs crop/resampling in linear-light 16-bit RGBA.
	LinearLightResize bool
	// SharpenAmount controls bounded post-resize unsharp masking; zero disables it.
	SharpenAmount float64
	// SharpenThreshold suppresses sharpening for small 8-bit luminance differences.
	SharpenThreshold int
	// ColorProfilePolicy is convert-srgb, assume-srgb, or reject-embedded.
	ColorProfilePolicy string
	// HDRToneMap enables metadata-gated ITU-R BT.2446 Method A conversion to SDR.
	HDRToneMap bool
	// HDRSourcePeakNits is the assumed mastering peak when metadata does not declare one.
	HDRSourcePeakNits float64
	// HDRTargetPeakNits is the target SDR peak luminance.
	HDRTargetPeakNits float64

	// MuseumModeEnabled applies canvas texture, black lifting, and warming for a "real art" look.
	MuseumModeEnabled bool

	// MuseumModeIntensity controls the strength of the museum effects (1-10, default 5).
	MuseumModeIntensity int

	// --- Health Server ---

	// HealthPort is the HTTP port for /health and /status endpoints.
	// 0 disables the health server.
	HealthPort int
	// HealthBindAddress is the local IP address used by the HTTP listener.
	HealthBindAddress string

	// UploadEnabled enables the HTTP upload endpoint on the health server when true.
	UploadEnabled bool
	// UploadToken is the HTTP Basic-auth password for the upload endpoint.
	UploadToken string

	// --- Timeouts ---

	// ConnectionTimeout is the max time to wait for a WSS handshake.
	ConnectionTimeout time.Duration

	// APITimeout is the max time to wait for art API responses.
	APITimeout time.Duration

	// UploadDelay is the pause between consecutive TV mutations. The legacy
	// environment name is retained for compatibility.
	UploadDelay time.Duration

	// UploadAttempts bounds retries proven not to have reached the TV. Ambiguous
	// or definitely applied outcomes are never retried.
	UploadAttempts int

	// GateTimeout is the HTTP timeout for the REST gate probe.
	GateTimeout time.Duration

	// --- Ownership ---
	// PUID and PGID for directory ownership (optional).
	PUID int
	PGID int

	// --- Portrait Mode Handling ---
	// PortraitMode controls how vertical photos are resized: "collage", "pad", "crop" (default "crop").
	PortraitMode string
}
