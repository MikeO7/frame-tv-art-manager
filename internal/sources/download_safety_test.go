package sources

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

type downloadRoundTripper func(*http.Request) (*http.Response, error)

func (f downloadRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// newDownloadTestLoader isolates the process temporary directory so tests can
// prove rejected downloads leave no temporary files behind.
func newDownloadTestLoader(t *testing.T, response func(*http.Request) *http.Response) (*Loader, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	artworkDir := t.TempDir()
	loader := newTestLoader(&config.Config{ArtworkDir: artworkDir, MaxDownloadSizeMB: 1}, slog.New(slog.DiscardHandler))
	loader.client = &http.Client{Transport: downloadRoundTripper(func(request *http.Request) (*http.Response, error) {
		return response(request), nil
	})}
	return loader, tempDir, artworkDir
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("%s retained %v (read error %v)", directory, entries, err)
	}
}

func TestPrepareDownloadRejectsUnsafeResponsesWithoutLeavingFiles(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		length      int64
		body        string
		want        string
	}{
		{name: "truncated body", contentType: "image/jpeg", length: 12, body: "short", want: "incomplete download"},
		{name: "non-image content", contentType: "text/html", body: "<html>not artwork</html>", want: "validate downloaded image"},
		{name: "corrupt image bytes", contentType: "image/jpeg", body: "not a jpeg", want: "validate downloaded image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loader, tempDir, artworkDir := newDownloadTestLoader(t, func(request *http.Request) *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK, ContentLength: test.length,
					Header:  http.Header{"Content-Type": []string{test.contentType}},
					Body:    io.NopCloser(strings.NewReader(test.body)),
					Request: request,
				}
			})
			_, err := loader.prepareDownload(context.Background(), "https://example.test/art.jpg", "art.jpg", "direct--art")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepareDownload() error = %v, want %q", err, test.want)
			}
			assertDirectoryEmpty(t, tempDir)
			assertDirectoryEmpty(t, artworkDir)
		})
	}
}

func TestPrepareDownloadRejectsUnsupportedFormatBeforeReadingBody(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("webp")}
	loader, tempDir, _ := newDownloadTestLoader(t, func(request *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/webp"}},
			Body: body, Request: request,
		}
	})
	_, err := loader.prepareDownload(context.Background(), "https://example.test/art.webp", "art.jpg", "direct--art")
	if err == nil || !strings.Contains(err.Error(), "unsupported source image format") {
		t.Fatalf("prepareDownload() error = %v", err)
	}
	if body.read || !body.closed {
		t.Fatalf("unsupported response body read=%v closed=%v", body.read, body.closed)
	}
	assertDirectoryEmpty(t, tempDir)
}

func TestPrepareDownloadStagesPrivateTemporaryFileWithDetectedExtension(t *testing.T) {
	payload := testPNGBytes(t)
	loader, tempDir, artworkDir := newDownloadTestLoader(t, func(request *http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: int64(len(payload)),
			Header: http.Header{"Content-Type": []string{"image/png"}},
			Body:   io.NopCloser(strings.NewReader(string(payload))), Request: request,
		}
	})
	legacyTemp := filepath.Join(artworkDir, "art.jpg.tmp")
	outside := filepath.Join(t.TempDir(), "operator-state")
	if err := os.WriteFile(outside, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, legacyTemp); err != nil {
		t.Fatal(err)
	}

	prepared, err := loader.prepareDownload(context.Background(), "https://example.test/art", "art.jpg", "direct--art", "source:custom")
	if err != nil {
		t.Fatalf("prepareDownload() error = %v", err)
	}
	defer func() { _ = os.Remove(prepared.path) }()
	if filepath.Dir(prepared.path) != tempDir || prepared.filename != "art.png" ||
		prepared.originKey != "source:custom" || prepared.written != int64(len(payload)) {
		t.Fatalf("prepared download = %+v", prepared)
	}
	if info, err := os.Stat(prepared.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("temporary file mode = %v, %v; want 0600", info, err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "preserve me" {
		t.Fatalf("predictable temporary symlink was followed: %q, %v", got, err)
	}
}

func TestResolveDownloadNameUsesDetectedExtension(t *testing.T) {
	t.Parallel()
	loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.New(slog.DiscardHandler))
	response := &http.Response{Header: http.Header{"Content-Type": []string{"image/png"}}}
	if name := loader.resolveDownloadName(response, "https://example.test/same", "source.jpg"); name != "source.png" {
		t.Fatalf("resolveDownloadName() = %q, want source.png", name)
	}
}

type trackingReadCloser struct {
	io.Reader
	read   bool
	closed bool
}

func (r *trackingReadCloser) Read(buffer []byte) (int, error) {
	r.read = true
	return r.Reader.Read(buffer)
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}
