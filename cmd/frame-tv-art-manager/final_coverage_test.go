package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestPreflightSelectsReadOnlyAndWritablePaths(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
	}{
		{name: "dry run", dryRun: true},
		{name: "writable", dryRun: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := &config.Config{
				ArtworkDir: filepath.Join(root, "artwork"),
				TokenDir:   filepath.Join(root, "tokens"),
				DryRun:     test.dryRun,
			}
			if test.dryRun {
				if err := os.Mkdir(cfg.ArtworkDir, 0o755); err != nil {
					t.Fatalf("create artwork directory: %v", err)
				}
				if err := os.Mkdir(cfg.TokenDir, 0o700); err != nil {
					t.Fatalf("create token directory: %v", err)
				}
			}

			if err := preflight(context.Background(), cfg, slog.New(slog.DiscardHandler)); err != nil {
				t.Fatalf("preflight() error = %v", err)
			}
			for _, path := range []string{cfg.ArtworkDir, cfg.TokenDir} {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("preflight directory %s: info=%v error=%v", path, info, err)
				}
			}
		})
	}
}

func TestBuildApplicationValidatesCollectionAndWiresHTTP(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if _, err := buildApplication(context.Background(), &config.Config{}, logger); err == nil {
		t.Fatal("buildApplication() accepted an empty collection root")
	}

	root := t.TempDir()
	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10"}, ArtworkDir: root, TokenDir: t.TempDir(),
		ClientName: "Frame Manager Test", SyncIntervalMin: 5,
		ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
		UploadAttempts: 1, HealthPort: 8080, HealthBindAddress: "127.0.0.1", ShutdownTimeout: time.Second,
	}
	application, err := buildApplication(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("buildApplication() error = %v", err)
	}
	if application == nil {
		t.Fatal("buildApplication() returned a nil application")
	}
}
