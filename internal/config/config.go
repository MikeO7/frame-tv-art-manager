// Package config loads and validates all application configuration from
// environment variables. It produces a single Config struct that is passed
// by pointer to every other package — no global state.
package config

import (
	"net"
	"time"
)

// Config holds all application settings. It is populated by Load() and then
// treated as read-only for the lifetime of the process.
type Config struct {
	// TVIPs is the list of TV IP addresses to sync artwork to.
	TVIPs []string

	// ArtworkDir is the local directory containing artwork images.
	ArtworkDir string

	// MaxArtworkImages caps how many images the manager adds to ArtworkDir
	// (downloads and uploads) and how many it syncs to each TV. A larger local
	// folder is never an error: the first MaxArtworkImages images by file name
	// are synced. 0 means no limit (fill the TV to its capacity).
	MaxArtworkImages int

	// MaxDownloadSizeMB is the maximum allowed size for a single download or upload.
	MaxDownloadSizeMB int

	// MaxLocalFileSizeMB is the largest artwork file accepted from the local
	// folder. Larger files are excluded from the collection.
	MaxLocalFileSizeMB int

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

	// SlideshowEnabledSet, SlideshowIntervalSet, and SlideshowTypeSet record
	// which slideshow variables were set. Without SLIDESHOW_ENABLED the
	// manager only adjusts a slideshow that is already playing.
	SlideshowEnabledSet  bool
	SlideshowIntervalSet bool
	SlideshowTypeSet     bool

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

	// --- Rotation ---

	// RotationIntervalDays is how long each artwork stays on a full TV before
	// it is swapped for artwork that is waiting. Zero disables rotation.
	RotationIntervalDays float64

	// --- Auto-Off ---

	// AutoOffTime is a 24-hour time string (e.g. "22:00") at which TVs in
	// art mode should be powered off. Empty string disables the feature.
	AutoOffTime string

	// AutoOffGraceHours is how long after AutoOffTime to keep trying.
	AutoOffGraceHours float64

	// AutoOnTime is a 24-hour time string at which a TV that is off is woken
	// with Wake-on-LAN, once per day. Empty disables scheduled wake. It needs
	// TVMAC.
	AutoOnTime string

	// AutoOnGraceHours is how long after AutoOnTime a wake may still be sent.
	AutoOnGraceHours float64

	// --- Wake-on-LAN ---

	// TVMAC is the six-byte Wake-on-LAN address parsed from TV_MAC. It is nil
	// when TV_MAC is unset or ambiguous because several TVs are configured.
	TVMAC net.HardwareAddr

	// WakeBroadcast is the IPv4 address Wake-on-LAN packets are sent to, from
	// TV_WOL_BROADCAST. Empty means the limited broadcast plus the TV's /24
	// directed broadcast.
	WakeBroadcast string

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

	// UnsplashAccessKey is the Access Key for Unsplash.
	UnsplashAccessKey string

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
	// JPEGSubsampling is the JPEG chroma sampling: "444" (full-resolution
	// color, default) or "420" (smaller files, softer color edges).
	JPEGSubsampling string
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
	// PortraitMode controls how vertical photos are resized: "collage", "pad", "crop" (default "pad").
	PortraitMode string
	// PadStyle is how padded images are presented: "mat" (gallery mat, default) or "blur".
	PadStyle string
	// MaxCropPercent is the largest share of a landscape image that may be
	// cropped away to fill the frame before it is padded instead (default 25).
	MaxCropPercent int
	// MaxUpscale caps how far a small image is enlarged; images needing more
	// to fill the frame are padded at this scale (default 3).
	MaxUpscale float64
	// MatTone selects the mat tone: "auto" (follows the art, default),
	// "light", or "dark" (calmest at night).
	MatTone string
}
