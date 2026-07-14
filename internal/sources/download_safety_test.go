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

func TestExecuteDownloadSkipsExistingReextendedIdentityWithoutNetworkBodyRead(t *testing.T) {
	artworkDir := t.TempDir()
	loader := newTestLoader(&config.Config{ArtworkDir: artworkDir}, slog.Default())
	existingName := "direct__same.h_existing.png"
	loader.index.prefixMap["direct__same"] = existingName
	loader.index.catalog[existingName] = struct{}{}
	if err := os.WriteFile(filepath.Join(artworkDir, existingName), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed existing artwork: %v", err)
	}
	body := &trackingReadCloser{Reader: strings.NewReader("replacement")}
	loader.client = &http.Client{Transport: downloadRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       body,
			Request:    request,
		}, nil
	})}

	downloaded, err := loader.executeDownload(
		context.Background(),
		"https://example.test/same",
		"001__direct__same.jpg",
		"001__direct__same",
	)
	if err != nil {
		t.Fatalf("executeDownload() error = %v", err)
	}
	if downloaded {
		t.Fatal("executeDownload() replaced an existing re-extended identity")
	}
	if body.read {
		t.Fatal("executeDownload() consumed the response body after proving the identity exists")
	}
	if !body.closed {
		t.Fatal("executeDownload() did not close the skipped response body")
	}
	contents, err := os.ReadFile(filepath.Join(artworkDir, existingName))
	if err != nil {
		t.Fatalf("read existing artwork: %v", err)
	}
	if string(contents) != "existing" {
		t.Fatalf("existing artwork was modified: %q", contents)
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
