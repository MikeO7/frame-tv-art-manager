package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/sync"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h":
			fmt.Println("Usage: frame-tv-art-manager")
			fmt.Println("Configuration is provided entirely via environment variables.")
			fmt.Println("See README.md for details.")
			os.Exit(0)
		case "--version", "-v":
			fmt.Printf("frame-tv-art-manager version %s (commit %s) built on %s\n", Version, Commit, BuildDate)
			os.Exit(0)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))

	logger.Info("Frame TV Art Manager starting", "version", Version, "commit", Commit, "build_date", BuildDate)

	// Robustness check: Ensure required directories exist and are writable.
	dirs := map[string]string{
		"artwork": cfg.ArtworkDir,
		"tokens":  cfg.TokenDir,
	}
	for name, path := range dirs {
		if err := os.MkdirAll(path, 0755); err != nil { //nolint:gosec // Required for shared volumes
			logger.Error("Failed to create/access directory", "name", name, "path", path, "error", err)
			os.Exit(1)
		}
		// If PUID/PGID are set, try to change ownership so the host folders are correct.
		if cfg.PUID != 0 || cfg.PGID != 0 {
			if err := os.Chown(path, cfg.PUID, cfg.PGID); err != nil {
				logger.Warn("Failed to set directory ownership (continuing anyway)", "path", path, "puid", cfg.PUID, "pgid", cfg.PGID, "error", err)
			}
		}
		// Test writability
		testFile := fmt.Sprintf("%s/.write_test", path)
		if err := os.WriteFile(testFile, []byte("ok"), 0644); err != nil { //nolint:gosec // Test file, broad permissions intentional
			logger.Error("Directory is not writable", "name", name, "path", path, "error", err)
			os.Exit(1)
		}
		_ = os.Remove(testFile)
		logger.Debug("Directory validated", "name", name, "path", path)
	}

	// Bootstrap sources file if missing
	if cfg.SourcesFile != "" {
		if _, err := os.Stat(cfg.SourcesFile); os.IsNotExist(err) {
			logger.Info("Creating example sources file (all commented out)", "path", cfg.SourcesFile)
			template := "# ==========================================\n" +
				"# Frame TV Art Manager - Source List\n" +
				"# ==========================================\n" +
				"# Uncomment the lines below to enable them.\n\n" +
				"# providers:\n" +
				"  # --- 🚀 NASA (The Universe) ---\n" +
				"  # nasa:\n" +
				"  #   - apod             # Today's Picture of the Day\n" +
				"  #   - search:nebula     # Top 10 high-res nebula photos\n\n" +
				"  # --- 🎨 Art Institute of Chicago (Fine Art) ---\n" +
				"  # art_institute_of_chicago:\n" +
				"  #   - search:monet      # 10 masterpieces by Claude Monet\n" +
				"  #   - photo:12345       # A specific artwork by ID\n\n" +
				"  # --- 📸 Unsplash (Photography) ---\n" +
				"  # unsplash:\n" +
				"  #   - collection:225444 # Up to 50 photos from a collection\n" +
				"  #   - photo:L9W_5q57_V8 # A specific high-res photo\n\n" +
				"  # --- 🌿 Pexels (Nature & Architecture) ---\n" +
				"  # pexels:\n" +
				"  #   - search:nature     # 10 high-res photos from Pexels\n" +
				"  #   - curated           # 10 hand-picked photos from Pexels\n\n" +
				"  # --- 🔗 Direct URLs (Any JPEG/PNG) ---\n" +
				"  # direct:\n" +
				"  #   - https://example.com/artwork.jpg\n\n" +
				"# 🔍 How to find IDs:\n" +
				"# - Unsplash Photo: unsplash.com/photos/abc123 -> abc123\n" +
				"# - Pexels Photo: pexels.com/photo/123 -> 123\n" +
				"# - Art Institute: artic.edu/artworks/12345/monet -> 12345\n"
			if err := os.WriteFile(cfg.SourcesFile, []byte(template), 0644); err != nil {
				logger.Warn("Failed to bootstrap sources file", "error", err)
			}
		}
	}

	healthStatus := health.NewStatus()

	engine := sync.NewEngine(cfg, logger, healthStatus)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = engine.RunLoop(ctx)
	}()

	healthServer := health.NewServer(cfg.HealthPort, healthStatus, logger)
	go healthServer.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down gracefully...")
	cancel()
	
	// Wait for engine to finish current cycle
	<-done
	_ = healthServer.Shutdown(context.Background())
	logger.Info("Shutdown complete")
}
