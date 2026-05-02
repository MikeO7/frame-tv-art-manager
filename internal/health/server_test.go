package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthEndpoint(t *testing.T) {
	status := NewStatus()
	status.RecordSync(true, nil)
	status.SetTVStatus("192.168.1.1", TVStatus{
		IP:         "192.168.1.1",
		LastSeen:   time.Now().Format(time.RFC3339),
		ImageCount: 42,
		ArtMode:    true,
		Status:     "ok",
	})

	srv := NewServer(0, status, silentLogger())
	// Use httptest directly instead of starting a real listener.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}
	if resp["last_sync_ok"] != true {
		t.Errorf("expected last_sync_ok=true, got %v", resp["last_sync_ok"])
	}
	if resp["sync_count"].(float64) != 1 {
		t.Errorf("expected sync_count=1, got %v", resp["sync_count"])
	}
}

func TestServer_Routes(t *testing.T) {
	status := NewStatus()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(0, status, logger) // Port 0 doesn't actually start, but we can call handlers.

	// Test handleHealth
	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	server.handleHealth(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", rr.Code, http.StatusOK)
	}

	// Test handleStatus
	req = httptest.NewRequest("GET", "/status", nil)
	rr = httptest.NewRecorder()
	server.handleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", rr.Code, http.StatusOK)
	}
}

func TestServer_Shutdown(t *testing.T) {
	status := NewStatus()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(12345, status, logger)

	// Start server in background
	server.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}
}

func TestStatusEndpoint(t *testing.T) {
	status := NewStatus()
	status.RecordSync(false, nil)

	srv := NewServer(0, status, silentLogger())
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["last_sync_ok"] != false {
		t.Errorf("expected last_sync_ok=false, got %v", resp["last_sync_ok"])
	}
}

func TestShutdown_NilServer(t *testing.T) {
	// Shutdown on a server that was never started should not panic.
	srv := NewServer(0, NewStatus(), silentLogger())
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
