package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestSetupLoggerHonorsConfiguredLevel(t *testing.T) {
	tests := []struct {
		level string
		want  slog.Level
	}{
		{level: "debug", want: slog.LevelDebug},
		{level: "info", want: slog.LevelInfo},
		{level: "warn", want: slog.LevelWarn},
		{level: "error", want: slog.LevelError},
		{level: "unknown", want: slog.LevelInfo},
	}
	for _, test := range tests {
		t.Run(test.level, func(t *testing.T) {
			logger := setupLogger(test.level)
			if !logger.Enabled(context.Background(), test.want) {
				t.Fatalf("setupLogger(%q) disabled level %v", test.level, test.want)
			}
			if logger.Enabled(context.Background(), test.want-1) {
				t.Fatalf("setupLogger(%q) enabled level below %v", test.level, test.want)
			}
		})
	}
}

func TestPrepareDirectoriesCreatesSecureWritableDirectories(t *testing.T) {
	tests := []struct {
		name      string
		ownership bool
	}{
		{name: "default ownership"},
		{name: "explicit ownership", ownership: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := &config.Config{
				ArtworkDir: filepath.Join(root, "artwork"),
				TokenDir:   filepath.Join(root, "tokens"),
			}
			if test.ownership {
				cfg.PUID, cfg.PGID = os.Getuid(), os.Getgid()
			}
			if err := prepareDirectories(cfg); err != nil {
				t.Fatalf("prepareDirectories() error = %v", err)
			}
			for path, want := range map[string]os.FileMode{cfg.ArtworkDir: 0o755, cfg.TokenDir: 0o700} {
				assertEmptyDirectory(t, path, want)
			}
		})
	}
}

func TestPrepareDirectoryRejectsUnsafePaths(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string) string
	}{
		{
			name: "regular file",
			prepare: func(t *testing.T, root string) string {
				path := filepath.Join(root, "occupied")
				if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, root string) string {
				target := filepath.Join(root, "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.prepare(t, t.TempDir())
			if err := prepareDirectory(path, 0o700, 0, 0); err == nil {
				t.Fatalf("prepareDirectory(%s) accepted an unsafe path", test.name)
			}
		})
	}
}

