package health

import (
	"log/slog"
	"os"
)

// silentLogger returns a logger that only emits errors, for use in tests.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
