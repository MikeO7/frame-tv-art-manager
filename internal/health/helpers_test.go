package health

import (
	"log/slog"
	"os"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// silentLogger returns a logger that only emits errors, for use in tests.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testConfig returns a minimal config struct populated for testing.
func testConfig(port int, uploadEnabled bool, artworkDir string) *config.Config {
	return &config.Config{
		HealthPort:        port,
		UploadEnabled:     uploadEnabled,
		ArtworkDir:        artworkDir,
		MaxDownloadSizeMB: 20,
	}
}
