package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MikeO7/frame-tv-art-manager/internal/app"
	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/sync"
)

//nolint:gochecknoglobals // these variables are injected at build time via -ldflags
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

const (
	directoryArtwork = "artwork"
	directoryTokens  = "tokens"
)

func main() {
	handleCLIArgs()
	if err := runMain(); err != nil {
		fmt.Fprintf(os.Stderr, "Frame TV Art Manager failed: %v\n", err)
		os.Exit(1)
	}
}

func runMain() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runMainContext(ctx)
}

func runMainContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("application context is required")
	}
	cfg, warnings, err := config.LoadWithWarnings()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := setupLogger(cfg.LogLevel)
	logger.Info("Frame TV Art Manager starting", "version", Version, "commit", Commit, "build_date", BuildDate)
	for _, warning := range warnings {
		logger.Warn(warning.Message, "variable", warning.Variable, "fallback", warning.Fallback)
	}
	if err := preflight(ctx, cfg, logger); err != nil {
		return err
	}
	application, err := buildApplication(ctx, cfg, logger)
	if err != nil {
		return err
	}
	if err := application.Run(ctx); err != nil {
		return fmt.Errorf("run application: %w", err)
	}
	logger.Info("Shutdown complete")
	return nil
}

func preflight(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if cfg.DryRun {
		if err := validateDryRunDirectories(cfg); err != nil {
			return fmt.Errorf("dry-run preflight: %w", err)
		}
		return nil
	}
	if err := prepareDirectories(cfg); err != nil {
		return err
	}
	return bootstrapSources(ctx, cfg, logger)
}

func buildApplication(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
) (*app.Application, error) {
	healthStatus := health.NewStatus()
	engine, err := sync.NewManagedEngine(ctx, cfg, logger, healthStatus)
	if err != nil {
		return nil, fmt.Errorf("construct sync cycle: %w", err)
	}
	healthServer := health.NewServer(cfg, healthStatus, logger, engine)
	applicationOptions := app.Options{
		Prepare: func(ctx context.Context) error {
			_, err := engine.Prepare(ctx, collection.PrepareRequest{DryRun: cfg.DryRun})
			return err
		},
		RunCycle:        engine.RunLoop,
		SetState:        func(state app.State) { healthStatus.SetLifecycle(string(state)) },
		Closers:         []app.ResourceCloser{engine},
		ShutdownTimeout: cfg.ShutdownTimeout,
	}
	if cfg.HealthPort != 0 {
		applicationOptions.BindHTTP = func(ctx context.Context) (app.HTTPServer, error) {
			if err := healthServer.Bind(ctx); err != nil {
				return nil, err
			}
			return healthServer, nil
		}
	}
	application, err := app.New(applicationOptions)
	if err != nil {
		return nil, fmt.Errorf("construct application: %w", err)
	}
	return application, nil
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
			runHealthCheck("/health")
		case "-livenesscheck", "--livenesscheck":
			runHealthCheck("/live")
		}
	}
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

func prepareDirectories(cfg *config.Config) error {
	dirs := []struct {
		name string
		path string
		perm os.FileMode
	}{
		// Artwork stays traversable and readable over SMB/NFS; do not tighten it to 0700.
		{directoryArtwork, cfg.ArtworkDir, 0o755},
		{directoryTokens, cfg.TokenDir, 0o700},
	}
	for _, dir := range dirs {
		if err := prepareDirectory(dir.path, dir.perm, cfg.PUID, cfg.PGID); err != nil {
			return fmt.Errorf("prepare %s directory %s: %w", dir.name, dir.path, err)
		}
	}
	return nil
}

//nolint:gocyclo // ordered permission, ownership, and durability probes must fail at their exact boundary
func prepareDirectory(path string, mode os.FileMode, uid, gid int) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, mode); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path must be a non-symlink directory")
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set directory mode %04o: %w", mode, err)
	}
	if uid != 0 || gid != 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("set directory ownership: %w", err)
		}
	}
	probe, err := os.CreateTemp(path, ".write-test-*")
	if err != nil {
		return fmt.Errorf("create write probe: %w", err)
	}
	probePath := probe.Name()
	if err := probe.Chmod(0o600); err != nil {
		_ = probe.Close()
		_ = os.Remove(probePath)
		return fmt.Errorf("secure write probe: %w", err)
	}
	_, writeErr := probe.WriteString("ok")
	syncErr := probe.Sync()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if err := errors.Join(writeErr, syncErr, closeErr, removeErr); err != nil {
		return fmt.Errorf("verify directory writes: %w", err)
	}
	return nil
}

func validateDryRunDirectories(cfg *config.Config) error {
	for _, directory := range []struct {
		name     string
		path     string
		required os.FileMode
	}{
		{name: directoryArtwork, path: cfg.ArtworkDir},
		{name: directoryTokens, path: cfg.TokenDir, required: 0o700},
	} {
		info, err := os.Lstat(directory.path)
		if err != nil {
			return fmt.Errorf("inspect %s directory %s: %w", directory.name, directory.path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s path is not a non-symlink directory: %s", directory.name, directory.path)
		}
		if directory.required != 0 && info.Mode().Perm() != directory.required {
			return fmt.Errorf("%s directory mode is %04o, require %04o", directory.name, info.Mode().Perm(), directory.required)
		}
	}
	return nil
}

func bootstrapSources(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if cfg.SourcesFile == "" {
		return nil
	}
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

	if err := durablefs.CreateExclusive(ctx, cfg.SourcesFile, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, template)
		return err
	}); errors.Is(err, os.ErrExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("persist sources template: %w", err)
	}
	logger.Info("created example sources file (all commented out)", "path", cfg.SourcesFile)
	return nil
}
