// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	extJPG = ".jpg"
	extPNG = ".png"
)

// Loader reads a sources file and downloads any images that aren't
// already present in the artwork directory.
type Loader struct {
	sourcesFile string
	artworkDir  string
	logger      *slog.Logger
	client      *http.Client
}

// NewLoader creates a new sources loader.
func NewLoader(sourcesFile, artworkDir string, logger *slog.Logger) *Loader {
	return &Loader{
		sourcesFile: sourcesFile,
		artworkDir:  artworkDir,
		logger:      logger,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded (matched by URL hash filename).
//
// Sources file format (one URL per line):
//
//	# Lines starting with # are comments
//	https://example.com/photo.jpg
//	https://images.unsplash.com/photo-abc123?w=3840
//
// Downloaded files are named by SHA256 hash of the URL to enable
// idempotent re-runs.
func (l *Loader) Sync() (int, error) {
	if l.sourcesFile == "" {
		return 0, nil
	}

	f, err := os.Open(l.sourcesFile)
	if err != nil {
		if os.IsNotExist(err) {
			l.logger.Debug("no sources file found", "path", l.sourcesFile)
			return 0, nil
		}
		return 0, fmt.Errorf("open sources file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read sources file: %w", err)
	}

	l.logger.Info("processing image sources", "urls", len(urls))

	downloaded := 0
	for _, url := range urls {
		ok, err := l.downloadIfNew(url)
		if err != nil {
			l.logger.Warn("failed to download source image",
				"url", truncateURL(url),
				"error", err,
			)
			continue
		}
		if ok {
			downloaded++
		}
	}

	if downloaded > 0 {
		l.logger.Info("downloaded new source images", "count", downloaded)
	}

	return downloaded, nil
}

// downloadIfNew downloads a URL if the corresponding file doesn't
// already exist. Returns true if a new file was downloaded.
func (l *Loader) downloadIfNew(url string) (bool, error) {
	filename := l.urlToFilename(url)
	destPath := filepath.Join(l.artworkDir, filename)

	// Skip if already downloaded.
	if _, err := os.Stat(destPath); err == nil {
		return false, nil
	}

	l.logger.Info("downloading source image",
		"url", truncateURL(url),
		"file", filename,
	)

	resp, err := l.client.Get(url)
	if err != nil {
		return false, fmt.Errorf("HTTP GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d from %s", resp.StatusCode, truncateURL(url))
	}

	// Determine extension from Content-Type or URL.
	ext := extensionFromResponse(resp, url)

	// Update filename with correct extension if needed.
	if ext != "" && !strings.HasSuffix(filename, ext) {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ext
		destPath = filepath.Join(l.artworkDir, filename)

		// Re-check with correct extension.
		if _, err := os.Stat(destPath); err == nil {
			return false, nil
		}
	}

	// Write to temp file then rename for atomicity.
	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}

	written, err := io.Copy(out, resp.Body)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("download body: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename temp file: %w", err)
	}

	// Ensure inclusive permissions for Mac access.
	_ = os.Chmod(destPath, 0644) //nosec G302

	l.logger.Info("downloaded source image",
		"file", filename,
		"size_bytes", written,
	)

	return true, nil
}

// urlToFilename generates a deterministic filename from a URL using
// SHA256 hashing. This ensures idempotent downloads.
func (l *Loader) urlToFilename(url string) string {
	hash := sha256.Sum256([]byte(url))
	return fmt.Sprintf("src_%x%s", hash[:8], extJPG) // 16 hex chars
}

// extensionFromResponse determines the file extension from the HTTP
// Content-Type header or URL path.
func extensionFromResponse(resp *http.Response, url string) string {
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "image/jpeg"):
		return extJPG
	case strings.Contains(ct, "image/png"):
		return extPNG
	case strings.Contains(ct, "image/webp"):
		return extJPG // TV doesn't support webp, caller will need to convert
	}

	// Fall back to URL extension.
	ext := strings.ToLower(filepath.Ext(strings.Split(url, "?")[0]))
	switch ext {
	case extJPG, ".jpeg", extPNG:
		return ext
	}

	return extJPG // default
}

// truncateURL shortens a URL for logging readability.
func truncateURL(url string) string {
	if len(url) > 80 {
		return url[:77] + "..."
	}
	return url
}
