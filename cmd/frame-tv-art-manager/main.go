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
				"providers:\n" +
				"  # --- 🚀 NASA ---\n" +
				"  # nasa:\n" +
				"  #   - apod\n" +
				"  #   - search:nebula\n\n" +
				"  # --- 🎨 Art Institute of Chicago ---\n" +
				"  # art_institute_of_chicago:\n" +
				"  #   - search:impressionism\n" +
				"  #   - search:monet\n\n" +
				"  # --- 📸 Unsplash ---\n" +
				"  # unsplash:\n" +
				"  #   - collection:225444\n"
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
