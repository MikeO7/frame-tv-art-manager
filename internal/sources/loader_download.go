package sources

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	sourceMediaTypeJPEG = "image/jpeg"
	sourceMediaTypePNG  = "image/png"
)

type preparedDownload struct {
	path      string
	filename  string
	originKey string
	written   int64
}

func (l *loader) checkExisting(identity string) bool {
	_, exists := l.sourceOrigins["source:"+identity]
	return exists
}

// prepareImage downloads the preferred rendition, falling back to the next
// one whenever a candidate cannot be fetched or fails validation (for example
// because it exceeds the size or pixel caps).
func (l *loader) prepareImage(ctx context.Context, img sourceImage) (preparedDownload, error) {
	var failures []error
	for _, candidate := range append([]string{img.URL}, img.Fallbacks...) {
		prepared, err := l.prepareDownload(ctx, candidate, img.Identity+extJPG, img.Identity)
		if err == nil {
			return prepared, nil
		}
		if ctx.Err() != nil {
			return preparedDownload{}, fmt.Errorf("download %s: %w", truncateURL(candidate), ctx.Err())
		}
		failures = append(failures, fmt.Errorf("download %s: %w", truncateURL(candidate), err))
	}
	return preparedDownload{}, errors.Join(failures...)
}

func (l *loader) prepareDownload(ctx context.Context, url, filename, identity string) (preparedDownload, error) {
	l.logger.Info("downloading source image", "url", truncateURL(url), "file", filename)

	resp, err := l.fetch(ctx, url)
	if err != nil {
		return preparedDownload{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := validateSourceContentType(resp.Header.Get("Content-Type")); err != nil {
		return preparedDownload{}, err
	}

	filename = l.resolveDownloadName(resp, url, filename)

	tmpPath, written, err := l.downloadToTemp(resp)
	if err != nil {
		return preparedDownload{}, err
	}

	if resp.ContentLength > 0 && written != resp.ContentLength {
		_ = os.Remove(tmpPath)
		return preparedDownload{}, fmt.Errorf("incomplete download: expected %d bytes, got %d", resp.ContentLength, written)
	}
	if err := validateDownloadedImage(tmpPath, filepath.Ext(filename)); err != nil {
		_ = os.Remove(tmpPath)
		return preparedDownload{}, fmt.Errorf("validate downloaded image: %w", err)
	}
	return preparedDownload{path: tmpPath, filename: filename, originKey: "source:" + identity, written: written}, nil
}

func validateSourceContentType(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return fmt.Errorf("parse source content type: %w", err)
	}
	if strings.HasPrefix(mediaType, "image/") && mediaType != sourceMediaTypeJPEG && mediaType != sourceMediaTypePNG {
		return fmt.Errorf("unsupported source image format %q", strings.TrimPrefix(mediaType, "image/"))
	}
	return nil
}

// validateDownloadedImage checks the header of a download: its format must
// match its filename and its dimensions must be within the pixel limit. The
// Collection Store decodes every pixel before it publishes the artwork, so the
// pixels are not decoded here as well.
func validateDownloadedImage(path, extension string) error {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = file.Close() }()
	configuration, format, err := image.DecodeConfig(file)
	if err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	if configuration.Width <= 0 || configuration.Height <= 0 ||
		int64(configuration.Width) > maxDownloadedPixels/int64(configuration.Height) {
		return fmt.Errorf("dimensions %dx%d exceed safety limit", configuration.Width, configuration.Height)
	}
	wantFormat := "jpeg"
	if strings.EqualFold(extension, extPNG) {
		wantFormat = "png"
	}
	if format != wantFormat {
		return fmt.Errorf("%s content does not match %s filename", format, extension)
	}
	return nil
}

// fetch performs a validated HTTP GET, rejecting non-HTTP(S) schemes, non-200
// responses, and oversized Content-Length up front. On success it returns an
// open response whose body is limited to the download size and which the
// caller must close; on error the body is closed.
func (l *loader) fetch(ctx context.Context, url string) (*http.Response, error) {
	parsedURL, err := neturl.Parse(url)
	if err != nil {
		return nil, errors.New("invalid source URL format")
	}
	if parsedURL.Scheme != schemeHTTP && parsedURL.Scheme != schemeHTTPS {
		return nil, errors.New("unsupported URL scheme")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, providerRequestError(ctx, "create source download request", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, providerRequestError(ctx, "source download", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, truncateURL(url))
	}

	maxBytes := int64(l.maxSizeMB) * bytesPerMB
	if size := resp.ContentLength; size > maxBytes {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("file too large: %d bytes (limit %d MB)", size, l.maxSizeMB)
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, maxBytes)

	return resp, nil
}

// resolveDownloadName rewrites filename to match the response's image extension.
func (l *loader) resolveDownloadName(resp *http.Response, url, filename string) string {
	ext := extensionFromResponse(resp, url)
	if ext == "" || strings.HasSuffix(filename, ext) {
		return filename
	}

	return strings.TrimSuffix(filename, filepath.Ext(filename)) + ext
}

// downloadToTemp streams the (already size-guarded) response body into a
// private temporary file outside the Artwork Collection and returns its path
// and byte count. Only the Collection Store can publish the final artwork.
func (l *loader) downloadToTemp(resp *http.Response) (string, int64, error) {
	out, err := os.CreateTemp("", sourceDownloadPattern)
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := out.Name()

	written, copyErr := io.Copy(out, resp.Body)
	chmodErr := out.Chmod(0o600)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("download body: %w", err)
	}
	return tmpPath, written, nil
}

func extensionFromResponse(resp *http.Response, url string) string {
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, sourceMediaTypeJPEG):
		return extJPG
	case strings.Contains(ct, sourceMediaTypePNG):
		return extPNG
	}

	ext := strings.ToLower(filepath.Ext(strings.Split(url, "?")[0]))
	switch ext {
	case extJPG, ".jpeg", extPNG:
		return ext
	}

	return extJPG
}

func truncateURL(url string) string {
	parsed, err := neturl.Parse(url)
	if err != nil || parsed.Host == "" || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) {
		return "<invalid-url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	safeURL := parsed.String()
	if len(safeURL) > 80 {
		return safeURL[:77] + "..."
	}
	return safeURL
}
