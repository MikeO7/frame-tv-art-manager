package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func newTestServer(t *testing.T, cfg *config.Config, status *Status, logger *slog.Logger) *Server {
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
	return NewServer(cfg, status, logger, importer)
}

func TestBindRejectsUploadWithoutCollection(t *testing.T) {
	t.Parallel()
	cfg := testConfig(1, true, t.TempDir())
	server := NewServer(cfg, NewStatus(), silentLogger(), nil)

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

func encodedTestImage(t *testing.T, format string) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var err error
	if format == "png" {
		err = png.Encode(&out, img)
	} else {
		err = jpeg.Encode(&out, img, nil)
	}
	if err != nil {
		t.Fatalf("encode test image: %v", err)
	}
	return out.Bytes()
}

func TestHealthEndpoint(t *testing.T) {
	status := NewStatus()
	status.SetLifecycle("ready")
	status.RecordSync(true, nil)
	status.SetTVStatus("192.168.1.1", TVStatus{
		IP:         "192.168.1.1",
		LastSeen:   time.Now().Format(time.RFC3339),
		ImageCount: 42,
		ArtMode:    true,
		Status:     "ok",
	})

	srv := newTestServer(t, testConfig(0, false, ""), status, silentLogger())
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
	server := newTestServer(t, testConfig(0, false, ""), status, logger) // Port 0 doesn't actually start, but we can call handlers.

	// Test handleHealth
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	server.handleHealth(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("handler returned wrong status code: got %v want %v", rr.Code, http.StatusServiceUnavailable)
	}

	// Test handleStatus
	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	rr = httptest.NewRecorder()
	server.handleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", rr.Code, http.StatusOK)
	}
}

