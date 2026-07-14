package sync

import (
	"log/slog"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestConvergencePolicyMapsDynamicOperatorSettings(t *testing.T) {
	brightness := 7
	now := time.Date(2026, time.July, 14, 22, 30, 0, 0, time.UTC)
	policy, err := convergencePolicy(&config.Config{
		RemoveUnknownImages: true, MatteStyle: "shadowbox_warm",
		SlideshowOverride: true, SlideshowEnabled: true,
		SlideshowInterval: 60, SlideshowType: "shuffle",
		ManualBrightness: &brightness, BrightnessMin: 2, BrightnessMax: 10,
		AutoOffTime: "22:00", AutoOffGraceHours: 1, Timezone: "UTC",
	}, nil, now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("convergencePolicy() error = %v", err)
	}

	if !policy.RemoveUnknown || policy.Select || policy.AllowEmpty || policy.DefaultMatte != "shadowbox_warm" ||
		policy.Power != reconcile.PowerOff ||
		policy.Slideshow.Mode != reconcile.PolicySet ||
		policy.Slideshow.Setting != (samsung.SlideshowSetting{Interval: 60, Kind: samsung.SlideshowShuffle}) ||
		policy.Brightness != (reconcile.SettingPolicy{Mode: reconcile.PolicySet, Value: 7}) {
		t.Fatalf("convergence policy = %+v", policy)
	}
}

func TestConvergencePolicyPreservesCurrentSelection(t *testing.T) {
	t.Parallel()

	policy, err := convergencePolicy(
		&config.Config{Timezone: "UTC"}, nil,
		time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("convergencePolicy() error = %v", err)
	}
	if policy.Select {
		t.Fatal("cached convergence policy would change the operator's current artwork selection")
	}
}

func TestConvergencePolicyWakesOnlyOneKnownConfiguredTV(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	logger := slog.New(slog.DiscardHandler)

	one, err := convergencePolicy(&config.Config{
		TVIPs: []string{"192.0.2.10"}, TVMAC: "aa:bb:cc:dd:ee:ff", Timezone: "UTC",
	}, nil, now, logger)
	if err != nil || one.Power != reconcile.WakeWhenKnownOff {
		t.Fatalf("single-TV policy = %+v, %v", one, err)
	}
	multiple, err := convergencePolicy(&config.Config{
		TVIPs: []string{"192.0.2.10", "192.0.2.11"}, TVMAC: "aa:bb:cc:dd:ee:ff", Timezone: "UTC",
	}, nil, now, logger)
	if err != nil || multiple.Power != reconcile.PowerPreserve {
		t.Fatalf("multi-TV policy = %+v, %v", multiple, err)
	}
}

func TestConvergencePolicyPreservesUnsetSettingsAndDisablesExplicitSlideshow(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	preserve, err := convergencePolicy(&config.Config{Timezone: "UTC"}, nil, now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("preserve convergencePolicy() error = %v", err)
	}
	if preserve.Power != reconcile.PowerPreserve || preserve.Slideshow.Mode != reconcile.PolicyPreserve ||
		preserve.Brightness.Mode != reconcile.PolicyPreserve {
		t.Fatalf("preserve policy = %+v", preserve)
	}

	disable, err := convergencePolicy(&config.Config{
		Timezone: "UTC", SlideshowOverride: true, SlideshowType: "sequential",
	}, nil, now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("disable convergencePolicy() error = %v", err)
	}
	if disable.Slideshow.Mode != reconcile.PolicyDisable ||
		disable.Slideshow.Setting != (samsung.SlideshowSetting{Kind: samsung.SlideshowSequential}) {
		t.Fatalf("disable slideshow policy = %+v", disable.Slideshow)
	}
}

func TestConvergencePolicyCopiesConfiguredMatteMapping(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	policy, err := convergencePolicy(
		&config.Config{Timezone: "UTC", MatteStyle: "global"},
		&config.MatteConfig{
			DefaultMatte: "file-default",
			Overrides: map[string]string{
				"z.jpg": "modern_black",
				"a.jpg": "shadowbox_warm",
			},
		},
		now,
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("convergencePolicy() error = %v", err)
	}
	want, err := reconcile.NewMatteOverrides([]reconcile.MatteOverride{
		{Filename: "a.jpg", Matte: "shadowbox_warm"},
		{Filename: "z.jpg", Matte: "modern_black"},
	})
	if err != nil {
		t.Fatalf("NewMatteOverrides() error = %v", err)
	}
	if policy.DefaultMatte != "file-default" || policy.MatteOverrides != want {
		t.Fatalf("matte policy = %+v", policy)
	}

	_, err = convergencePolicy(
		&config.Config{Timezone: "UTC"},
		&config.MatteConfig{Overrides: map[string]string{"../unsafe.jpg": "none"}},
		now,
		slog.New(slog.DiscardHandler),
	)
	if err == nil {
		t.Fatal("convergencePolicy() accepted an unsafe matte filename")
	}
	_, err = convergencePolicy(
		&config.Config{Timezone: "UTC"},
		&config.MatteConfig{DefaultMatte: " invalid"},
		now,
		slog.New(slog.DiscardHandler),
	)
	if err == nil {
		t.Fatal("convergencePolicy() accepted an unnormalized default matte")
	}
}
