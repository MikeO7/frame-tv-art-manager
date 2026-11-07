package sync

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const slideshowSequentialConfig = "sequential"

func convergencePolicy(
	cfg *config.Config,
	mattes *config.MatteConfig,
	now time.Time,
	logger *slog.Logger,
) (reconcile.Policy, error) {
	matteOverrides, defaultMatte, err := convergenceMattes(cfg.MatteStyle, mattes)
	if err != nil {
		return reconcile.Policy{}, err
	}
	policy := reconcile.Policy{
		RemoveUnknown:  cfg.RemoveUnknownImages,
		DefaultMatte:   defaultMatte,
		PortraitMatte:  cfg.PortraitMatte,
		MatteOverrides: matteOverrides,
		Rotation:       cfg.RotationInterval(),
	}
	if cfg.TVMAC != nil {
		if boundary, due := autoOnBoundaryAt(cfg.AutoOnTime, cfg.AutoOnGraceHours, cfg.Timezone, now); due {
			policy.Power, policy.WakeBoundary = reconcile.PowerOn, boundary
		}
	}
	if isWithinAutoOffWindowAt(cfg.AutoOffTime, cfg.AutoOffGraceHours, cfg.Timezone, now) {
		policy.Power, policy.WakeBoundary = reconcile.PowerOff, time.Time{}
	}
	policy.Show, policy.Slideshow = pictureChangePolicy(cfg)
	location := (*brightness.SolarLocation)(nil)
	if cfg.SolarEnabled {
		location = &brightness.SolarLocation{
			Latitude: cfg.Latitude, Longitude: cfg.Longitude, Timezone: cfg.Timezone,
		}
	}
	if target := brightness.GetTargetValueAt(brightness.TargetOptions{
		Location: location, Min: cfg.BrightnessMin, Max: cfg.BrightnessMax,
		Manual: cfg.ManualBrightness, Now: now,
	}, logger); target != nil {
		policy.Brightness = reconcile.SettingPolicy{Mode: reconcile.PolicySet, Value: *target}
	}
	return policy, nil
}

func convergenceMattes(global string, mattes *config.MatteConfig) (reconcile.MatteOverrides, string, error) {
	if mattes == nil {
		return reconcile.MatteOverrides{}, global, nil
	}
	entries := make([]reconcile.MatteOverride, 0, len(mattes.Overrides))
	for filename, matte := range mattes.Overrides {
		entries = append(entries, reconcile.MatteOverride{Filename: filename, Matte: matte})
	}
	overrides, err := reconcile.NewMatteOverrides(entries)
	if err != nil {
		return reconcile.MatteOverrides{}, "", fmt.Errorf("configure artwork matte overrides: %w", err)
	}
	defaultMatte := global
	if mattes.DefaultMatte != "" {
		defaultMatte = mattes.DefaultMatte
	}
	if defaultMatte != "" && (defaultMatte != strings.TrimSpace(defaultMatte) || len(defaultMatte) > 128) {
		return reconcile.MatteOverrides{}, "", fmt.Errorf("configure default artwork matte: value is invalid")
	}
	return overrides, defaultMatte, nil
}

// pictureChangePolicy decides who changes the picture on screen. In app mode
// the manager picks each picture and leaves the TV's slideshow setting alone,
// since selecting a picture stops it anyway. In tv mode the TV's own
// slideshow is managed as configured.
func pictureChangePolicy(cfg *config.Config) (reconcile.ShowPolicy, reconcile.SlideshowPolicy) {
	sequential := cfg.SlideshowType == slideshowSequentialConfig
	if interval := cfg.ShowInterval(); interval > 0 {
		show := reconcile.ShowPolicy{Interval: interval, Order: reconcile.ShowShuffle}
		if sequential {
			show.Order = reconcile.ShowSequential
		}
		return show, reconcile.SlideshowPolicy{}
	}
	if !cfg.SlideshowOverride || cfg.SlideshowMode != config.SlideshowModeTV {
		return reconcile.ShowPolicy{}, reconcile.SlideshowPolicy{}
	}
	kind := samsung.SlideshowShuffle
	if sequential {
		kind = samsung.SlideshowSequential
	}
	switch {
	case cfg.SlideshowEnabled:
		return reconcile.ShowPolicy{}, reconcile.SlideshowPolicy{
			Mode: reconcile.PolicySet, Setting: samsung.SlideshowSetting{Interval: cfg.SlideshowInterval, Kind: kind},
		}
	case cfg.SlideshowEnabledSet:
		return reconcile.ShowPolicy{}, reconcile.SlideshowPolicy{
			Mode: reconcile.PolicyDisable, Setting: samsung.SlideshowSetting{Kind: kind},
		}
	}
	// Only the interval or type was configured: tune a slideshow the owner
	// already runs, and never switch one on or off.
	adjust := reconcile.SlideshowPolicy{Mode: reconcile.PolicyAdjust}
	if cfg.SlideshowIntervalSet {
		adjust.Setting.Interval = cfg.SlideshowInterval
	}
	if cfg.SlideshowTypeSet {
		adjust.Setting.Kind = kind
	}
	return reconcile.ShowPolicy{}, adjust
}
