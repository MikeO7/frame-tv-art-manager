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
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--help", "-h":
			fmt.Println("Frame TV Art Manager")
			fmt.Println("\nUsage:")
			fmt.Println("  frame-tv-art-manager [command] [flags]")
			fmt.Println("\nCommands:")
			fmt.Println("  sync       Run the sync loop (default)")
			fmt.Println("  diagnose   Run connection diagnostics")
			fmt.Println("\nFlags:")
			fmt.Println("  --dry-run     Run with dry-run (no changes made)")
			fmt.Println("  --help, -h    Show help")
			fmt.Println("  --version, -v Show version")
			os.Exit(0)
		case "--version", "-v":
			fmt.Printf("Frame TV Art Manager\nVersion: %s\nCommit: %s\nBuild Date: %s\n", Version, Commit, BuildDate)
			os.Exit(0)
		case "--dry-run":
			os.Setenv("DRY_RUN", "true")
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

	healthStatus := health.NewStatus()

	engine := sync.NewEngine(cfg, logger, healthStatus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = engine.RunLoop(ctx)
	}()

	healthServer := health.NewServer(8080, healthStatus, logger)
	go healthServer.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down...")
	cancel()
	_ = healthServer.Shutdown(context.Background())
}
