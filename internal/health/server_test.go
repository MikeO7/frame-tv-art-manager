package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	srv := NewServer(testConfig(0, false, ""), status, silentLogger())
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
	server := NewServer(testConfig(0, false, ""), status, logger) // Port 0 doesn't actually start, but we can call handlers.

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
	server := NewServer(testConfig(12345, false, ""), status, logger)

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

	srv := NewServer(testConfig(0, false, ""), status, silentLogger())
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
	server := NewServer(testConfig(0, false, ""), status, silentLogger())
	server.Start()
	if server.server != nil {
		t.Error("expected no http.Server when port is 0")
	}
}

func TestShutdown_NilServer(t *testing.T) {
	// Shutdown on a server that was never started should not panic.
	srv := NewServer(testConfig(0, false, ""), NewStatus(), silentLogger())
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
	srv := NewServer(testConfig(-1, false, ""), status, logger)
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

func createMultipartRequest(filename string, content []byte) (*http.Request, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	_, err = part.Write(content)
	if err != nil {
		return nil, err
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func TestUpload_Disabled(t *testing.T) {
	status := NewStatus()
	srv := NewServer(testConfig(0, false, ""), status, silentLogger())

	req, err := createMultipartRequest("test.jpg", []byte("fake content"))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestUpload_MethodNotAllowed(t *testing.T) {
	status := NewStatus()
	srv := NewServer(testConfig(0, true, ""), status, silentLogger())

	req := httptest.NewRequest(http.MethodPut, "/upload", nil)
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestUpload_GETHTML(t *testing.T) {
	status := NewStatus()
	srv := NewServer(testConfig(0, true, ""), status, silentLogger())

	req := httptest.NewRequest(http.MethodGet, "/upload", nil)
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("expected HTML content-type, got %s", w.Header().Get("Content-Type"))
	}
}

func TestUpload_InvalidMultipart(t *testing.T) {
	status := NewStatus()
	srv := NewServer(testConfig(0, true, ""), status, silentLogger())

	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not multipart")))
	req.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUpload_UnsupportedType(t *testing.T) {
	status := NewStatus()
	srv := NewServer(testConfig(0, true, ""), status, silentLogger())

	req, err := createMultipartRequest("test.txt", []byte("plain text content which is not an image"))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestUpload_SuccessAndDeduplication(t *testing.T) {
	status := NewStatus()
	tmpDir := t.TempDir()
	srv := NewServer(testConfig(0, true, tmpDir), status, silentLogger())

	// Valid JPEG header is FFD8
	jpegContent := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01}

	// Add some padding to make it a mock JPEG image
	for i := 0; i < 500; i++ {
		jpegContent = append(jpegContent, 0x00)
	}

	req, err := createMultipartRequest("photo.jpg", jpegContent)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}

	filename, ok := resp["filename"].(string)
	if !ok || filename == "" {
		t.Fatalf("missing filename in response")
	}

	// Verify file is saved to disk
	filePath := filepath.Join(tmpDir, filename)
	if _, err := os.Stat(filePath); err != nil {
		t.Errorf("file was not saved: %v", err)
	}

	// Upload again (should trigger deduplication)
	req2, err := createMultipartRequest("photo.jpg", jpegContent)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	w2 := httptest.NewRecorder()
	srv.HandleUpload(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected 200 on duplicate, got %d", w2.Code)
	}

	var resp2 map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}

	if resp2["message"] != "File already exists (deduplicated)" {
		t.Errorf("expected duplicate message, got %q", resp2["message"])
	}
}

func TestUpload_SuccessRawBinary(t *testing.T) {
	status := NewStatus()
	tmpDir := t.TempDir()
	srv := NewServer(testConfig(0, true, tmpDir), status, silentLogger())

	// Valid JPEG header is FFD8
	jpegContent := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01}
	for i := 0; i < 500; i++ {
		jpegContent = append(jpegContent, 0x00)
	}

	// Create request with raw body and content type
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(jpegContent))
	req.Header.Set("Content-Type", "image/jpeg")

	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}

	filename, ok := resp["filename"].(string)
	if !ok || filename == "" {
		t.Fatalf("missing filename in response")
	}

	// Verify file is saved to disk
	filePath := filepath.Join(tmpDir, filename)
	if _, err := os.Stat(filePath); err != nil {
		t.Errorf("file was not saved: %v", err)
	}
}

