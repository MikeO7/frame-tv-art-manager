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

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
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

func (l *Loader) checkExisting(identity string) bool {
	_, exists := l.sourceOrigins["source:"+identity]
	return exists
}

func (l *Loader) executeDownload(ctx context.Context, url, filename, identity string, originKeys ...string) (bool, error) {
	prepared, err := l.prepareDownload(ctx, url, filename, identity, originKeys...)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(prepared.path) }()

	item, isNew, err := l.importDownload(ctx, prepared.path, prepared.filename, prepared.originKey)
	if err != nil {
		return false, err
	}
	l.sourceOrigins[prepared.originKey] = struct{}{}
	if isNew {
		l.collectionSize++
		l.logger.Info("downloaded source image", "file", item.Name, "size_bytes", prepared.written)
	}
	return isNew, nil
}

func (l *Loader) prepareDownload(ctx context.Context, url, filename, identity string, originKeys ...string) (preparedDownload, error) {
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
	originKey := "source:" + identity
	if len(originKeys) > 0 {
		originKey = originKeys[0]
	}
	return preparedDownload{path: tmpPath, filename: filename, originKey: originKey, written: written}, nil
}

func (l *Loader) importDownload(
	ctx context.Context,
	path string,
	hint string,
	originKey string,
) (collection.Item, bool, error) {
	if l.importer == nil {
		return collection.Item{}, false, errors.New("source collection importer is required")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return collection.Item{}, false, fmt.Errorf("open validated source download: %w", err)
	}
	defer func() { _ = file.Close() }()
	maxBytes := int64(l.maxSizeMB) * bytesPerMB
	snapshot, err := l.importer.Import(ctx, collection.ImportRequest{
		Reader: file, Hint: hint, MaxBytes: maxBytes,
		Origin: collection.Origin{Key: originKey, Class: collection.OriginSource},
	})
	if err != nil {
		return collection.Item{}, false, fmt.Errorf("transactionally import source artwork: %w", err)
	}
	if len(snapshot.Changes) != 1 || snapshot.Changes[0].Name == "" {
		return collection.Item{}, false, errors.New("source import returned no committed item")
	}
	name := snapshot.Changes[0].Name
	for _, item := range snapshot.Items {
		if item.Name == name {
			return item, snapshot.Changes[0].Kind == collection.ChangeAdded, nil
		}
	}
	return collection.Item{}, false, errors.New("source import change is absent from committed snapshot")
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

//nolint:gocyclo // complete image validation keeps format and pixel facts in one trust boundary
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
	const maxDownloadedPixels int64 = 40_000_000
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
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind: %w", err)
	}
	decoded, decodedFormat, err := image.Decode(file)
	if err != nil {
		return fmt.Errorf("decode pixels: %w", err)
	}
	if decodedFormat != format || decoded.Bounds().Dx() != configuration.Width || decoded.Bounds().Dy() != configuration.Height {
		return errors.New("decoded image facts are inconsistent")
	}
	return nil
}

// fetch performs a validated HTTP GET, rejecting non-HTTP(S) schemes, non-200
// responses, and oversized Content-Length up front. On success it returns an
// open response whose body the caller must close; on error the body is closed.
func (l *Loader) fetch(ctx context.Context, url string) (*http.Response, error) {
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

// resolveDownloadName rewrites filename to match the response's image extension.
func (l *Loader) resolveDownloadName(resp *http.Response, url, filename string) string {
	ext := extensionFromResponse(resp, url)
	if ext == "" || strings.HasSuffix(filename, ext) {
		return filename
	}

	return strings.TrimSuffix(filename, filepath.Ext(filename)) + ext
}

// downloadToTemp streams the (already size-guarded) response body into a
// private temporary file outside the Artwork Collection and returns its path
// and byte count. Only the Collection Store can publish the final artwork.
func (l *Loader) downloadToTemp(resp *http.Response) (string, int64, error) {
	out, err := os.CreateTemp("", ".frame-tv-source-download-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := out.Name()

	maxBytes := int64(defaultDownloadCapBytes)
	if l.maxSizeMB > 0 {
		maxBytes = int64(l.maxSizeMB) * bytesPerMB
	}
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)

	written, copyErr := io.Copy(out, reader)
	chmodErr := out.Chmod(0o600)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("download body: %w", err)
	}
	return tmpPath, written, nil
}

func (l *Loader) downloadWithIdentity(ctx context.Context, url, identity string, originKeys ...string) (bool, error) {
	if l.maxImages > 0 && l.collectionSize >= l.maxImages {
		l.logger.Warn("global image limit reached, skipping download", "limit", l.maxImages)
		return false, nil
	}

	if l.checkExisting(identity) {
		return false, nil
	}

	filename := identity + ".jpg"
	return l.executeDownload(ctx, url, filename, identity, originKeys...)
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