func TestStatusEndpoint(t *testing.T) {
	status := NewStatus()
	status.RecordSync(false, nil)

	srv := newTestServer(t, testConfig(0, false, ""), status, silentLogger())
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

func TestShutdown_NilServer(t *testing.T) {
	// Shutdown on a server that was never started should not panic.
	srv := newTestServer(t, testConfig(0, false, ""), NewStatus(), silentLogger())
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

func TestSetTVStatusPreservesLastKnownSeenTime(t *testing.T) {
	status := NewStatus()
	status.SetTVStatus("192.0.2.10", TVStatus{IP: "192.0.2.10", LastSeen: "known", Status: "ok"})
	status.SetTVStatus("192.0.2.10", TVStatus{IP: "192.0.2.10", Status: "backoff"})
	if got := status.snapshot().TVStatuses["192.0.2.10"].LastSeen; got != "known" {
		t.Fatalf("LastSeen = %q, want known", got)
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
	srv := newTestServer(t, testConfig(0, false, ""), status, silentLogger())

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
	srv := newTestServer(t, testConfig(0, true, ""), status, silentLogger())

	req := httptest.NewRequest(http.MethodPut, "/upload", nil)
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestUpload_GETHTML(t *testing.T) {
	status := NewStatus()
	srv := newTestServer(t, testConfig(0, true, ""), status, silentLogger())

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
	srv := newTestServer(t, testConfig(0, true, ""), status, silentLogger())

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
	srv := newTestServer(t, testConfig(0, true, ""), status, silentLogger())

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
	srv := newTestServer(t, testConfig(0, true, tmpDir), status, silentLogger())

	jpegContent := encodedTestImage(t, "jpeg")

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
	srv := newTestServer(t, testConfig(0, true, tmpDir), status, silentLogger())

	jpegContent := encodedTestImage(t, "jpeg")

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

func TestUpload_RawBinaryTooLarge(t *testing.T) {
	status := NewStatus()
	tmpDir := t.TempDir()
	cfg := testConfig(0, true, tmpDir)
	cfg.MaxDownloadSizeMB = 1
	srv := newTestServer(t, cfg, status, silentLogger())

	// Valid JPEG header, but body exceeds the 1 MB limit.
	largeContent := make([]byte, 1024*1024+100)
	copy(largeContent, []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01})

	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(largeContent))
	req.Header.Set("Content-Type", "image/jpeg")

	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "error" {
		t.Errorf("expected status=error, got %v", resp["status"])
	}

	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "too large") {
		t.Errorf("expected error message about size, got %q", errMsg)
	}

	// Ensure nothing was written to disk.
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected no files saved on oversized upload, got %d", len(files))
	}
}

func TestUpload_iOSShortcutSimulator(t *testing.T) {
	// 1. Success case with form file parameter "file" (iOS Shortcut style)
	t.Run("Valid Form JPEG", func(t *testing.T) {
		status := NewStatus()
		tmpDir := t.TempDir()
		srv := newTestServer(t, testConfig(0, true, tmpDir), status, silentLogger())

		jpegContent := encodedTestImage(t, "jpeg")

		req, err := createMultipartRequest("shortcut_favorite.jpg", jpegContent)
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

		filePath := filepath.Join(tmpDir, filename)
		if _, err := os.Stat(filePath); err != nil {
			t.Errorf("file was not saved to disk: %v", err)
		}
	})

	// 2. Success case with PNG
	t.Run("Valid Form PNG", func(t *testing.T) {
		status := NewStatus()
		tmpDir := t.TempDir()
		srv := newTestServer(t, testConfig(0, true, tmpDir), status, silentLogger())

		pngContent := encodedTestImage(t, "png")

		req, err := createMultipartRequest("shortcut_fav.png", pngContent)
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

		if !strings.HasSuffix(filename, ".png") {
			t.Errorf("expected png extension, got %s", filename)
		}
	})

	// 3. Error case - file too large
	t.Run("File Too Large", func(t *testing.T) {
		status := NewStatus()
		tmpDir := t.TempDir()
		cfg := testConfig(0, true, tmpDir)
		cfg.MaxDownloadSizeMB = 1 // 1 MB max size
		srv := newTestServer(t, cfg, status, silentLogger())

		// Create content larger than 1MB
		largeContent := make([]byte, 1024*1024+100)
		// Set JPEG prefix just to make it otherwise valid
		copy(largeContent, []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01})

		req, err := createMultipartRequest("large.jpg", largeContent)
		if err != nil {
			t.Fatalf("create request: %v", err)
		}

		w := httptest.NewRecorder()
		srv.HandleUpload(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request, got %d", w.Code)
		}

		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		if resp["status"] != "error" {
			t.Errorf("expected status=error, got %v", resp["status"])
		}

		errMsg, _ := resp["error"].(string)
		if !strings.Contains(errMsg, "too large") && !strings.Contains(errMsg, "limit") {
			t.Errorf("expected error message about size/limit, got %q", errMsg)
		}
	})

	// 4. Error case - missing file field
	t.Run("Missing File Field", func(t *testing.T) {
		status := NewStatus()
		tmpDir := t.TempDir()
		srv := newTestServer(t, testConfig(0, true, tmpDir), status, silentLogger())

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		// create some other field instead of "file"
		part, err := writer.CreateFormField("not-file")
		if err != nil {
			t.Fatalf("create form field: %v", err)
		}
		_, _ = part.Write([]byte("some data"))
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		w := httptest.NewRecorder()
		srv.HandleUpload(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}

		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		if resp["status"] != "error" {
			t.Errorf("expected status=error, got %v", resp["status"])
		}

		if !strings.Contains(resp["error"].(string), "missing 'file' parameter") {
			t.Errorf("expected error message about missing file parameter, got %q", resp["error"])
		}
	})

	// 5. Error case - unsupported format (HEIC/video)
	t.Run("Unsupported Format", func(t *testing.T) {
		status := NewStatus()
		tmpDir := t.TempDir()
		srv := newTestServer(t, testConfig(0, true, tmpDir), status, silentLogger())

		// Mock HEIC image simulator data
		heicContent := []byte("heic image simulator data")
		req, err := createMultipartRequest("image.heic", heicContent)
		if err != nil {
			t.Fatalf("create request: %v", err)
		}

		w := httptest.NewRecorder()
		srv.HandleUpload(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}

		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		if resp["status"] != "error" {
			t.Errorf("expected status=error, got %v", resp["status"])
		}

		if !strings.Contains(resp["error"].(string), "Invalid or unsafe image") {
			t.Errorf("expected safe invalid-image message, got %q", resp["error"])
		}
	})
}
