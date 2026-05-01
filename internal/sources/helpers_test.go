package sources

import (
	"log/slog"
	"os"
)

// testLogger returns a silent logger for use in tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
