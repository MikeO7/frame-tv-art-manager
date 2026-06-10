// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/jpeg"
	_ "image/png"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

const (
	extJPG = ".jpg"
	extPNG = ".png"

	cmdPhoto  = "photo"
	cmdSearch = "search"
)

// Loader reads a sources file and downloads any images that aren't
// already present in the artwork directory.
type Loader struct {
	cfg                *config.Config
	sourcesFile        string
	artworkDir         string
	logger             *slog.Logger
	client             *http.Client
	providers          []SourceProvider
	maxImages          int
	maxSizeMB          int
	index              *ArtworkCatalog
	lastSourcesModTime time.Time
	cachedUrls         []string
}

// NewLoader creates a new sources loader from application config.
func NewLoader(cfg *config.Config, logger *slog.Logger, index *ArtworkCatalog) *Loader {
	if index == nil {
		index = NewArtworkCatalog(cfg.ArtworkDir, logger)
	}

	providers := []SourceProvider{
		newUnsplashProvider(cfg.UnsplashAppID, cfg.UnsplashAccessKey, cfg.UnsplashSecretKey, logger),
		newNasaProvider(cfg.NasaAPIKey, logger),
		newArticProvider(logger),
		newPexelsProvider(cfg.PexelsAPIKey, logger),
		newPixabayProvider(cfg.PixabayAPIKey, logger),
		newDirectProvider(),
	}

	return &Loader{
		cfg:         cfg,
		sourcesFile: cfg.SourcesFile,
		artworkDir:  cfg.ArtworkDir,
		maxImages:   cfg.MaxArtworkImages,
		maxSizeMB:   cfg.MaxDownloadSizeMB,
		logger:      logger,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		providers: providers,
		index:     index,
	}
}

// Provider returns a registered source provider by name.
func (l *Loader) Provider(name string) SourceProvider {
	for _, p := range l.providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// resolveProvider returns the first provider that can handle a source line.
func (l *Loader) resolveProvider(line string) SourceProvider {
	for _, p := range l.providers {
		if p.CanHandle(line) {
			return p
		}
	}
	return nil
}

func (l *Loader) checkExisting(identity string) (string, bool) {
	return l.index.LookupPrefix(identity)
}

//nolint:gocyclo,funlen,gocognit // complexity justified for this domain-specific path
func (l *Loader) executeDownload(ctx context.Context, url, filename string) (bool, error) {
	destPath := filepath.Join(l.artworkDir, filename)
	l.logger.Info("downloading source image", "url", truncateURL(url), "file", filename)

	parsedURL, err := neturl.Parse(url)
	if err != nil {
		return false, fmt.Errorf("invalid url format: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return false, fmt.Errorf("unsupported url scheme: %s", parsedURL.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "FrameTVArtManager/1.0 (https://github.com/MikeO7/frame-tv-art-manager)")

	resp, err := l.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("HTTP GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d from %s", resp.StatusCode, truncateURL(url))
	}

	if l.maxSizeMB > 0 {
		maxBytes := int64(l.maxSizeMB) * 1024 * 1024
		if size := resp.ContentLength; size > maxBytes {
			return false, fmt.Errorf("file too large: %d bytes (limit %d MB)", size, l.maxSizeMB)
		}
		resp.Body = http.MaxBytesReader(nil, resp.Body, maxBytes)
	}

	ext := extensionFromResponse(resp, url)
	if ext != "" && !strings.HasSuffix(filename, ext) {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ext
		destPath = filepath.Join(l.artworkDir, filename)

		if existing, ok := l.checkExisting(filename); ok {
			l.index.MarkVisited(existing)
			return false, nil
		}
	}

	tmpPath := destPath + ".tmp"
	out, err := os.OpenFile(filepath.Clean(tmpPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}

	maxBytes := int64(100 * 1024 * 1024)
	if l.maxSizeMB > 0 {
		maxBytes = int64(l.maxSizeMB) * 1024 * 1024
	}
	reader := http.MaxBytesReader(nil, resp.Body, maxBytes)

	written, err := io.Copy(out, reader)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("download body: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename temp file: %w", err)
	}

	finalName, ok := l.finalizeDownload(destPath, filename)
	if !ok {
		return false, nil
	}

	l.index.MarkVisited(finalName)
	_ = os.Chmod(filepath.Join(l.artworkDir, finalName), 0o600)

	l.logger.Info("downloaded source image", "file", finalName, "size_bytes", written)
	return true, nil
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
	return l.executeDownload(ctx, url, filename)
}

func (l *Loader) finalizeDownload(path, filename string) (string, bool) {
	hash, err := fileHash(path)
	if err != nil {
		l.logger.Warn("failed to hash downloaded file", "file", filename, "error", err)
		return filename, true
	}

	if existing, duplicate := l.index.RegisterHash(hash, filename); duplicate {
		if existing != filename {
			l.logger.Info("discarding duplicate content", "file", filename, "matches", existing)
			_ = os.Remove(path)
			l.index.MarkVisited(existing)
			return existing, false
		}
	}

	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)
	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	}

	finalName := artwork.BuildHashName(identity, hash[:12], ext)
	finalPath := filepath.Join(l.artworkDir, finalName)

	if err := os.Rename(path, finalPath); err != nil {
		l.logger.Warn("failed to rename to hash-based name", "file", filename, "error", err)
		l.index.SetHash(hash, filename)
		return filename, true
	}

	l.index.SetHash(hash, finalName)
	return finalName, true
}

func (l *Loader) urlToSlug(url string) string {
	return URLToSlug(url)
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

// SourceLoader downloads remote artwork into the local collection.
type SourceLoader interface {
	Sync() (downloaded int, err error)
}

// Ensure Loader implements SourceLoader.
var _ SourceLoader = (*Loader)(nil)
