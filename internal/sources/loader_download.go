package sources

import (
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

func (l *Loader) checkExisting(identity string) (string, bool) {
	return l.index.LookupPrefix(identity)
}

func (l *Loader) executeDownload(ctx context.Context, url, filename, identity string) (bool, error) {
	l.logger.Info("downloading source image", "url", truncateURL(url), "file", filename)

	resp, err := l.fetch(ctx, url)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	filename, skip := l.resolveDownloadName(resp, url, filename)
	if skip {
		return false, nil
	}

	tmpPath, written, err := l.downloadToTemp(resp, filename)
	if err != nil {
		return false, err
	}

	if resp.ContentLength > 0 && written != resp.ContentLength {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("incomplete download: expected %d bytes, got %d", resp.ContentLength, written)
	}

	finalName, isNew, err := l.index.RegisterDownload(tmpPath, filename, identity)
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if !isNew {
		return false, nil
	}

	l.logger.Info("downloaded source image", "file", finalName, "size_bytes", written)

	optCfg := l.cfg.OptimizeOptions()
	if optCfg.Enabled {
		finalPath := filepath.Join(l.artworkDir, finalName)
		optName, modified, optErr := optimize.OptimizeFile(finalPath, optCfg, l.logger)
		if optErr != nil {
			l.logger.Warn("post-download optimization failed", "file", finalName, "error", optErr)
		} else if modified && optName != finalName {
			l.index.NoteFileRename(finalName, optName)
		}
	}

	return true, nil
}

// fetch performs a validated HTTP GET, rejecting non-HTTP(S) schemes, non-200
// responses, and oversized Content-Length up front. On success it returns an
// open response whose body the caller must close; on error the body is closed.
func (l *Loader) fetch(ctx context.Context, url string) (*http.Response, error) {
	parsedURL, err := neturl.Parse(url)
	if err != nil {
		return nil, fmt.Errorf("invalid url format: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported url scheme: %s", parsedURL.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, truncateURL(url))
	}

	if l.maxSizeMB > 0 {
		maxBytes := int64(l.maxSizeMB) * bytesPerMB
		if size := resp.ContentLength; size > maxBytes {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("file too large: %d bytes (limit %d MB)", size, l.maxSizeMB)
		}
		resp.Body = http.MaxBytesReader(nil, resp.Body, maxBytes)
	}

	return resp, nil
}

// resolveDownloadName rewrites filename to match the response's image
// extension and reports whether the re-extended file already exists (skip).
func (l *Loader) resolveDownloadName(resp *http.Response, url, filename string) (string, bool) {
	ext := extensionFromResponse(resp, url)
	if ext == "" || strings.HasSuffix(filename, ext) {
		return filename, false
	}

	filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ext
	if existing, ok := l.checkExisting(filename); ok {
		l.index.MarkVisited(existing)
		return filename, true
	}
	return filename, false
}

// downloadToTemp streams the (already size-guarded) response body into a
// temporary file in the artwork directory and returns its path and byte count.
func (l *Loader) downloadToTemp(resp *http.Response, filename string) (string, int64, error) {
	tmpPath := filepath.Join(l.artworkDir, filename+".tmp")
	// 0o644 is intentional — artwork files must be world-readable so they
	// can be accessed over SMB/NFS network shares. Do NOT tighten to 0o600.
	out, err := os.OpenFile(filepath.Clean(tmpPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}

	maxBytes := int64(defaultDownloadCapBytes)
	if l.maxSizeMB > 0 {
		maxBytes = int64(l.maxSizeMB) * bytesPerMB
	}
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)

	written, err := io.Copy(out, reader)
	_ = out.Close()
	// Explicit chmod to 0o644 is required to override restrictive system umasks (e.g. 0077)
	// so files are readable over SMB/NFS network shares. Do NOT tighten to 0o600.
	_ = os.Chmod(tmpPath, 0o644)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("download body: %w", err)
	}
	return tmpPath, written, nil
}

func (l *Loader) downloadWithIdentity(ctx context.Context, url, identity string) (bool, error) {
	if l.index.MaxReached(l.maxImages) {
		l.logger.Warn("global image limit reached, skipping download", "limit", l.maxImages)
		return false, nil
	}

	if existing, ok := l.checkExisting(identity); ok {
		l.index.MarkVisited(existing)
		return false, nil
	}

	filename := identity + ".jpg"
	return l.executeDownload(ctx, url, filename, identity)
}

func extensionFromResponse(resp *http.Response, url string) string {
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "image/jpeg"):
		return extJPG
	case strings.Contains(ct, "image/png"):
		return extPNG
	case strings.Contains(ct, "image/webp"):
		return extJPG
	}

	ext := strings.ToLower(filepath.Ext(strings.Split(url, "?")[0]))
	switch ext {
	case extJPG, ".jpeg", extPNG:
		return ext
	}

	return extJPG
}

func truncateURL(url string) string {
	if len(url) > 80 {
		return url[:77] + "..."
	}
	return url
}
