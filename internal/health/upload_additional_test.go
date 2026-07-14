package health

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestUploadHelpersAndPersistenceFailure(t *testing.T) {
	cfg := testConfig(0, true, t.TempDir())
	cfg.MaxDownloadSizeMB = 0
	if got := newTestServer(t, cfg, NewStatus(), silentLogger()).maxUploadBytes(); got != defaultMaxUploadBytes {
		t.Fatalf("maxUploadBytes() = %d, want %d", got, defaultMaxUploadBytes)
	}

	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "file")
	if err := os.WriteFile(notDirectory, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, testConfig(0, true, notDirectory), NewStatus(), silentLogger())
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(encodedTestImage(t, "jpeg")))
	req.Header.Set("Content-Type", "image/jpeg")
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("upload to invalid artwork directory returned %d, want 500: %s", w.Code, w.Body.String())
	}
}

func TestUploadHelpers(t *testing.T) {
	t.Run("parseUploadedFile_invalidMultipart", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not multipart")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=bad")

		_, err := parseUploadedFile(req, 1<<10)
		if err == nil || err.Error() != "file too large or invalid request" {
			t.Fatalf("parseUploadedFile() error = %v", err)
		}
	})
}

func TestProcessUpload_InvalidImagePayload(t *testing.T) {
	cfg := testConfig(0, true, t.TempDir())
	srv := newTestServer(t, cfg, NewStatus(), silentLogger())

	truncatedJPEG := []byte{0xff, 0xd8, 0xff, 0xe0}
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(truncatedJPEG))
	req.Header.Set("Content-Type", "image/jpeg")

	w := httptest.NewRecorder()
	srv.processUpload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("processUpload() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Invalid or unsafe image") {
		t.Fatalf("unexpected processUpload body: %s", body)
	}
}

func TestHealthEndpointFailedCycle(t *testing.T) {
	status := NewStatus()
	status.RecordSync(false, os.ErrDeadlineExceeded)
	w := httptest.NewRecorder()
	newTestServer(t, testConfig(0, false, ""), status, silentLogger()).handleHealth(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleHealth() status = %d, want 503", w.Code)
	}
}

func TestUploadEndpointRequiresConfiguredCredentials(t *testing.T) {
	cfg := testConfig(0, true, t.TempDir())
	cfg.UploadToken = "correct-horse-battery-staple"
	server := newTestServer(t, cfg, NewStatus(), silentLogger())

	unauthorized := httptest.NewRecorder()
	server.HandleUpload(unauthorized, httptest.NewRequest(http.MethodGet, "/upload", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthorized response = %d, headers %v", unauthorized.Code, unauthorized.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/upload", nil)
	request.SetBasicAuth("frame", cfg.UploadToken)
	authorized := httptest.NewRecorder()
	server.HandleUpload(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200", authorized.Code)
	}
}

func TestUploadRejectsCrossOriginBrowserPost(t *testing.T) {
	cfg := testConfig(0, true, t.TempDir())
	server := newTestServer(t, cfg, NewStatus(), silentLogger())
	request := httptest.NewRequest(http.MethodPost, "http://frame.local/upload", bytes.NewReader(encodedTestImage(t, "jpeg")))
	request.Header.Set("Content-Type", "image/jpeg")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	server.HandleUpload(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upload status = %d, want 403", response.Code)
	}
}

func TestUploadEndpointIsDisabledDuringDryRun(t *testing.T) {
	cfg := testConfig(0, true, t.TempDir())
	cfg.DryRun = true
	response := httptest.NewRecorder()
	newTestServer(t, cfg, NewStatus(), silentLogger()).HandleUpload(
		response,
		httptest.NewRequest(http.MethodGet, "/upload", nil),
	)
	if response.Code != http.StatusForbidden {
		t.Fatalf("dry-run upload status = %d, want 403", response.Code)
	}
}

func TestUploadUsesTransactionalCollectionImporter(t *testing.T) {
	root := t.TempDir()
	store, err := collection.New(collection.Config{Root: root, MaxImportBytes: 1 << 20})
	if err != nil {
		t.Fatalf("construct collection: %v", err)
	}
	cfg := testConfig(0, true, root)
	server := NewServer(cfg, NewStatus(), silentLogger(), store)
	payload := encodedTestImage(t, "jpeg")
	request := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "image/jpeg")
	response := httptest.NewRecorder()

	server.HandleUpload(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}
	snapshot, err := store.Prepare(request.Context(), collection.PrepareRequest{DryRun: true})
	if err != nil {
		t.Fatalf("read collection: %v", err)
	}
	if len(snapshot.Items) != 1 || !strings.Contains(response.Body.String(), snapshot.Items[0].Name) {
		t.Fatalf("committed items = %+v, response = %s", snapshot.Items, response.Body.String())
	}
}

func TestTransactionalImporterRejectsInvalidBytesAsBadRequest(t *testing.T) {
	root := t.TempDir()
	store, err := collection.New(collection.Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(testConfig(0, true, root), NewStatus(), silentLogger(), store)
	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("not an image"))
	request.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()

	server.HandleUpload(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid upload status = %d, want 400", response.Code)
	}
}
