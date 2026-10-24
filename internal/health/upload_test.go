package health

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

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

func multipartBody(t *testing.T, field, filename string, content []byte) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	var part io.Writer
	var err error
	if filename == "" {
		part, err = writer.CreateFormField(field)
	} else {
		part, err = writer.CreateFormFile(field, filename)
	}
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}

func oversizedJPEG() []byte {
	content := make([]byte, 1<<20+100)
	copy(content, []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01})
	return content
}

func assertArtworkDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read artwork directory: %v", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("rejected upload published %s", entry.Name())
		}
	}
}

func TestUploadRejectsUnsafeRequestsWithoutPublishing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		configure func(*config.Config)
		method    string
		request   func(t *testing.T) (io.Reader, string)
		origin    string
		wantCode  int
		wantError string
	}{
		{
			name: "disabled", configure: func(cfg *config.Config) { cfg.UploadEnabled = false },
			method: http.MethodPost, wantCode: http.StatusForbidden, wantError: "disabled",
		},
		{
			name: "dry run", configure: func(cfg *config.Config) { cfg.DryRun = true },
			method: http.MethodGet, wantCode: http.StatusForbidden, wantError: "dry-run",
		},
		{name: "unsupported method", method: http.MethodPut, wantCode: http.StatusMethodNotAllowed, wantError: "Method not allowed"},
		{
			name: "cross-origin browser post", method: http.MethodPost, origin: "https://attacker.example",
			request: func(t *testing.T) (io.Reader, string) {
				return bytes.NewReader(encodedTestImage(t, "jpeg")), "image/jpeg"
			},
			wantCode: http.StatusForbidden, wantError: "Cross-origin",
		},
		{
			name: "malformed multipart", method: http.MethodPost,
			request: func(*testing.T) (io.Reader, string) {
				return strings.NewReader("not multipart"), "multipart/form-data; boundary=bad"
			},
			wantCode: http.StatusBadRequest, wantError: "file too large or invalid request",
		},
		{
			name: "multipart without file field", method: http.MethodPost,
			request: func(t *testing.T) (io.Reader, string) {
				return multipartBody(t, "not-file", "", []byte("some data"))
			},
			wantCode: http.StatusBadRequest, wantError: "missing 'file' parameter",
		},
		{
			name: "multipart unsupported format", method: http.MethodPost,
			request: func(t *testing.T) (io.Reader, string) {
				return multipartBody(t, "file", "image.heic", []byte("heic image simulator data"))
			},
			wantCode: http.StatusBadRequest, wantError: "Invalid or unsafe image",
		},
		{
			name: "raw non-image", method: http.MethodPost,
			request: func(*testing.T) (io.Reader, string) {
				return strings.NewReader("not an image"), "application/octet-stream"
			},
			wantCode: http.StatusBadRequest, wantError: "Invalid or unsafe image",
		},
		{
			name: "raw truncated JPEG", method: http.MethodPost,
			request: func(*testing.T) (io.Reader, string) {
				return bytes.NewReader([]byte{0xff, 0xd8, 0xff, 0xe0}), "image/jpeg"
			},
			wantCode: http.StatusBadRequest, wantError: "Invalid or unsafe image",
		},
		{
			name: "raw body above limit", configure: func(cfg *config.Config) { cfg.MaxDownloadSizeMB = 1 }, method: http.MethodPost,
			request: func(*testing.T) (io.Reader, string) {
				return bytes.NewReader(oversizedJPEG()), "image/jpeg"
			},
			wantCode: http.StatusBadRequest, wantError: "too large",
		},
		{
			name: "multipart file above limit", configure: func(cfg *config.Config) { cfg.MaxDownloadSizeMB = 1 }, method: http.MethodPost,
			request: func(t *testing.T) (io.Reader, string) {
				return multipartBody(t, "file", "large.jpg", oversizedJPEG())
			},
			wantCode: http.StatusBadRequest, wantError: "too large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			cfg := testConfig(0, true, directory)
			if test.configure != nil {
				test.configure(cfg)
			}
			var body io.Reader
			contentType := ""
			if test.request != nil {
				body, contentType = test.request(t)
			}
			request := httptest.NewRequest(test.method, "http://frame.local/upload", body)
			if contentType != "" {
				request.Header.Set("Content-Type", contentType)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()

			newTestServer(t, cfg, NewStatus()).HandleUpload(response, request)

			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantCode, response.Body.String())
			}
			payload := decodeJSONResponse(t, response)
			if message, _ := payload[fieldError].(string); payload[fieldStatus] != statusError || !strings.Contains(message, test.wantError) {
				t.Fatalf("error response = %#v, want message containing %q", payload, test.wantError)
			}
			if response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("upload response omitted security headers: %v", response.Header())
			}
			assertArtworkDirectoryEmpty(t, directory)
		})
	}
}

