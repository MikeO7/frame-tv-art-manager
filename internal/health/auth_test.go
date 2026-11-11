package health

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

const testUploadToken = "correct-horse-battery-staple"

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func authTestServer(t *testing.T, token string) (*server, *fakeClock) {
	t.Helper()
	cfg := testConfig(0, true, t.TempDir())
	cfg.UploadToken = token
	server := newTestServer(t, cfg, NewStatus())
	clock := &fakeClock{now: time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)}
	server.now = clock.Now
	return server, clock
}

func statusRequest(client, user, password string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://frame.local/status", nil)
	request.RemoteAddr = client + ":40000"
	if user != "" || password != "" {
		request.SetBasicAuth(user, password)
	}
	return request
}

func TestAuthFailuresAreRateLimitedPerClient(t *testing.T) {
	t.Parallel()
	server, clock := authTestServer(t, testUploadToken)
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.handleStatus(response, request)
		return response
	}

	for attempt := 1; attempt <= authFailureLimit; attempt++ {
		if code := serve(statusRequest("192.0.2.50", "frame", "wrong")).Code; code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d status = %d, want 401", attempt, code)
		}
	}
	limited := serve(statusRequest("192.0.2.50", "frame", "wrong"))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong credentials beyond the budget status = %d, want 429", limited.Code)
	}
	if retry, err := strconv.Atoi(limited.Header().Get("Retry-After")); err != nil || retry < 1 || retry > 60 {
		t.Fatalf("Retry-After = %q, want 1..60 seconds", limited.Header().Get("Retry-After"))
	}
	upload := httptest.NewRecorder()
	uploadRequest := httptest.NewRequest(http.MethodGet, "http://frame.local/upload", nil)
	uploadRequest.RemoteAddr = "192.0.2.50:40001"
	uploadRequest.SetBasicAuth("frame", "another-guess")
	server.HandleUpload(upload, uploadRequest)
	if upload.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong upload credentials during lockout status = %d, want 429 on every route", upload.Code)
	}

	if code := serve(statusRequest("192.0.2.51", "frame", "wrong")).Code; code != http.StatusUnauthorized {
		t.Fatalf("another client status = %d, want its own budget (401)", code)
	}
	clock.now = clock.now.Add(authFailureWindow)
	if code := serve(statusRequest("192.0.2.50", "frame", "wrong")).Code; code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials after the window = %d, want a fresh budget (401)", code)
	}
}

func TestValidCredentialsAreNeverLockedOut(t *testing.T) {
	t.Parallel()
	server, _ := authTestServer(t, testUploadToken)
	for attempt := 0; attempt < authFailureLimit*3; attempt++ {
		response := httptest.NewRecorder()
		server.handleStatus(response, statusRequest("192.0.2.50", "frame", "wrong"))
	}
	status := httptest.NewRecorder()
	server.handleStatus(status, statusRequest("192.0.2.50", "frame", testUploadToken))
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), `"redacted"`) {
		t.Fatalf("valid credentials after wrong guesses from the same address = %d %s, want the full 200 status",
			status.Code, status.Body.String())
	}
	upload := httptest.NewRecorder()
	uploadRequest := authedUpload(http.MethodGet, "http://frame.local/upload", nil)
	uploadRequest.RemoteAddr = "192.0.2.50:40001"
	server.HandleUpload(upload, uploadRequest)
	if upload.Code != http.StatusOK {
		t.Fatalf("valid upload credentials after wrong guesses = %d, want 200", upload.Code)
	}
	anonymous := httptest.NewRecorder()
	server.handleStatus(anonymous, statusRequest("192.0.2.50", "", ""))
	if anonymous.Code != http.StatusOK {
		t.Fatalf("anonymous status from a limited address = %d, want the redacted 200", anonymous.Code)
	}
}

func TestParallelWrongCredentialsShareOneBudget(t *testing.T) {
	t.Parallel()
	server, _ := authTestServer(t, testUploadToken)
	const guesses = authFailureLimit * 20
	codes := make(chan int, guesses)
	start := make(chan struct{})
	var group sync.WaitGroup
	for range guesses {
		group.Go(func() {
			<-start
			response := httptest.NewRecorder()
			server.handleStatus(response, statusRequest("192.0.2.80", "frame", "wrong"))
			codes <- response.Code
		})
	}
	close(start)
	group.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusUnauthorized] != authFailureLimit || counts[http.StatusTooManyRequests] != guesses-authFailureLimit {
		t.Fatalf("parallel wrong guesses = %v, want exactly %d judged (401) and the rest refused (429)", counts, authFailureLimit)
	}
}

func TestAuthLimiterIgnoresAbsentCredentials(t *testing.T) {
	t.Parallel()
	server, _ := authTestServer(t, testUploadToken)
	for attempt := 0; attempt < authFailureLimit*2; attempt++ {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://frame.local/upload", nil)
		request.RemoteAddr = "192.0.2.60:1234"
		server.HandleUpload(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous probe %d status = %d, want 401", attempt, response.Code)
		}
	}
}

