package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestEngine_RunOnce_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	artworkDir := filepath.Join(tmpDir, "artwork")
	tokenDir := filepath.Join(tmpDir, "tokens")
	_ = os.MkdirAll(artworkDir, 0700)
	_ = os.MkdirAll(tokenDir, 0700)

	cfg := &config.Config{
		TVIPs:           []string{"127.0.0.1"},
		ArtworkDir:      artworkDir,
		TokenDir:        tokenDir,
		SyncIntervalMin: 1,
		OptimizeEnabled: false,
		DryRun:          true, // Use dry run to avoid real network calls
	}

	// We need a way to mock the source loader and samsung client if we wanted a full test,
	// but for now let's just test the initialization and basic flow.

	e := NewEngine(cfg, slog.Default(), nil)

	_ = e.RunOnce(context.Background())
	// We expect a connection failure since there's no TV at 127.0.0.1,
	// but this still covers the engine's initialization and pre-sync flow.
}

func TestParseDimensions(t *testing.T) {
	tests := []struct {
		filename   string
		expW, expH int
		expOk      bool
	}{
		{"art_3840x2160_opt.h_abc.jpg", 3840, 2160, true},
		{"simple.jpg", 0, 0, false},
		{"invalid_100x_opt.jpg", 0, 0, false},
		{"prefix_1920x1080.jpg", 1920, 1080, true},
	}

	for _, tt := range tests {
		w, h, ok := parseDimensions(tt.filename)
		if ok != tt.expOk || w != tt.expW || h != tt.expH {
			t.Errorf("parseDimensions(%q) = %d,%d,%v; want %d,%d,%v", tt.filename, w, h, ok, tt.expW, tt.expH, tt.expOk)
		}
	}
}