func TestUpload_iOSShortcutSimulator(t *testing.T) {
	jpegContent := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01}
	for i := 0; i < 100; i++ {
		jpegContent = append(jpegContent, 0x00)
	}

	pngContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for i := 0; i < 100; i++ {
		pngContent = append(pngContent, 0x00)
	}

	largeContent := make([]byte, 1024*1024+100)
	copy(largeContent, []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01})

	tests := []struct {
		name           string
		maxSizeMB      int
		setupReq       func() (*http.Request, error)
		expectedStatus int
		expectedJSON   string
		expectedErr    string
		verifyExt      string
	}{
		{
			name:      "Valid Form JPEG",
			maxSizeMB: 0,
			setupReq: func() (*http.Request, error) {
				return createMultipartRequest("shortcut_favorite.jpg", jpegContent)
			},
			expectedStatus: http.StatusOK,
			expectedJSON:   "ok",
		},
		{
			name:      "Valid Form PNG",
			maxSizeMB: 0,
			setupReq: func() (*http.Request, error) {
				return createMultipartRequest("shortcut_fav.png", pngContent)
			},
			expectedStatus: http.StatusOK,
			expectedJSON:   "ok",
			verifyExt:      ".png",
		},
		{
			name:      "File Too Large",
			maxSizeMB: 1,
			setupReq: func() (*http.Request, error) {
				return createMultipartRequest("large.jpg", largeContent)
			},
			expectedStatus: http.StatusBadRequest,
			expectedJSON:   "error",
			expectedErr:    "too large",
		},
		{
			name:      "Missing File Field",
			maxSizeMB: 0,
			setupReq: func() (*http.Request, error) {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				part, err := writer.CreateFormField("not-file")
				if err != nil {
					return nil, err
				}
				_, _ = part.Write([]byte("some data"))
				_ = writer.Close()

				req := httptest.NewRequest(http.MethodPost, "/upload", &body)
				req.Header.Set("Content-Type", writer.FormDataContentType())
				return req, nil
			},
			expectedStatus: http.StatusBadRequest,
			expectedJSON:   "error",
			expectedErr:    "missing 'file' parameter",
		},
		{
			name:      "Unsupported Format",
			maxSizeMB: 0,
			setupReq: func() (*http.Request, error) {
				return createMultipartRequest("image.heic", []byte("heic image simulator data"))
			},
			expectedStatus: http.StatusBadRequest,
			expectedJSON:   "error",
			expectedErr:    "Unsupported file type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := NewStatus()
			tmpDir := t.TempDir()

			cfg := testConfig(0, true, tmpDir)
			if tt.maxSizeMB > 0 {
				cfg.MaxDownloadSizeMB = tt.maxSizeMB
			}
			srv := NewServer(cfg, status, silentLogger())

			req, err := tt.setupReq()
			if err != nil {
				t.Fatalf("setup request: %v", err)
			}

			w := httptest.NewRecorder()
			srv.HandleUpload(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected %d, got %d", tt.expectedStatus, w.Code)
			}

			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if resp["status"] != tt.expectedJSON {
				t.Errorf("expected status=%v, got %v", tt.expectedJSON, resp["status"])
			}

			verifyErrorResponse(t, resp, tt.expectedErr)
			verifySuccessResponse(t, resp, tmpDir, tt.expectedJSON, tt.verifyExt)
		})
	}
}

func verifyErrorResponse(t *testing.T, resp map[string]any, expectedErr string) {
	t.Helper()
	if expectedErr == "" {
		return
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, expectedErr) && (expectedErr != "too large" || !strings.Contains(errMsg, "limit")) {
		t.Errorf("expected error containing %q, got %q", expectedErr, errMsg)
	}
}

func verifySuccessResponse(t *testing.T, resp map[string]any, tmpDir, expectedJSON, verifyExt string) {
	t.Helper()
	if expectedJSON != "ok" {
		return
	}
	filename, ok := resp["filename"].(string)
	if !ok || filename == "" {
		t.Fatalf("missing filename in response")
	}

	if verifyExt != "" && !strings.HasSuffix(filename, verifyExt) {
		t.Errorf("expected %s extension, got %s", verifyExt, filename)
	}

	filePath := filepath.Join(tmpDir, filename)
	if _, err := os.Stat(filePath); err != nil {
		t.Errorf("file was not saved to disk: %v", err)
	}
}