func TestAuthLimiterMemoryIsBounded(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	limiter := newAuthLimiter(clock.Now)
	for index := 0; index < authTrackedClients+50; index++ {
		clock.now = clock.now.Add(time.Millisecond)
		limiter.failure(fmt.Sprintf("client-%d", index))
	}
	if len(limiter.failures) > authTrackedClients {
		t.Fatalf("tracked clients = %d, want at most %d", len(limiter.failures), authTrackedClients)
	}
	if _, ok := limiter.failures["client-0"]; ok {
		t.Fatal("the oldest client window was not evicted")
	}
	clock.now = clock.now.Add(authFailureWindow)
	limiter.failure("late")
	if wait := limiter.failure("client-60"); wait != 0 {
		t.Fatalf("failure in an expired window must wait %v, want 0", wait)
	}
}

func TestStatusRedactsDetailsForAnonymousCallers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		token      string
		user, pass string
		wantCode   int
		wantDetail bool
	}{
		{name: "no token configured", wantCode: http.StatusOK, wantDetail: true},
		{name: "anonymous with token", token: testUploadToken, wantCode: http.StatusOK},
		{name: "authenticated", token: testUploadToken, user: "frame", pass: testUploadToken, wantCode: http.StatusOK, wantDetail: true},
		{name: "wrong credentials", token: testUploadToken, user: "frame", pass: "nope", wantCode: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, _ := authTestServer(t, test.token)
			server.status.RecordSync(false, errors.New("dial 192.0.2.10: refused"))
			server.status.SetTVStatus("192.0.2.10", TVStatus{IP: "192.0.2.10", Status: "unreachable", LastErrorMessage: "refused"})
			server.status.SetTVStatus("192.0.2.9", TVStatus{IP: "192.0.2.9", Status: "ok", ImageCount: 4})
			response := httptest.NewRecorder()
			server.handleStatus(response, statusRequest("192.0.2.70", test.user, test.pass))
			if response.Code != test.wantCode {
				t.Fatalf("status code = %d, want %d", response.Code, test.wantCode)
			}
			if test.wantCode != http.StatusOK {
				return
			}
			body := response.Body.String()
			if test.wantDetail {
				if !strings.Contains(body, "refused") || !strings.Contains(body, `"192.0.2.10"`) {
					t.Fatalf("authorized status omitted detail: %s", body)
				}
				return
			}
			if strings.Contains(body, "192.0.2.") || strings.Contains(body, "refused") {
				t.Fatalf("anonymous status leaked addresses or errors: %s", body)
			}
			payload := decodeJSONResponse(t, response)
			tvs, _ := payload["tvs"].(map[string]any)
			first, _ := tvs["tv-1"].(map[string]any)
			second, _ := tvs["tv-2"].(map[string]any)
			if payload[fieldRedacted] != true || first["status"] != "unreachable" || second["image_count"] != float64(4) {
				t.Fatalf("redacted status = %#v, want TVs numbered in address order", payload)
			}
		})
	}
}

func TestUploadStreamsMultipartFileAfterOtherFields(t *testing.T) {
	t.Parallel()
	server, _ := authTestServer(t, testUploadToken)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("note", "from the phone"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "art.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(encodedTestImage(t, "png")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := authedUpload(http.MethodPost, "http://frame.local/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.HandleUpload(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

func TestUploadReportsFullCollectionAsConflict(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := collection.New(collection.Config{Root: directory, MaxItems: 1, MaxImportBytes: 20 << 20})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(testConfig(0, true, directory), NewStatus(), silentLogger(), store)
	post := func(image []byte) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.HandleUpload(response, authedUpload(http.MethodPost, "http://frame.local/upload", bytes.NewReader(image)))
		return response
	}
	if response := post(encodedTestImage(t, "png")); response.Code != http.StatusOK {
		t.Fatalf("first upload status = %d: %s", response.Code, response.Body.String())
	}
	response := post(encodedTestImage(t, "jpeg"))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "MAX_ARTWORK_IMAGES") {
		t.Fatalf("full collection upload = %d %s, want 409 naming MAX_ARTWORK_IMAGES", response.Code, response.Body.String())
	}
}

func TestUploadSpoolIsRemovedAfterImport(t *testing.T) {
	spoolDirectory := t.TempDir()
	t.Setenv("TMPDIR", spoolDirectory)
	server, _ := authTestServer(t, testUploadToken)
	server.cfg.MaxDownloadSizeMB = 1
	for _, body := range [][]byte{encodedTestImage(t, "png"), []byte("not an image"), oversizedJPEG(), {}} {
		response := httptest.NewRecorder()
		server.HandleUpload(response, authedUpload(http.MethodPost, "http://frame.local/upload", bytes.NewReader(body)))
		if response.Code != http.StatusOK && response.Code != http.StatusBadRequest && response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
		}
	}
	entries, err := os.ReadDir(spoolDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("upload spool files remain: %v", entries)
	}
}

func TestSpoolUploadReportsInterruptedBody(t *testing.T) {
	t.Parallel()
	request := authedUpload(http.MethodPost, "http://frame.local/upload", io.MultiReader(
		strings.NewReader("partial"), errorReader{},
	))
	_, err := spoolUpload(request)
	var rejected uploadError
	if !errors.As(err, &rejected) || !strings.Contains(rejected.message, "interrupted") {
		t.Fatalf("spoolUpload() error = %v, want interrupted upload", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
