package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/app"
	"github.com/MikeO7/frame-tv-art-manager/internal/assistant"
	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
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

	museumSearchTimeout = 30 * time.Second
)

func main() {
	handleCLIArgs()
	if err := runMain(); err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		logger.Error("Frame TV Art Manager failed", "component", "application", "error", err)
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
		return fmt.Errorf("preflight application: %w", err)
	}
	application, err := buildApplication(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("build application: %w", err)
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
	var serverOptions []health.ServerOption
	if cfg.MCPEnabled {
		handler, err := assistant.NewHandler(assistant.Options{
			Config: cfg, Logger: logger, Artwork: engine, Themes: engine.Themes(), Status: healthStatus, Version: Version,
			Searcher: sources.NewArtSearcher(&http.Client{Timeout: museumSearchTimeout}, logger),
		})
		if err != nil {
			return nil, fmt.Errorf("construct MCP endpoint: %w", err)
		}
		serverOptions = append(serverOptions, health.WithMCP(handler))
		logger.Info("MCP endpoint enabled", "path", "/mcp")
	}
	healthServer := health.NewServer(cfg, healthStatus, logger, engine, serverOptions...)
	applicationOptions := app.Options{
		Prepare: func(ctx context.Context) error {
			_, err := engine.Prepare(ctx, collection.PrepareRequest{})
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
			fmt.Print("Usage: frame-tv-art-manager\n" +
				"Configuration is provided entirely via environment variables.\n" +
				"See README.md for details.\n")
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
	if err := level.UnmarshalText([]byte(logLevel)); err != nil {
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
	if uid, gid, change := directoryOwner(uid, gid); change {
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

// directoryOwner turns PUID and PGID into os.Chown arguments. Zero means
// unset, so an unset half becomes -1 and that half of the ownership is left
// as it is; change is false when neither is set.
func directoryOwner(puid, pgid int) (uid, gid int, change bool) {
	if puid == 0 && pgid == 0 {
		return -1, -1, false
	}
	uid, gid = -1, -1
	if puid != 0 {
		uid = puid
	}
	if pgid != 0 {
		gid = pgid
	}
	return uid, gid, true
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
	template := lineSourcesTemplate
	if extension := strings.ToLower(filepath.Ext(cfg.SourcesFile)); extension == ".yaml" || extension == ".yml" {
		template = yamlSourcesTemplate
	}
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

// yamlSourcesTemplate is the starter for a .yaml or .yml sources file, which
// the loader reads as a "providers:" map.
const yamlSourcesTemplate = "# ==========================================\n" +
	"# Frame TV Art Manager - Source List\n" +
	"# ==========================================\n" +
	"# Uncomment the lines below to enable them.\n\n" +
	"# providers:\n" +
	"  # --- 🚀 NASA (The Universe) ---\n" +
	"  # nasa:\n" +
	"  #   - apod             # Today's Picture of the Day (skipped on video days)\n" +
	"  #   - search:nebula     # The first 10 nebula results from the NASA image library\n\n" +
	"  # --- 🎨 Art Institute of Chicago (Fine Art) ---\n" +
	"  # art_institute_of_chicago:\n" +
	"  #   - search:monet      # The first 10 public-domain works matching \"monet\"\n" +
	"  #   - photo:12345       # A specific public-domain artwork by ID\n\n" +
	"  # --- 🏛️ The Met, Cleveland Museum of Art, SMK (Fine Art, no API key) ---\n" +
	"  # met:\n" +
	"  #   - search:sunflowers # The first 10 public-domain works matching \"sunflowers\"\n" +
	"  #   - search:?isHighlight=true&departmentId=11 # Highlights of European Paintings\n" +
	"  #   - object:436524     # A specific public-domain object by ID\n" +
	"  # cleveland:\n" +
	"  #   - search:snow?type=Painting # The first 10 CC0 paintings matching \"snow\"\n" +
	"  # smk:\n" +
	"  #   - search:hammershøi # The first 10 public-domain works from SMK\n\n" +
	"  # --- 🖼️ Every museum at once (no API key) ---\n" +
	"  # museum:\n" +
	"  #   - search:christmas  # Up to 20 public-domain works across all four museums\n\n" +
	"  # --- 🌍 Wikimedia Commons (credit and license shown in /status) ---\n" +
	"  # commons:\n" +
	"  #   - potd              # Today's Picture of the Day\n" +
	"  #   - featured          # The 10 newest Featured Pictures\n" +
	"  #   - search:aurora     # 10 Featured Pictures matching \"aurora\"\n\n" +
	"  # --- 📸 Unsplash (Photography) ---\n" +
	"  # unsplash:\n" +
	"  #   - collection:225444 # Up to 990 photos from a collection\n" +
	"  #   - photo:L9W_5q57_V8 # A specific photo\n\n" +
	"  # --- 🌿 Pexels (Nature & Architecture) ---\n" +
	"  # pexels:\n" +
	"  #   - search:nature     # The first 10 search results from Pexels\n" +
	"  #   - curated           # The latest 10 curated photos from Pexels\n" +
	"  #   - collection:abc123 # Up to 800 photos from a collection\n\n" +
	"  # --- 🍃 Pixabay (Free Art) ---\n" +
	"  # pixabay:\n" +
	"  #   - search:nature     # Up to 500 photos at least 3840 pixels wide\n" +
	"  #   - editors_choice    # Up to 500 Editor's Choice photos at least 3840 pixels wide\n" +
	"  #   - photo:12345       # A specific photo by ID\n\n" +
	"  # --- 🔗 Direct URLs (Any JPEG/PNG) ---\n" +
	"  # direct:\n" +
	"  #   - https://example.com/artwork.jpg\n\n" +
	"# Provider results are cached for 24 hours (APOD and commons:potd until the next day).\n" +
	"# 🔍 How to find IDs:\n" +
	"# - Unsplash Photo: unsplash.com/photos/abc123 -> abc123\n" +
	"# - Pexels Photo: pexels.com/photo/123 -> 123\n" +
	"# - Pixabay Photo: pixabay.com/photos/name-123 -> 123\n" +
	"# - Art Institute: artic.edu/artworks/12345/monet -> 12345\n" +
	"# - The Met: metmuseum.org/art/collection/search/436524 -> 436524\n" +
	"# - Cleveland: clevelandart.org/art/1997.111 -> 1997.111\n" +
	"# - SMK: open.smk.dk/artwork/image/KMS1 -> KMS1\n"

// lineSourcesTemplate is the starter for any other sources file, which the
// loader reads as one source per line. A comment there must have a line to
// itself, so every example sits alone on a line that starts with "#".
const lineSourcesTemplate = `# ==========================================
# Frame TV Art Manager - Source List
# ==========================================
# One source per line. Lines that start with # are ignored, and a comment
# cannot share a line with a source.
# To use an example, remove the # in front of it.

# --- NASA (The Universe) ---
# Today's Picture of the Day (skipped on video days):
#nasa:apod
# The first 10 nebula results from the NASA image library:
#nasa:search:nebula

# --- Art Institute of Chicago (Fine Art) ---
# The first 10 public-domain works matching "monet":
#art_institute_of_chicago:search:monet
# A specific public-domain artwork by ID:
#art_institute_of_chicago:photo:12345

# --- The Met, Cleveland Museum of Art, SMK (Fine Art, no API key) ---
# The first 10 public-domain works matching "sunflowers":
#met:search:sunflowers
# Highlights of European Paintings:
#met:search:?isHighlight=true&departmentId=11
# A specific public-domain object by ID:
#met:object:436524
# The first 10 CC0 paintings matching "snow":
#cleveland:search:snow?type=Painting
# The first 10 public-domain works from SMK:
#smk:search:hammershøi

# --- Every museum at once (no API key) ---
# Up to 20 public-domain works across all four museums:
#museum:search:christmas

# --- Wikimedia Commons (credit and license shown in /status) ---
# Today's Picture of the Day:
#commons:potd
# The 10 newest Featured Pictures:
#commons:featured
# 10 Featured Pictures matching "aurora":
#commons:search:aurora

# --- Unsplash (Photography) ---
# Up to 990 photos from a collection:
#unsplash:collection:225444
# A specific photo:
#unsplash:photo:L9W_5q57_V8

# --- Pexels (Nature & Architecture) ---
# The first 10 search results from Pexels:
#pexels:search:nature
# The latest 10 curated photos from Pexels:
#pexels:curated
# Up to 800 photos from a collection:
#pexels:collection:abc123

# --- Pixabay (Free Art) ---
# Up to 500 photos at least 3840 pixels wide:
#pixabay:search:nature
# Up to 500 Editor's Choice photos at least 3840 pixels wide:
#pixabay:editors_choice
# A specific photo by ID:
#pixabay:photo:12345

# --- Direct URLs (Any JPEG/PNG) ---
#https://example.com/artwork.jpg

# Provider results are cached for 24 hours (APOD and commons:potd until the next day).
# How to find IDs:
# - Unsplash Photo: unsplash.com/photos/abc123 -> abc123
# - Pexels Photo: pexels.com/photo/123 -> 123
# - Pixabay Photo: pixabay.com/photos/name-123 -> 123
# - Art Institute: artic.edu/artworks/12345/monet -> 12345
# - The Met: metmuseum.org/art/collection/search/436524 -> 436524
# - Cleveland: clevelandart.org/art/1997.111 -> 1997.111
# - SMK: open.smk.dk/artwork/image/KMS1 -> KMS1
`
