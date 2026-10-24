package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func testConfig(port int, uploadEnabled bool, artworkDir string) *config.Config {
	return &config.Config{
		HealthPort:        port,
		UploadEnabled:     uploadEnabled,
		ArtworkDir:        artworkDir,
		MaxDownloadSizeMB: 20,
	}
}

func newTestServer(t *testing.T, cfg *config.Config, status *Status) *Server {
	t.Helper()
	var importer collection.Store
	if cfg != nil && cfg.UploadEnabled {
		if cfg.ArtworkDir == "" {
			configured := *cfg
			configured.ArtworkDir = t.TempDir()
			cfg = &configured
		}
		var err error
		importer, err = collection.New(collection.Config{
			Root: cfg.ArtworkDir, MaxImportBytes: int64(cfg.MaxDownloadSizeMB) << 20,
		})
		if err != nil {
			t.Fatalf("construct test Artwork Collection: %v", err)
		}
	}
	return NewServer(cfg, status, silentLogger(), importer)
}

func decodeJSONResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	return payload
}

func TestBindRejectsUploadWithoutCollection(t *testing.T) {
	t.Parallel()
	server := NewServer(testConfig(1, true, t.TempDir()), NewStatus(), silentLogger(), nil)

	err := server.Bind(context.Background())
	if err == nil || !strings.Contains(err.Error(), "authoritative artwork collection") {
		t.Fatalf("Bind() error = %v, want missing collection error", err)
	}
}

func TestServerBoundLifecycle(t *testing.T) {
	t.Parallel()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatalf("release local port: %v", err)
	}

	cfg := testConfig(port, false, "")
	cfg.HealthBindAddress = "127.0.0.1"
	server := NewServer(cfg, NewStatus(), silentLogger(), nil)
	if err := server.Bind(context.Background()); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve() }()

	response, err := http.Get("http://" + server.listener.Addr().String() + "/live")
	if err != nil {
		t.Fatalf("GET /live: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close /live response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /live status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve() error = %v, want http.ErrServerClosed", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestServerShutdownAndCloseAreSafeBeforeBind(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, testConfig(0, false, ""), NewStatus())
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() before Bind error = %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() before Bind error = %v", err)
	}
	server.server = &http.Server{}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() on idle HTTP server error = %v", err)
	}
}

func TestHealthEndpointRequiresReadySuccessfulSync(t *testing.T) {
	t.Parallel()
	syncErr := errors.New("sync failed")
	tests := []struct {
		name          string
		lifecycle     string
		syncs         []error
		wantCode      int
		wantState     string
		wantLastError string
	}{
		{name: "ready after successful sync", lifecycle: "ready", syncs: []error{nil}, wantCode: http.StatusOK, wantState: statusOK},
		{name: "ready before first sync", lifecycle: "ready", wantCode: http.StatusServiceUnavailable, wantState: statusError},
		{name: "starting after successful sync", lifecycle: "starting", syncs: []error{nil}, wantCode: http.StatusServiceUnavailable, wantState: statusError},
		{
			name: "latest sync failed", lifecycle: "ready", syncs: []error{nil, syncErr},
			wantCode: http.StatusServiceUnavailable, wantState: statusError, wantLastError: syncErr.Error(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			status := NewStatus()
			status.SetLifecycle(test.lifecycle)
			for _, err := range test.syncs {
				status.RecordSync(err == nil, err)
			}
			response := httptest.NewRecorder()
			newTestServer(t, testConfig(0, false, ""), status).handleHealth(response, httptest.NewRequest(http.MethodGet, "/health", nil))

			if response.Code != test.wantCode {
				t.Fatalf("status code = %d, want %d", response.Code, test.wantCode)
			}
			payload := decodeJSONResponse(t, response)
			if payload[fieldStatus] != test.wantState || payload[fieldLastError] != test.wantLastError ||
				payload[fieldSyncCount] != float64(len(test.syncs)) || payload[fieldLifecycle] != test.lifecycle {
				t.Fatalf("health response = %#v", payload)
			}
		})
	}
}

func TestLiveEndpointReflectsProcessFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		lifecycle string
		wantCode  int
		wantState string
	}{
		{lifecycle: "starting", wantCode: http.StatusOK, wantState: statusOK},
		{lifecycle: "ready", wantCode: http.StatusOK, wantState: statusOK},
		{lifecycle: "failed", wantCode: http.StatusServiceUnavailable, wantState: statusError},
	}
	for _, test := range tests {
		t.Run(test.lifecycle, func(t *testing.T) {
			t.Parallel()
			status := NewStatus()
			status.SetLifecycle(test.lifecycle)
			response := httptest.NewRecorder()
			newTestServer(t, testConfig(0, false, ""), status).handleLive(response, httptest.NewRequest(http.MethodGet, "/live", nil))

			if response.Code != test.wantCode {
				t.Fatalf("status code = %d, want %d", response.Code, test.wantCode)
			}
			payload := decodeJSONResponse(t, response)
			if payload[fieldStatus] != test.wantState || payload[fieldLifecycle] != test.lifecycle {
				t.Fatalf("liveness response = %#v", payload)
			}
		})
	}
}

func TestHTTPStatusWireContract(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	lastSyncAt := startedAt.Add(30 * time.Second)
	status := NewStatus()
	status.StartedAt = startedAt
	status.RecordSync(false, errors.New("earlier failure"))
	status.RecordSync(true, nil)
	status.LastSyncAt = lastSyncAt
	status.SetStage("idle")
	status.SetLifecycle("ready")
	status.SetTVStatus("192.0.2.10", TVStatus{
		IP: "192.0.2.10", LastSeen: "2026-07-12T12:00:00Z", ImageCount: 3, ArtMode: true, Status: "ok",
	})
	status.SetTVStatus("192.0.2.10", TVStatus{IP: "192.0.2.10", ImageCount: 3, ArtMode: true, Status: "ok"})

	server := newTestServer(t, testConfig(0, false, ""), status)
	server.now = func() time.Time { return startedAt.Add(90 * time.Second) }
	response := httptest.NewRecorder()
	server.handleStatus(response, httptest.NewRequest(http.MethodGet, "/status", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	payload := decodeJSONResponse(t, response)
	wantKeys := map[string]struct{}{
		fieldStatus: {}, fieldUptime: {}, "started_at": {}, fieldLastSync: {},
		fieldLastSyncOK: {}, fieldLastError: {}, fieldCurrentStage: {},
		fieldSyncCount: {}, "tvs": {}, fieldLifecycle: {},
	}
	if len(payload) != len(wantKeys) {
		t.Fatalf("status keys = %v, want exactly %v", payload, wantKeys)
	}
	for key := range wantKeys {
		if _, ok := payload[key]; !ok {
			t.Errorf("status response missing key %q", key)
		}
	}
	if payload[fieldStatus] != statusOK || payload[fieldUptime] != "1m30s" ||
		payload["started_at"] != startedAt.Format(time.RFC3339) ||
		payload[fieldLastSync] != lastSyncAt.Format(time.RFC3339) ||
		payload[fieldLastSyncOK] != true || payload[fieldLastError] != "" ||
		payload[fieldCurrentStage] != "idle" || payload[fieldSyncCount] != float64(2) ||
		payload[fieldLifecycle] != "ready" {
		t.Fatalf("status wire values = %#v", payload)
	}

	tvs, ok := payload["tvs"].(map[string]any)
	if !ok {
		t.Fatalf("tvs = %#v, want object", payload["tvs"])
	}
	tv, ok := tvs["192.0.2.10"].(map[string]any)
	if !ok {
		t.Fatalf("TV entry = %#v, want object", tvs["192.0.2.10"])
	}
	if len(tv) != 5 {
		t.Fatalf("TV wire keys = %#v, want exactly five non-empty fields", tv)
	}
	if tv["image_count"] != float64(3) || tv["art_mode"] != true || tv["status"] != "ok" ||
		tv["last_seen"] != "2026-07-12T12:00:00Z" {
		t.Fatalf("TV wire state = %#v; an update without LastSeen must keep the last known time", tv)
	}
}
