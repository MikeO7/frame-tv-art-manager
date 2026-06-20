// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "image/jpeg"
	_ "image/png"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	extJPG = ".jpg"
	extPNG = ".png"

	cmdPhoto  = "photo"
	cmdSearch = "search"

	bytesPerMB = 1 << 20
	// defaultDownloadCapBytes bounds a single download when no explicit
	// MAX_DOWNLOAD_SIZE_MB limit is configured, guarding against runaway bodies.
	defaultDownloadCapBytes = 100 * bytesPerMB

	userAgent = "FrameTVArtManager/1.0 (https://github.com/MikeO7/frame-tv-art-manager)"
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

	finalName, isNew, err := l.index.RegisterDownload(tmpPath, filename, identity)
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if !isNew {
		return false, nil
	}

	l.logger.Info("downloaded source image", "file", finalName, "size_bytes", written)
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

// SourceLoader downloads remote artwork into the local collection.
type SourceLoader interface {
	Sync() (downloaded int, err error)
}

// Ensure Loader implements SourceLoader.
var _ SourceLoader = (*Loader)(nil)

// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded.
func (l *Loader) Sync() (int, error) {
	if l.sourcesFile == "" {
		return 0, nil
	}

	urls, err := l.loadSources()
	if err != nil {
		return 0, err
	}

	if len(urls) == 0 {
		return 0, nil
	}

	l.logger.Info("processing image sources", "urls", len(urls))

	// Deduplicate URLs to avoid redundant processing.
	urls = deduplicateStrings(urls)

	// Build content index to avoid duplicates.
	l.index.Rebuild()

	l.index.ResetVisited()
	var downloaded int32
	var globalIndex int32 = 1

	var wg sync.WaitGroup
	// Concurrency limit: 5 source lines at once to avoid hitting rate limits too fast.
	semaphore := make(chan struct{}, 5)

	for _, line := range urls {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(lne string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			count, syncErr := l.syncLine(context.Background(), lne, &globalIndex)
			if syncErr != nil {
				l.logger.Warn("source resolve failed", "line", lne, "error", syncErr)
				return
			}

			if count > 0 {
				//nolint:gosec // per-line download count is bounded by MaxArtworkImages
				atomic.AddInt32(&downloaded, int32(count))
			}
		}(line)
	}
	wg.Wait()

	// Remove managed images that are no longer in sources.
	for _, filename := range l.index.UnusedManagedFiles() {
		l.logger.Info("removing unused source image", "file", filename)
		_ = os.Remove(filepath.Join(l.artworkDir, filename))
	}

	if downloaded > 0 {
		l.logger.Info("downloaded new source images", "count", downloaded)
	}

	return int(downloaded), nil
}

// syncLine resolves and downloads all images for one sources-file line.
func (l *Loader) syncLine(ctx context.Context, line string, globalIndex *int32) (int, error) {
	provider := l.resolveProvider(line)
	if provider == nil {
		return 0, fmt.Errorf("no provider found for line: %s", line)
	}

	images, err := provider.Resolve(ctx, line, globalIndex)
	if err != nil {
		return 0, err
	}

	var count int
	for _, img := range images {
		ok, dErr := l.downloadWithIdentity(ctx, img.URL, img.Identity)
		if dErr != nil {
			l.logger.Warn("source download failed", "url", img.URL, "error", dErr)
			continue
		}
		if !ok {
			continue
		}
		count++
		if img.OnDownload != nil {
			if hookErr := img.OnDownload(ctx); hookErr != nil {
				l.logger.Warn("post-download hook failed", "error", hookErr)
			}
		}
	}
	return count, nil
}

// loadSources reads the sources file (TXT or YAML) and returns a list of source strings.
func (l *Loader) loadSources() ([]string, error) {
	info, statErr := os.Stat(l.sourcesFile)
	if statErr == nil {
		if info.ModTime().Equal(l.lastSourcesModTime) && l.cachedUrls != nil {
			return l.cachedUrls, nil
		}
		l.lastSourcesModTime = info.ModTime()
	}

	var urls []string
	var err error
	ext := strings.ToLower(filepath.Ext(l.sourcesFile))
	if ext == ".yaml" || ext == ".yml" {
		urls, err = l.loadYamlSources()
	} else {
		urls, err = l.loadTxtSources()
	}

	if err == nil {
		l.cachedUrls = urls
	}
	return urls, err
}

func (l *Loader) loadTxtSources() ([]string, error) {
	f, err := os.Open(l.sourcesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
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
	return urls, scanner.Err()
}

func (l *Loader) loadYamlSources() ([]string, error) {
	data, err := os.ReadFile(l.sourcesFile)
	if err != nil {
		return nil, err
	}

	var urls []string

	// Try structured format with "providers" map (DRY)
	var dry struct {
		Providers map[string][]string `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &dry); err == nil && len(dry.Providers) > 0 {
		for provider, commands := range dry.Providers {
			for _, cmd := range commands {
				urls = append(urls, fmt.Sprintf("%s:%s", provider, cmd))
			}
		}
		return urls, nil
	}

	// Try to parse as a structured map with a "sources" key (classic list)
	var structured struct {
		Sources []string `yaml:"sources"`
	}
	if err := yaml.Unmarshal(data, &structured); err == nil && len(structured.Sources) > 0 {
		return structured.Sources, nil
	}

	// Try to parse as a simple list first
	var list []string
	if err := yaml.Unmarshal(data, &list); err == nil && len(list) > 0 {
		return list, nil
	}

	return nil, fmt.Errorf("invalid YAML sources format (expected 'providers:' map or 'sources:' list)")
}

// deduplicateStrings returns a new slice containing only unique strings from the input.
func deduplicateStrings(input []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range input {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
