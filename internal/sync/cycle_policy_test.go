package sync

import (
	"log/slog"
	"net"
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
		SlideshowMode: config.SlideshowModeTV, SlideshowOverride: true, SlideshowEnabled: true,
		SlideshowInterval: 60, SlideshowType: "shuffle",
		ManualBrightness: &brightness, BrightnessMin: 2, BrightnessMax: 10,
		AutoOffTime: "22:00", AutoOffGraceHours: 1, Timezone: "UTC",
	}, nil, now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("convergencePolicy() error = %v", err)
	}

	if !policy.RemoveUnknown || policy.Show != (reconcile.ShowPolicy{}) || policy.AllowEmpty || policy.DefaultMatte != "shadowbox_warm" ||
		policy.Power != reconcile.PowerOff ||
		policy.Slideshow.Mode != reconcile.PolicySet ||
		policy.Slideshow.Setting != (samsung.SlideshowSetting{Interval: 60, Kind: samsung.SlideshowShuffle}) ||
		policy.Brightness != (reconcile.SettingPolicy{Mode: reconcile.PolicySet, Value: 7}) {
		t.Fatalf("convergence policy = %+v", policy)
	}
}

func TestConvergencePolicyShowsArtByDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  config.Config
		want reconcile.ShowPolicy
	}{
		{
			name: "default shuffles every interval",
			cfg:  config.Config{SlideshowMode: config.SlideshowModeApp, SlideshowInterval: 15, SlideshowType: "shuffle"},
			want: reconcile.ShowPolicy{Interval: 15 * time.Minute, Order: reconcile.ShowShuffle},
		},
		{
			name: "sequential order",
			cfg:  config.Config{SlideshowMode: config.SlideshowModeApp, SlideshowInterval: 3, SlideshowType: "sequential"},
			want: reconcile.ShowPolicy{Interval: 3 * time.Minute, Order: reconcile.ShowSequential},
		},
		{
			name: "explicitly enabled",
			cfg: config.Config{
				SlideshowMode: config.SlideshowModeApp, SlideshowInterval: 60, SlideshowType: "shuffle",
				SlideshowEnabled: true, SlideshowEnabledSet: true, SlideshowOverride: true,
			},
			want: reconcile.ShowPolicy{Interval: time.Hour},
		},
		{
			name: "disabled leaves the picture to the TV",
			cfg: config.Config{
				SlideshowMode: config.SlideshowModeApp, SlideshowInterval: 15, SlideshowType: "shuffle",
				SlideshowEnabledSet: true, SlideshowOverride: true,
			},
		},
		{
			name: "tv mode manages only the TV slideshow",
			cfg:  config.Config{SlideshowMode: config.SlideshowModeTV, SlideshowInterval: 15, SlideshowType: "shuffle"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := test.cfg
			cfg.Timezone = "UTC"
			policy, err := convergencePolicy(&cfg, nil, time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC),
				slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("convergencePolicy() error = %v", err)
			}
			if policy.Show != test.want {
				t.Fatalf("show policy = %+v, want %+v", policy.Show, test.want)
			}
			if policy.Show.Interval > 0 && policy.Slideshow.Mode != reconcile.PolicyPreserve {
				t.Fatalf("app rotation also managed the TV slideshow: %+v", policy.Slideshow)
			}
		})
	}
}

func TestConvergencePolicyWakesOnceAtTheScheduledTime(t *testing.T) {
	t.Parallel()
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	boundary := time.Date(2026, time.July, 14, 7, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		cfg          config.Config
		now          time.Time
		wantPower    reconcile.PowerPolicy
		wantBoundary time.Time
	}{
		{name: "MAC alone never wakes", cfg: config.Config{TVMAC: mac}, now: boundary.Add(time.Hour)},
		{
			name: "inside the wake window", cfg: config.Config{TVMAC: mac, AutoOnTime: "07:00", AutoOnGraceHours: 2},
			now: boundary.Add(90 * time.Minute), wantPower: reconcile.PowerOn, wantBoundary: boundary,
		},
		{
			name: "before the wake window", cfg: config.Config{TVMAC: mac, AutoOnTime: "07:00", AutoOnGraceHours: 2},
			now: boundary.Add(-time.Minute),
		},
		{
			name: "after the wake window", cfg: config.Config{TVMAC: mac, AutoOnTime: "07:00", AutoOnGraceHours: 2},
			now: boundary.Add(2 * time.Hour),
		},
		{
			name: "window crossing midnight", cfg: config.Config{TVMAC: mac, AutoOnTime: "23:30", AutoOnGraceHours: 2},
			now: time.Date(2026, time.July, 15, 0, 30, 0, 0, time.UTC), wantPower: reconcile.PowerOn,
			wantBoundary: time.Date(2026, time.July, 14, 23, 30, 0, 0, time.UTC),
		},
		{name: "no MAC", cfg: config.Config{AutoOnTime: "07:00", AutoOnGraceHours: 2}, now: boundary.Add(time.Minute)},
		{
			name: "auto-off wins", now: boundary.Add(time.Minute), wantPower: reconcile.PowerOff,
			cfg: config.Config{
				TVMAC: mac, AutoOnTime: "07:00", AutoOnGraceHours: 2, AutoOffTime: "06:30", AutoOffGraceHours: 2,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := test.cfg
			cfg.TVIPs, cfg.Timezone = []string{"192.0.2.10"}, "UTC"
			policy, err := convergencePolicy(&cfg, nil, test.now, slog.New(slog.DiscardHandler))
			if err != nil || policy.Power != test.wantPower || !policy.WakeBoundary.Equal(test.wantBoundary) {
				t.Fatalf("policy power %v boundary %v, %v; want %v at %v",
					policy.Power, policy.WakeBoundary, err, test.wantPower, test.wantBoundary)
			}
		})
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
		Timezone: "UTC", SlideshowMode: config.SlideshowModeTV, SlideshowOverride: true, SlideshowEnabledSet: true,
		SlideshowType: "sequential",
	}, nil, now, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("disable convergencePolicy() error = %v", err)
	}
	if disable.Slideshow.Mode != reconcile.PolicyDisable ||
		disable.Slideshow.Setting != (samsung.SlideshowSetting{Kind: samsung.SlideshowSequential}) {
		t.Fatalf("disable slideshow policy = %+v", disable.Slideshow)
	}

	// Only SLIDESHOW_INTERVAL: tune a running slideshow, never stop one.
	adjust, err := convergencePolicy(&config.Config{
		Timezone: "UTC", SlideshowMode: config.SlideshowModeTV, SlideshowOverride: true, SlideshowIntervalSet: true,
		SlideshowInterval: 60,
		SlideshowType:     "shuffle",
	}, nil, now, slog.New(slog.DiscardHandler))
	if err != nil || adjust.Slideshow != (reconcile.SlideshowPolicy{
		Mode: reconcile.PolicyAdjust, Setting: samsung.SlideshowSetting{Interval: 60},
	}) {
		t.Fatalf("adjust slideshow policy = %+v, %v", adjust.Slideshow, err)
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

func TestConvergencePolicyCarriesRotationInterval(t *testing.T) {
	t.Parallel()

	policy, err := convergencePolicy(&config.Config{Timezone: "UTC", RotationIntervalDays: 14}, nil,
		time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC), slog.New(slog.DiscardHandler))
	if err != nil || policy.Rotation != 14*24*time.Hour {
		t.Fatalf("convergencePolicy() rotation = %s, error = %v; want two weeks", policy.Rotation, err)
	}
}