func TestBootstrapSources(t *testing.T) {
	t.Run("creates template when missing", func(t *testing.T) {
		sourcesFile := filepath.Join(t.TempDir(), "new-sources.txt")
		if err := bootstrapSources(context.Background(), &config.Config{SourcesFile: sourcesFile}, slog.Default()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(sourcesFile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Frame TV Art Manager") {
			t.Error("expected template content in new sources file")
		}
		info, err := os.Stat(sourcesFile)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("sources template mode = %v, %v; want 0600", info, err)
		}
	})

	t.Run("preserves existing file", func(t *testing.T) {
		sourcesFile := filepath.Join(t.TempDir(), "existing.txt")
		if err := os.WriteFile(sourcesFile, []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := bootstrapSources(context.Background(), &config.Config{SourcesFile: sourcesFile}, slog.Default()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(sourcesFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "existing" {
			t.Error("bootstrapSources overwrote an existing file")
		}
	})

	t.Run("no-op when path empty", func(t *testing.T) {
		if err := bootstrapSources(context.Background(), &config.Config{}, slog.Default()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reports write failure", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parentFile, []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{SourcesFile: filepath.Join(parentFile, "sources.yaml")}
		if err := bootstrapSources(context.Background(), cfg, slog.Default()); err == nil {
			t.Fatal("bootstrapSources accepted an invalid parent path")
		}
	})
}

func TestValidateDryRunDirectoriesIsReadOnly(t *testing.T) {
	root := t.TempDir()
	artworkDir := filepath.Join(root, "artwork")
	tokenDir := filepath.Join(root, "tokens")
	if err := os.Mkdir(artworkDir, 0o755); err != nil {
		t.Fatalf("create artwork directory: %v", err)
	}
	if err := os.Mkdir(tokenDir, 0o700); err != nil {
		t.Fatalf("create token directory: %v", err)
	}
	cfg := &config.Config{ArtworkDir: artworkDir, TokenDir: tokenDir}
	if err := validateDryRunDirectories(cfg); err != nil {
		t.Fatalf("validateDryRunDirectories() error = %v", err)
	}
	assertEmptyDirectory(t, artworkDir, 0o755)
	assertEmptyDirectory(t, tokenDir, 0o700)
	if err := os.Chmod(tokenDir, 0o755); err != nil {
		t.Fatalf("change token mode: %v", err)
	}
	if err := validateDryRunDirectories(cfg); err == nil {
		t.Fatal("validateDryRunDirectories() accepted insecure token directory")
	}
}

func TestPreflightOnlyMutatesOutsideDryRun(t *testing.T) {
	tests := []struct {
		name        string
		dryRun      bool
		wantSources bool
	}{
		{name: "dry run validates existing directories", dryRun: true},
		{name: "writable run prepares directories and sources", wantSources: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := &config.Config{
				ArtworkDir:  filepath.Join(root, "artwork"),
				TokenDir:    filepath.Join(root, "tokens"),
				SourcesFile: filepath.Join(root, "sources.yaml"),
				DryRun:      test.dryRun,
			}
			if test.dryRun {
				if err := os.Mkdir(cfg.ArtworkDir, 0o755); err != nil {
					t.Fatalf("create artwork directory: %v", err)
				}
				if err := os.Mkdir(cfg.TokenDir, 0o700); err != nil {
					t.Fatalf("create token directory: %v", err)
				}
			}

			if err := preflight(context.Background(), cfg, slog.New(slog.DiscardHandler)); err != nil {
				t.Fatalf("preflight() error = %v", err)
			}
			assertEmptyDirectory(t, cfg.ArtworkDir, 0o755)
			assertEmptyDirectory(t, cfg.TokenDir, 0o700)
			_, err := os.Stat(cfg.SourcesFile)
			if gotSources := err == nil; gotSources != test.wantSources {
				t.Fatalf("sources file exists = %t, want %t (stat error %v)", gotSources, test.wantSources, err)
			}
		})
	}
}

func TestBuildApplicationValidatesCollectionAndWiresHTTP(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if _, err := buildApplication(context.Background(), &config.Config{}, logger); err == nil {
		t.Fatal("buildApplication() accepted an empty collection root")
	}

	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10"}, ArtworkDir: t.TempDir(), TokenDir: t.TempDir(),
		ClientName: "Frame Manager Test", SyncIntervalMin: 5,
		ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
		UploadAttempts: 1, HealthPort: 8080, HealthBindAddress: "127.0.0.1", ShutdownTimeout: time.Second,
	}
	application, err := buildApplication(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("buildApplication() error = %v", err)
	}
	if application == nil {
		t.Fatal("buildApplication() returned a nil application")
	}
}

func TestRunMainContextStopsCleanlyWhenCanceled(t *testing.T) {
	dataDir := t.TempDir()
	artworkDir := filepath.Join(dataDir, "artwork")
	tokenDir := filepath.Join(dataDir, "tokens")
	if err := os.Mkdir(artworkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TV_IPS", "127.0.0.1")
	t.Setenv("ARTWORK_DIR", artworkDir)
	t.Setenv("TOKEN_DIR", tokenDir)
	t.Setenv("HEALTH_PORT", "0")
	t.Setenv("DRY_RUN", "true")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runMainContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runMainContext() error = %v", err)
	}
	if err := runMainContext(nil); err == nil { //nolint:staticcheck // nil context must be rejected explicitly
		t.Fatal("runMainContext(nil) error = nil")
	}
}

func TestInformationalFlagsPrintAndExitSuccessfully(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Args = []string{"frame-tv-art-manager", os.Getenv("CLI_ARG")}
		handleCLIArgs()
		return
	}

	tests := map[string]string{
		"--help": "Usage: frame-tv-art-manager", "-h": "Usage: frame-tv-art-manager",
		"--version": "frame-tv-art-manager version", "-v": "frame-tv-art-manager version",
	}
	for arg, want := range tests {
		t.Run(arg, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestInformationalFlagsPrintAndExitSuccessfully$")
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "CLI_ARG="+arg)
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("handleCLIArgs(%q) exited with %v", arg, err)
			}
			if !strings.Contains(string(output), want) {
				t.Fatalf("handleCLIArgs(%q) output = %q, want %q", arg, output, want)
			}
		})
	}
}

func assertEmptyDirectory(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", path, err)
	}
	if !info.IsDir() || info.Mode().Perm() != want {
		t.Fatalf("%s mode = %v, want directory %04o", path, info.Mode(), want)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s retained entries %v", path, entries)
	}
}
