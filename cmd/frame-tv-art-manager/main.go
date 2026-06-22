package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/sync"
)

//nolint:gochecknoglobals // these variables are injected at build time via -ldflags
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func main() {
	handleCLIArgs()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.LogLevel)
	logger.Info("Frame TV Art Manager starting", "version", Version, "commit", Commit, "build_date", BuildDate)

	validateDirectories(cfg, logger)
	bootstrapSources(cfg, logger)

	healthStatus := health.NewStatus()

	engine := sync.NewEngine(cfg, logger, healthStatus)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = engine.RunLoop(ctx)
	}()

	healthServer := health.NewServer(cfg, healthStatus, logger)
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

func handleCLIArgs() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h":
			//nolint:forbidigo // standard CLI usage output
			fmt.Println("Usage: frame-tv-art-manager")
			//nolint:forbidigo // standard CLI usage output
			fmt.Println("Configuration is provided entirely via environment variables.")
			//nolint:forbidigo // standard CLI usage output
			fmt.Println("See README.md for details.")
			os.Exit(0)
		case "--version", "-v":
			//nolint:forbidigo // standard CLI version output
			fmt.Printf("frame-tv-art-manager version %s (commit %s) built on %s\n", Version, Commit, BuildDate)
			os.Exit(0)
		case "-healthcheck", "--healthcheck":
			runHealthCheck()
		}
	}
}

// Health check tuning for the container HEALTHCHECK probe.
const (
	defaultHealthPort     = 8080
	healthCheckTimeout    = 5 * time.Second
	healthCheckMaxBytes   = 1 << 20 // 1 MiB cap to bound the probe response
	healthStatusHealthyOK = "ok"
)

// runHealthCheck performs the container health probe and exits with status 0
// when healthy or 1 otherwise. All work is delegated to performHealthCheck so
// resource cleanup runs via defer (os.Exit, used here, bypasses defers).
func runHealthCheck() {
	if err := performHealthCheck(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	//nolint:forbidigo // CLI success output
	fmt.Println("Healthy")
	os.Exit(0)
}

func performHealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", healthCheckPort())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create health check request: %w", err)
	}

	client := &http.Client{Timeout: healthCheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: HTTP status %d", resp.StatusCode)
	}

	var status struct {
		Status string `json:"status"`
	}
	// Enforce a hard limit on memory allocation to prevent DoS via excessively large payloads.
	reader := http.MaxBytesReader(nil, resp.Body, healthCheckMaxBytes)
	if err := json.NewDecoder(reader).Decode(&status); err != nil {
		return fmt.Errorf("decode health check response: %w", err)
	}

	if status.Status != healthStatusHealthyOK {
		return fmt.Errorf("health check returned status: %q", status.Status)
	}
	return nil
}

// healthCheckPort resolves the probe port from HEALTH_PORT, falling back to the default.
func healthCheckPort() int {
	if portStr := os.Getenv("HEALTH_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			return p
		}
	}
	return defaultHealthPort
}

func setupLogger(logLevel string) *slog.Logger {
	var level slog.Level
	switch logLevel {
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

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

func validateDirectories(cfg *config.Config, logger *slog.Logger) {
	dirs := []struct {
		name string
		path string
		perm os.FileMode
	}{
		// 0o755 is intentional for artwork directory so that network shares
		// like SMB/NFS can traverse and read the images. Do NOT tighten to 0o700.
		{"artwork", cfg.ArtworkDir, 0o755},
		{"tokens", cfg.TokenDir, 0o700},
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir.path, dir.perm); err != nil {
			logger.Error("Failed to create/access directory", "name", dir.name, "path", dir.path, "error", err)
			os.Exit(1)
		}
		if err := os.Chmod(dir.path, dir.perm); err != nil {
			logger.Warn("Failed to set directory permissions", "path", dir.path, "perm", dir.perm, "error", err)
		}
		if cfg.PUID != 0 || cfg.PGID != 0 {
			if err := os.Chown(dir.path, cfg.PUID, cfg.PGID); err != nil {
				logger.Warn("Failed to set directory ownership", "path", dir.path, "puid", cfg.PUID, "pgid", cfg.PGID, "error", err)
			}
		}
		testFile := fmt.Sprintf("%s/.write_test", dir.path)
		if err := os.WriteFile(testFile, []byte("ok"), 0o600); err != nil {
			logger.Error("Directory is not writable", "name", dir.name, "path", dir.path, "error", err)
			os.Exit(1)
		}
		_ = os.Remove(testFile)
	}
}

func bootstrapSources(cfg *config.Config, logger *slog.Logger) {
	if cfg.SourcesFile == "" {
		return
	}
	if _, err := os.Stat(cfg.SourcesFile); !os.IsNotExist(err) {
		return
	}

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
		"  # --- 🍃 Pixabay (Free Art) ---\n" +
		"  # pixabay:\n" +
		"  #   - search:nature     # 10 high-res photos from Pixabay\n" +
		"  #   - editors_choice    # 10 hand-picked photos from Pixabay\n" +
		"  #   - user:12345        # Up to 50 photos from a specific artist\n\n" +
		"  # --- 🔗 Direct URLs (Any JPEG/PNG) ---\n" +
		"  # direct:\n" +
		"  #   - https://example.com/artwork.jpg\n\n" +
		"# 🔍 How to find IDs:\n" +
		"# - Unsplash Photo: unsplash.com/photos/abc123 -> abc123\n" +
		"# - Pexels Photo: pexels.com/photo/123 -> 123\n" +
		"# - Pixabay Photo/User: pixabay.com/.../name-123 -> 123\n" +
		"# - Art Institute: artic.edu/artworks/12345/monet -> 12345\n"

	if err := os.WriteFile(cfg.SourcesFile, []byte(template), 0o600); err != nil {
		logger.Warn("Failed to bootstrap sources file", "error", err)
	}
}
