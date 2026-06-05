package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
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
	logger := silentLogger()
	server := NewServer(0, status, logger) // Port 0 doesn't actually start, but we can call handlers.

	// Test handleHealth
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	server.handleHealth(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", rr.Code, http.StatusOK)
	}

	// Test handleStatus
	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	rr = httptest.NewRecorder()
	server.handleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", rr.Code, http.StatusOK)
	}
}

func TestServer_Shutdown(t *testing.T) {
	status := NewStatus()
	logger := silentLogger()
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

func TestStatus_SetStage(t *testing.T) {
	status := NewStatus()
	status.SetStage("syncing TVs")
	status.mu.RLock()
	stage := status.CurrentStage
	status.mu.RUnlock()
	if stage != "syncing TVs" {
		t.Errorf("CurrentStage = %q", stage)
	}
}

func TestServer_Start_DisabledPort(t *testing.T) {
	status := NewStatus()
	server := NewServer(0, status, silentLogger())
	server.Start()
	if server.server != nil {
		t.Error("expected no http.Server when port is 0")
	}
}

func TestShutdown_NilServer(t *testing.T) {
	// Shutdown on a server that was never started should not panic.
	srv := NewServer(0, NewStatus(), silentLogger())
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRecordSync_WithError(t *testing.T) {
	status := NewStatus()

	testErr := errors.New("sync failed")
	status.RecordSync(false, testErr)

	status.mu.RLock()
	errMsg := status.LastErrorMessage
	status.mu.RUnlock()

	if errMsg != testErr.Error() {
		t.Errorf("expected LastErrorMessage %q, got %q", testErr.Error(), errMsg)
	}
}

// safeBuffer is a thread-safe wrapper around bytes.Buffer to prevent data races during testing.
type safeBuffer struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Bytes()
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServer_StartAndServe(t *testing.T) {
	status := NewStatus()

	// Use a thread-safe buffer for slog to prevent data races between the logger goroutine and the test poller.
	var buf safeBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// Port -1 is invalid and will cause ListenAndServe to fail immediately.
	srv := NewServer(-1, status, logger)
	srv.Start()

	// Wait for the goroutine to log the error, with a timeout
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for server error log")
		case <-ticker.C:
			if bytes.Contains(buf.Bytes(), []byte("health server error")) {
				// We found the error log, the goroutine executed the failure path successfully.
				// Assert that the specific error we expect is present.
				if !bytes.Contains(buf.Bytes(), []byte("invalid port")) {
					t.Errorf("expected 'invalid port' in log output, got: %s", buf.String())
				}
				return
			}
		}
	}
}
