package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create test listener: %v", err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.Listener = ln
	ts.Start()
	t.Cleanup(func() { ts.Close() })
	return ts
}

func TestSetupLogger(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unknown"} {
		logger := setupLogger(level)
		if logger == nil {
			t.Fatalf("setupLogger(%q) returned nil", level)
		}
	}
}

func TestValidateDirectories(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ArtworkDir: filepath.Join(tmp, "artwork"),
		TokenDir:   filepath.Join(tmp, "tokens"),
	}
	if err := prepareDirectories(cfg); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"artwork", "tokens"} {
		path := filepath.Join(tmp, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", name)
		}
	}
}

func TestBootstrapSources(t *testing.T) {
	tmp := t.TempDir()

	t.Run("creates template when missing", func(t *testing.T) {
		sourcesFile := filepath.Join(tmp, "new-sources.txt")
		cfg := &config.Config{SourcesFile: sourcesFile}
		if err := bootstrapSources(context.Background(), cfg, slog.Default()); err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(sourcesFile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Frame TV Art Manager") {
			t.Error("expected template content in new sources file")
		}
	})

	t.Run("skips when file exists", func(t *testing.T) {
		sourcesFile := filepath.Join(tmp, "existing.txt")
		if err := os.WriteFile(sourcesFile, []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{SourcesFile: sourcesFile}
		if err := bootstrapSources(context.Background(), cfg, slog.Default()); err != nil {
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
}

func TestValidateDirectories_WithOwnership(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ArtworkDir: filepath.Join(tmp, "artwork"),
		TokenDir:   filepath.Join(tmp, "tokens"),
		PUID:       os.Getuid(),
		PGID:       os.Getgid(),
	}
	if err := prepareDirectories(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDirectoryRejectsNonDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareDirectory(path, 0o700, 0, 0); err == nil {
		t.Fatal("prepareDirectory accepted a regular file")
	}
}

func TestPrepareDirectoryRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareDirectory(link, 0o700, 0, 0); err == nil {
		t.Fatal("prepareDirectory accepted a symlink")
	}
}

func TestHandleCLIArgs(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Args = []string{"frame-tv-art-manager", os.Getenv("CLI_ARG")}
		handleCLIArgs()
		return
	}

	for _, arg := range []string{"--help", "-h", "--version", "-v"} {
		t.Run(arg, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestHandleCLIArgs")
			cmd.Env = append(os.Environ(),
				"GO_WANT_HELPER_PROCESS=1",
				"CLI_ARG="+arg,
			)
			err := cmd.Run()
			if err != nil {
				t.Fatalf("handleCLIArgs(%q) exited with %v", arg, err)
			}
		})
	}
}

func TestHandleCLIArgs_Healthcheck(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Args = []string{"frame-tv-art-manager", os.Getenv("CLI_ARG")}
		handleCLIArgs()
		return
	}

	// Spin up a mock HTTP server.
	ts := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Extract the port from the test server URL.
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}

	// Run helper process targeting this mock server.
	cmd := exec.Command(os.Args[0], "-test.run=TestHandleCLIArgs_Healthcheck")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"CLI_ARG=-healthcheck",
		"HEALTH_PORT="+portStr,
	)
	err = cmd.Run()
	if err != nil {
		t.Fatalf("-healthcheck failed: %v", err)
	}
}

func TestPerformHealthCheckResponses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{name: "healthy", statusCode: http.StatusOK, body: `{"status":"ok"}`},
		{name: "http failure", statusCode: http.StatusServiceUnavailable, body: `{}`, wantError: "HTTP status 503"},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`, wantError: "decode health check response"},
		{name: "degraded status", statusCode: http.StatusOK, body: `{"status":"error"}`, wantError: `status: "error"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(u.Host)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HEALTH_PORT", port)
			err = performHealthCheck()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("performHealthCheck: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("performHealthCheck() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestPerformHealthCheckConnectionFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEALTH_PORT", strconv.Itoa(port))
	if err := performHealthCheck(); err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("performHealthCheck() error = %v", err)
	}
}

func TestHealthCheckPort(t *testing.T) {
	t.Setenv("HEALTH_PORT", "")
	if got := healthCheckPort(); got != defaultHealthPort {
		t.Fatalf("healthCheckPort() = %d", got)
	}
	t.Setenv("HEALTH_PORT", "not-a-number")
	if got := healthCheckPort(); got != defaultHealthPort {
		t.Fatalf("healthCheckPort() malformed = %d", got)
	}
	t.Setenv("HEALTH_PORT", "4321")
	if got := healthCheckPort(); got != 4321 {
		t.Fatalf("healthCheckPort() = %d, want 4321", got)
	}
}

func TestBootstrapSourcesWriteFailure(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapSources(
		context.Background(),
		&config.Config{SourcesFile: filepath.Join(parentFile, "sources.yaml")},
		slog.Default(),
	); err == nil {
		t.Fatal("bootstrapSources accepted an invalid parent path")
	}
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
	for _, path := range []string{artworkDir, tokenDir} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(entries) != 0 {
			t.Fatalf("dry-run preflight mutated %s: %v", path, entries)
		}
	}
	if err := os.Chmod(tokenDir, 0o755); err != nil {
		t.Fatalf("change token mode: %v", err)
	}
	if err := validateDryRunDirectories(cfg); err == nil {
		t.Fatal("validateDryRunDirectories() accepted insecure token directory")
	}
}

func TestMainLifecycle(t *testing.T) {
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
}
