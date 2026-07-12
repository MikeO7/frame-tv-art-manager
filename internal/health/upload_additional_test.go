package health

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestUploadValidationBoundaries(t *testing.T) {
	t.Run("truncated supported image", func(t *testing.T) {
		err := validateUploadedImage([]byte{0xff, 0xd8, 0xff, 0xe0})
		if err == nil || !strings.Contains(err.Error(), "decode header") {
			t.Fatalf("validateUploadedImage() error = %v", err)
		}
	})

	t.Run("excessive width", func(t *testing.T) {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 16385, 1))); err != nil {
			t.Fatal(err)
		}
		err := validateUploadedImage(encoded.Bytes())
		if err == nil || !strings.Contains(err.Error(), "exceed limits") {
			t.Fatalf("validateUploadedImage() error = %v", err)
		}
	})

	t.Run("valid image", func(t *testing.T) {
		if err := validateUploadedImage(encodedTestImage(t, "png")); err != nil {
			t.Fatalf("validateUploadedImage: %v", err)
		}
	})
}

func TestUploadHelpersAndPersistenceFailure(t *testing.T) {
	if got := (&uploadError{msg: "sentinel"}).Error(); got != "sentinel" {
		t.Fatalf("uploadError.Error() = %q", got)
	}
	if _, _, uploadErr := readImagePayload(failingReader{}); uploadErr == nil || uploadErr.code != http.StatusInternalServerError {
		t.Fatalf("readImagePayload failure = %#v", uploadErr)
	}

	cfg := testConfig(0, true, t.TempDir())
	cfg.MaxDownloadSizeMB = 0
	if got := NewServer(cfg, NewStatus(), silentLogger()).maxUploadBytes(); got != defaultMaxUploadBytes {
		t.Fatalf("maxUploadBytes() = %d, want %d", got, defaultMaxUploadBytes)
	}

	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "file")
	if err := os.WriteFile(notDirectory, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(testConfig(0, true, notDirectory), NewStatus(), silentLogger())
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(encodedTestImage(t, "jpeg")))
	req.Header.Set("Content-Type", "image/jpeg")
	w := httptest.NewRecorder()
	srv.HandleUpload(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("upload to invalid artwork directory returned %d, want 500: %s", w.Code, w.Body.String())
	}
}

func TestUploadHelpers(t *testing.T) {
	t.Run("readFailure", func(t *testing.T) {
		if got := readFailure(&http.MaxBytesError{Limit: 10}, "read body"); got.code != http.StatusBadRequest {
			t.Fatalf("expected too-large error, got %q", got.Error())
		}

		if got := readFailure(io.ErrUnexpectedEOF, "read body"); got.code != http.StatusInternalServerError {
			t.Fatalf("expected internal error code, got %d", got.code)
		}
	})

	t.Run("parseUploadedFile_invalidMultipart", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not multipart")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=bad")

		_, err := parseUploadedFile(req, 1<<10)
		if err == nil || err.Error() != "file too large or invalid request" {
			t.Fatalf("parseUploadedFile() error = %v", err)
		}
	})
}

func TestAtomicWriteArtwork_InvalidPath(t *testing.T) {
	t.Run("create temp failure", func(t *testing.T) {
		if err := atomicWriteArtwork("/this/path/does/not/exist/out.jpg", []byte("boom")); err == nil {
			t.Fatal("expected temporary-write failure")
		}
	})
}

func TestHealthEndpointFailedCycle(t *testing.T) {
	status := NewStatus()
	status.RecordSync(false, os.ErrDeadlineExceeded)
	w := httptest.NewRecorder()
	NewServer(testConfig(0, false, ""), status, silentLogger()).handleHealth(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleHealth() status = %d, want 503", w.Code)
	}
}
