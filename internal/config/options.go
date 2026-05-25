package config

import (
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

// SyncPolicy holds operational settings for a TV sync reconciliation run.
type SyncPolicy struct {
	DryRun              bool
	RemoveUnknownImages bool
	SlideshowOverride   bool
	ArtworkDir          string
	UploadDelay         time.Duration
	UploadAttempts      int
	SyncIntervalMin     int
	MatteStyle          string
}

// SyncPolicy returns sync reconciliation settings derived from config.
func (c *Config) SyncPolicy() SyncPolicy {
	attempts := c.UploadAttempts
	if attempts < 1 {
		attempts = 1
	}
	return SyncPolicy{
		DryRun:              c.DryRun,
		RemoveUnknownImages: c.RemoveUnknownImages,
		SlideshowOverride:   c.SlideshowOverride,
		ArtworkDir:          c.ArtworkDir,
		UploadDelay:         c.UploadDelay,
		UploadAttempts:      attempts,
		SyncIntervalMin:     c.SyncIntervalMin,
		MatteStyle:          c.MatteStyle,
	}
}

// TVConnectOptions holds settings used when opening a TV connection.
type TVConnectOptions struct {
	TVMAC             string
	EnableRESTGate    bool
	SkipTLSVerify     bool
	ClientName        string
	TokenDir          string
	ConnectionTimeout time.Duration
	APITimeout        time.Duration
	GateTimeout       time.Duration
	MatteStyle        string
}

// TVConnectOptions returns TV connection settings derived from config.
func (c *Config) TVConnectOptions() TVConnectOptions {
	return TVConnectOptions{
		TVMAC:             c.TVMAC,
		EnableRESTGate:    c.EnableRESTGate,
		SkipTLSVerify:     !c.VerifyTLS,
		ClientName:        c.ClientName,
		TokenDir:          c.TokenDir,
		ConnectionTimeout: c.ConnectionTimeout,
		APITimeout:        c.APITimeout,
		GateTimeout:       c.GateTimeout,
		MatteStyle:        c.MatteStyle,
	}
}

// OptimizeOptions returns image optimization settings derived from config.
func (c *Config) OptimizeOptions() optimize.Config {
	return optimize.Config{
		Enabled:             c.OptimizeEnabled,
		SmartCropEnabled:    c.SmartCropEnabled,
		MaxWidth:            c.OptimizeMaxWidth,
		MaxHeight:           c.OptimizeMaxHeight,
		OptimizeJPEGQuality: c.OptimizeJPEGQuality,
		MuseumModeEnabled:   c.MuseumModeEnabled,
		MuseumModeIntensity: c.MuseumModeIntensity,
	}
}
