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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "image/jpeg"
	_ "image/png"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	extJPG = ".jpg"
	extPNG = ".png"

	cmdPhoto  = "photo"
	cmdSearch = "search"
)

var reManagedIndex = regexp.MustCompile(`^[0-9]{3}__`)

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
	index              *ArtworkIndex
	lastSourcesModTime time.Time
	cachedUrls         []string
}

// NewLoader creates a new sources loader from application config.
func NewLoader(cfg *config.Config, logger *slog.Logger, index *ArtworkIndex) *Loader {
	if index == nil {
		index = NewArtworkIndex(cfg.ArtworkDir, logger)
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

func (l *Loader) checkExisting(identity string) (string, bool) {
	return l.index.LookupPrefix(identity)
}

//nolint:gocyclo,funlen // complexity justified for this domain-specific path
func (l *Loader) executeDownload(ctx context.Context, url, filename string) (bool, error) {
	destPath := filepath.Join(l.artworkDir, filename)
	l.logger.Info("downloading source image", "url", truncateURL(url), "file", filename)

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

	// Check file size.
	if l.maxSizeMB > 0 {
		maxBytes := int64(l.maxSizeMB) * 1024 * 1024
		if size := resp.ContentLength; size > maxBytes {
			return false, fmt.Errorf("file too large: %d bytes (limit %d MB)", size, l.maxSizeMB)
		}
		// Wrap body to prevent DOS from oversized files.
		resp.Body = http.MaxBytesReader(nil, resp.Body, maxBytes)
	}

	// Determine extension and potential new path.
	ext := extensionFromResponse(resp, url)
	if ext != "" && !strings.HasSuffix(filename, ext) {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ext
		destPath = filepath.Join(l.artworkDir, filename)

		// Re-check by identity prefix.
		if existing, ok := l.checkExisting(filename); ok {
			l.index.MarkVisited(existing)
			return false, nil
		}
	}

	// Write to temp file then rename for atomicity.
	tmpPath := destPath + ".tmp"
	out, err := os.Create(filepath.Clean(tmpPath))
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}

	// Prevent DoS / resource exhaustion by enforcing a maximum read size, using MaxBytesReader
	maxBytes := int64(100 * 1024 * 1024) // 100 MB default hard limit
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

// downloadWithIdentity is a helper that handles the full download, hashing,
// and indexing flow for a given identity.
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

// finalizeDownload checks for content duplicates, renames the file to include
// the hash, and updates the index. Returns the final filename and true if the
// file should be kept.
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

// truncateURL shortens a URL for logging readability.
func truncateURL(url string) string {
	if len(url) > 80 {
		return url[:77] + "..."
	}
	return url
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
