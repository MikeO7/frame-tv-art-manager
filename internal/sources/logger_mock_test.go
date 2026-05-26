package sources

import (
	"bytes"

	"log/slog"
)

// newTestLogger returns a logger and a buffer that captures log output
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	return slog.New(handler), &buf
}