func TestUploadRejectsMalformedOriginsBeforeImport(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"http://frame.local/path", "http://frame.local/", "http://frame.local?query",
		"http://frame.local?", "http://frame.local#fragment", "http://frame.local#",
		"http://attacker@frame.local", "null", "ftp://frame.local", "http://other.local",
		"", "http://frame.local" + strings.Repeat("a", 2048),
	} {
		t.Run(origin, func(t *testing.T) {
			t.Parallel()
			server := NewServer(testConfig(0, true, t.TempDir()), NewStatus(), silentLogger(), nil)
			request := httptest.NewRequest(http.MethodPost, "http://frame.local/upload", bytes.NewReader(encodedTestImage(t, "png")))
			request.Header.Set("Origin", origin)
			response := httptest.NewRecorder()
			server.HandleUpload(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("upload status = %d, want 403 before the importer is called", response.Code)
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "http://frame.local/upload", nil)
	request.Header.Add("Origin", "http://frame.local")
	request.Header.Add("Origin", "http://frame.local")
	if validUploadOrigin(request) {
		t.Fatal("duplicate Origin headers must be rejected")
	}
}

func TestUploadEndpointRequiresConfiguredCredentials(t *testing.T) {
	t.Parallel()
	cfg := testConfig(0, true, t.TempDir())
	cfg.UploadToken = "correct-horse-battery-staple"
	server := newTestServer(t, cfg, NewStatus())

	for _, test := range []struct {
		name     string
		user     string
		password string
		wantCode int
	}{
		{name: "missing credentials", wantCode: http.StatusUnauthorized},
		{name: "wrong user", user: "admin", password: cfg.UploadToken, wantCode: http.StatusUnauthorized},
		{name: "wrong token", user: "frame", password: "guess", wantCode: http.StatusUnauthorized},
		{name: "valid credentials", user: "frame", password: cfg.UploadToken, wantCode: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/upload", nil)
			if test.user != "" {
				request.SetBasicAuth(test.user, test.password)
			}
			response := httptest.NewRecorder()
			server.HandleUpload(response, request)
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, test.wantCode)
			}
			if challenged := response.Header().Get("WWW-Authenticate") != ""; challenged != (test.wantCode == http.StatusUnauthorized) {
				t.Fatalf("WWW-Authenticate = %q for status %d", response.Header().Get("WWW-Authenticate"), response.Code)
			}
		})
	}
}

func TestUploadPageIsServedAsHTML(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	newTestServer(t, testConfig(0, true, ""), NewStatus()).HandleUpload(response, httptest.NewRequest(http.MethodGet, "/upload", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("upload page status/content type = %d/%q", response.Code, response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("upload page omitted security headers: %v", response.Header())
	}
}

func TestUploadImportsThroughCollectionAndDeduplicates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		format    string
		multipart bool
	}{
		{name: "multipart JPEG", format: "jpeg", multipart: true},
		{name: "multipart PNG", format: "png", multipart: true},
		{name: "raw JPEG", format: "jpeg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			store, err := collection.New(collection.Config{Root: root, MaxImportBytes: 1 << 20})
			if err != nil {
				t.Fatalf("construct collection: %v", err)
			}
			server := NewServer(testConfig(0, true, root), NewStatus(), silentLogger(), store)
			payload := encodedTestImage(t, test.format)
			upload := func() map[string]any {
				t.Helper()
				var body io.Reader = bytes.NewReader(payload)
				contentType := "image/" + test.format
				if test.multipart {
					body, contentType = multipartBody(t, "file", "shortcut."+test.format, payload)
				}
				request := httptest.NewRequest(http.MethodPost, "/upload", body)
				request.Header.Set("Content-Type", contentType)
				response := httptest.NewRecorder()
				server.HandleUpload(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
				}
				return decodeJSONResponse(t, response)
			}

			first := upload()
			filename, _ := first[fieldFilename].(string)
			if first[fieldStatus] != statusOK || first[fieldMessage] != "File imported successfully" || filename == "" {
				t.Fatalf("first upload response = %#v", first)
			}
			wantExtension := map[string]string{"jpeg": ".jpg", "png": ".png"}[test.format]
			if filepath.Ext(filename) != wantExtension {
				t.Fatalf("filename = %q, want extension %s", filename, wantExtension)
			}
			snapshot, err := store.Prepare(t.Context(), collection.PrepareRequest{DryRun: true})
			if err != nil {
				t.Fatalf("read collection: %v", err)
			}
			if len(snapshot.Items) != 1 || snapshot.Items[0].Name != filename {
				t.Fatalf("committed items = %+v, want %s", snapshot.Items, filename)
			}

			second := upload()
			if second[fieldMessage] != "File already exists (deduplicated)" || second[fieldFilename] != filename {
				t.Fatalf("duplicate upload response = %#v", second)
			}
		})
	}
}

func TestUploadReportsCollectionFailures(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "file")
	if err := os.WriteFile(notDirectory, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, testConfig(0, true, notDirectory), NewStatus())
	request := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(encodedTestImage(t, "jpeg")))
	request.Header.Set("Content-Type", "image/jpeg")
	response := httptest.NewRecorder()
	server.HandleUpload(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("upload to invalid artwork directory returned %d, want 500: %s", response.Code, response.Body.String())
	}

	unavailable := NewServer(testConfig(0, true, parent), NewStatus(), silentLogger(), nil)
	request = httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(encodedTestImage(t, "jpeg")))
	request.Header.Set("Content-Type", "image/jpeg")
	response = httptest.NewRecorder()
	unavailable.HandleUpload(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload without collection returned %d, want 503", response.Code)
	}
}

func TestMaxUploadBytesFallsBackToSafeDefault(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		configured int
		want       int64
	}{
		{configured: 0, want: defaultMaxUploadBytes},
		{configured: -1, want: defaultMaxUploadBytes},
		{configured: 3, want: 3 << 20},
	} {
		cfg := testConfig(0, false, "")
		cfg.MaxDownloadSizeMB = test.configured
		if got := NewServer(cfg, NewStatus(), silentLogger(), nil).maxUploadBytes(); got != test.want {
			t.Errorf("maxUploadBytes(%d MB) = %d, want %d", test.configured, got, test.want)
		}
	}
}
