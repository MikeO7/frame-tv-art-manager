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
		if err := os.MkdirAll(path, 0755); err != nil {
			logger.Error("Failed to create/access directory", "name", name, "path", path, "error", err)
			os.Exit(1)
		}
		// Test writability
		testFile := fmt.Sprintf("%s/.write_test", path)
		if err := os.WriteFile(testFile, []byte("ok"), 0644); err != nil {
			logger.Error("Directory is not writable", "name", name, "path", path, "error", err)
			os.Exit(1)
		}
		_ = os.Remove(testFile)
		logger.Debug("Directory validated", "name", name, "path", path)
	}

	healthStatus := health.NewStatus()

	engine := sync.NewEngine(cfg, logger, healthStatus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = engine.RunLoop(ctx)
	}()

	healthServer := health.NewServer(cfg.HealthPort, healthStatus, logger)
	go healthServer.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down...")
	cancel()
	_ = healthServer.Shutdown(context.Background())
}
