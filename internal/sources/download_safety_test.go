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

func TestExecuteDownloadRejectsTruncatedResponseAndRemovesTemporaryFile(t *testing.T) {
	artworkDir := t.TempDir()
	loader := newTestLoader(&config.Config{
		ArtworkDir:        artworkDir,
		MaxDownloadSizeMB: 1,
	}, slog.Default())
	loader.client = &http.Client{Transport: downloadRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: 12,
			Header:        http.Header{"Content-Type": []string{"image/jpeg"}},
			Body:          io.NopCloser(strings.NewReader("short")),
			Request:       request,
		}, nil
	})}

	downloaded, err := loader.executeDownload(
		context.Background(),
		"https://example.test/truncated.jpg",
		"001__direct__truncated.jpg",
		"001__direct__truncated",
	)
	if err == nil || !strings.Contains(err.Error(), "incomplete download") {
		t.Fatalf("executeDownload() error = %v, want incomplete download", err)
	}
	if downloaded {
		t.Fatal("executeDownload() reported a truncated response as downloaded")
	}
	entries, readErr := os.ReadDir(artworkDir)
	if readErr != nil {
		t.Fatalf("read artwork directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("truncated download left files behind: %v", entries)
	}
}

func TestExecuteDownloadRejectsNonImageAndRemovesTemporaryFile(t *testing.T) {
	artworkDir := t.TempDir()
	loader := newTestLoader(&config.Config{ArtworkDir: artworkDir, MaxDownloadSizeMB: 1}, slog.Default())
	loader.client = &http.Client{Transport: downloadRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<html>not artwork</html>")),
			Request:    request,
		}, nil
	})}
	downloaded, err := loader.executeDownload(
		context.Background(), "https://example.test/art", "001__direct__art.jpg", "001__direct__art",
	)
	if err == nil || downloaded {
		t.Fatalf("executeDownload() = %t, %v; want validation failure", downloaded, err)
	}
	entries, readErr := os.ReadDir(artworkDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("invalid download left files behind: %v, %v", entries, readErr)
	}
}

func TestExecuteDownloadRejectsExplicitUnsupportedImageFormatBeforeReadingBody(t *testing.T) {
	loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir(), MaxDownloadSizeMB: 1}, slog.Default())
	body := &trackingReadCloser{Reader: strings.NewReader("webp")}
	loader.client = &http.Client{Transport: downloadRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/webp"}},
			Body: body, Request: request,
		}, nil
	})}
	downloaded, err := loader.executeDownload(
		context.Background(), "https://example.test/art.webp", "001__direct__art.jpg", "001__direct__art",
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported source image format") || downloaded {
		t.Fatalf("executeDownload() = %t, %v", downloaded, err)
	}
	if body.read || !body.closed {
		t.Fatalf("unsupported response body read=%v closed=%v", body.read, body.closed)
	}
}

func TestResolveDownloadNameUsesDetectedExtension(t *testing.T) {
	loader := newTestLoader(&config.Config{ArtworkDir: t.TempDir()}, slog.Default())
	response := &http.Response{Header: http.Header{"Content-Type": []string{"image/png"}}}
	name := loader.resolveDownloadName(response, "https://example.test/same", "source.jpg")
	if name != "source.png" {
		t.Fatalf("resolveDownloadName() = %q, want source.png", name)
	}
}

func TestExecuteDownloadNeverFollowsPredictableTemporarySymlink(t *testing.T) {
	artworkDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "operator-state")
	if err := os.WriteFile(outside, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyTemp := filepath.Join(artworkDir, "001__direct__safe.jpg.tmp")
	if err := os.Symlink(outside, legacyTemp); err != nil {
		t.Fatal(err)
	}
	loader := newTestLoader(&config.Config{ArtworkDir: artworkDir, MaxDownloadSizeMB: 1}, slog.Default())
	payload := testJPEGBytes(t)
	loader.client = &http.Client{Transport: downloadRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: int64(len(payload)),
			Header: http.Header{"Content-Type": []string{"image/jpeg"}},
			Body:   io.NopCloser(strings.NewReader(string(payload))), Request: request,
		}, nil
	})}

	downloaded, err := loader.executeDownload(
		context.Background(), "https://example.test/safe.jpg", "001__direct__safe.jpg", "001__direct__safe",
	)
	if err != nil || !downloaded {
		t.Fatalf("executeDownload() = %t, %v", downloaded, err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "preserve me" {
		t.Fatalf("symlink target = %q, %v", got, err)
	}
	info, err := os.Lstat(legacyTemp)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("legacy symlink changed: %v, %v", info, err)
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
