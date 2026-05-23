package sync

import (
	"fmt"
	"log/slog"

	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const (
	ssTypeShuffle    = "shuffleslideshow"
	ssTypeSequential = "slideshow"
)

// ReconcileInput is the per-TV inventory reconciliation request.
type ReconcileInput struct {
	LocalFiles        map[string]struct{}
	Mapping           map[string]string
	MatteOverrides    map[string]string
	DesiredBrightness *int
	Slideshow         *samsung.SlideshowStatus
	TriggerAutoOff    bool
}

// TVSyncResult is the outcome of a single TV reconciliation run.
type TVSyncResult struct {
	IP           string
	Model        string
	Status       string
	ArtMode      bool
	Uploaded     int
	Deleted      int
	TotalImages  int
	Brightness   string
	Slideshow    string
	ErrorMessage string

	NewUploads   map[string]string
	DeletedFiles []string
}

// BuildReconcileInput assembles TV-side sync policy for one reconciliation run.
func BuildReconcileInput(
	cfg *config.Config,
	localFiles map[string]struct{},
	mapping map[string]string,
	matteConfig *MatteConfig,
	logger *slog.Logger,
) ReconcileInput {
	matteOverrides := make(map[string]string, len(localFiles))
	for filename := range localFiles {
		matteOverrides[filename] = matteConfig.GetMatte(filename, cfg.MatteStyle)
	}

	triggerAutoOff := IsWithinAutoOffWindow(cfg.AutoOffTime, cfg.AutoOffGraceHours, cfg.Timezone)
	if triggerAutoOff {
		logger.Info("within auto-off window, preparing TV power-off trigger",
			"off_time", cfg.AutoOffTime,
			"grace_hours", FormatGraceDisplay(cfg.AutoOffGraceHours),
		)
	}

	return ReconcileInput{
		LocalFiles:        localFiles,
		Mapping:           mapping,
		MatteOverrides:    matteOverrides,
		DesiredBrightness: determineBrightness(cfg, logger),
		Slideshow:         determineSlideshowSettings(cfg, logger),
		TriggerAutoOff:    triggerAutoOff,
	}
}

func determineSlideshowSettings(cfg *config.Config, logger *slog.Logger) *samsung.SlideshowStatus {
	if !cfg.SlideshowOverride || !cfg.SlideshowEnabled {
		return nil
	}

	ssType := ssTypeShuffle
	if cfg.SlideshowType == "sequential" || cfg.SlideshowType == "order" {
		ssType = ssTypeSequential
	}

	interval := fmt.Sprintf("%d", cfg.SlideshowInterval)

	isValid := false
	supported := []string{"3", "15", "60", "720", "1440", "10080"}
	for _, s := range supported {
		if interval == s {
			isValid = true
			break
		}
	}

	if !isValid {
		logger.Warn("invalid slideshow interval detected for 2024 model, defaulting to 3m shuffle",
			"requested", interval,
			"supported", supported)
		interval = "3"
		ssType = ssTypeShuffle
	}

	return &samsung.SlideshowStatus{
		Value:      interval,
		Type:       ssType,
		CategoryID: "MY-C0002",
	}
}

func determineBrightness(cfg *config.Config, logger *slog.Logger) *int {
	return brightness.GetTargetValue(brightness.Config{
		SolarEnabled:     cfg.SolarEnabled,
		Latitude:         cfg.Latitude,
		Longitude:        cfg.Longitude,
		Timezone:         cfg.Timezone,
		BrightnessMin:    cfg.BrightnessMin,
		BrightnessMax:    cfg.BrightnessMax,
		ManualBrightness: cfg.ManualBrightness,
	}, logger)
}
