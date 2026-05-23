// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
	unsplash           *unsplashClient
	nasa               *nasaClient
	artic              *articClient
	pexels             *pexelsClient
	pixabay            *pixabayClient
	providers          []SourceProvider
	maxImages          int
	maxSizeMB          int
	index              map[string]string // hash -> filename (content deduplication)
	prefixMap          map[string]string // prefix -> filename (idempotency check)
	visited            map[string]bool   // filename -> true (cleanup tracking)
	lastDirModTime     time.Time
	lastSourcesModTime time.Time
	cachedUrls         []string
	mu                 sync.Mutex // Protects index, prefixMap, and visited
}

// NewLoader creates a new sources loader from application config.
func NewLoader(cfg *config.Config, logger *slog.Logger) *Loader {
	unsplash := newUnsplashProvider(cfg.UnsplashAppID, cfg.UnsplashAccessKey, cfg.UnsplashSecretKey, logger)
	nasa := newNasaProvider(cfg.NasaAPIKey, logger)
	artic := newArticProvider(logger)
	pexels := newPexelsProvider(cfg.PexelsAPIKey, logger)
	pixabay := newPixabayProvider(cfg.PixabayAPIKey, logger)

	l := &Loader{
		cfg:         cfg,
		sourcesFile: cfg.SourcesFile,
		artworkDir:  cfg.ArtworkDir,
		maxImages:   cfg.MaxArtworkImages,
		maxSizeMB:   cfg.MaxDownloadSizeMB,
		logger:      logger,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		unsplash:  unsplash,
		nasa:      nasa,
		artic:     artic,
		pexels:    pexels,
		pixabay:   pixabay,
		index:     make(map[string]string),
		prefixMap: make(map[string]string),
		visited:   make(map[string]bool),
	}

	l.providers = []SourceProvider{
		unsplash,
		nasa,
		artic,
		pexels,
		pixabay,
		newDirectProvider(), // DirectProvider is fallback, matches all direct URLs
	}

	return l
}

// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded.
//
//nolint:gocognit
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
	l.buildContentIndex()

	l.visited = make(map[string]bool)
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

			var provider SourceProvider
			for _, p := range l.providers {
				if p.CanHandle(lne) {
					provider = p
					break
				}
			}

			if provider == nil {
				l.logger.Error("no provider found for line", "line", lne)
				return
			}

			images, resolveErr := provider.Resolve(context.Background(), lne, &globalIndex)
			if resolveErr != nil {
				l.logger.Warn("source resolve failed", "line", lne, "error", resolveErr)
				return
			}

			var count int
			for _, img := range images {
				ok, dErr := l.downloadWithIdentity(img.URL, img.Identity)
				if dErr != nil {
					l.logger.Warn("source download failed", "url", img.URL, "error", dErr)
					continue
				}
				if ok {
					count++
					if img.OnDownload != nil {
						if hookErr := img.OnDownload(context.Background()); hookErr != nil {
							l.logger.Warn("post-download hook failed", "error", hookErr)
						}
					}
				}
			}

			if count > 0 {
				atomic.AddInt32(&downloaded, int32(count)) //nolint:gosec
			}
		}(line)
	}
	wg.Wait()

	// Remove managed images that are no longer in sources.
	l.cleanupUnusedSources()

	if downloaded > 0 {
		l.logger.Info("downloaded new source images", "count", downloaded)
	}

	return int(downloaded), nil
}

// handleArticLine is a backwards-compatible wrapper for testing.
func (l *Loader) handleArticLine(line string, globalIndex *int32) (int, error) {
	var provider SourceProvider
	for _, p := range l.providers {
		if p.CanHandle(line) {
			provider = p
			break
		}
	}
	if provider == nil {
		return 0, fmt.Errorf("no provider found for line: %s", line)
	}
	images, err := provider.Resolve(context.Background(), line, globalIndex)
	if err != nil {
		return 0, err
	}
	var count int
	for _, img := range images {
		ok, dErr := l.downloadWithIdentity(img.URL, img.Identity)
		if dErr == nil && ok {
			count++
		}
	}
	return count, nil
}

func (l *Loader) checkExisting(identity string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	existing, ok := l.prefixMap[stripIndexPrefix(identity)]
	return existing, ok
}

// stripIndexPrefix removes the non-deterministic numeric prefix (e.g. "001__") for stable idempotency.
func stripIndexPrefix(identity string) string {
	if len(identity) > 5 && identity[3:5] == "__" {
		return identity[5:]
	}
	return identity
}

func (l *Loader) executeDownload(url, filename string) (bool, error) {
	destPath := filepath.Join(l.artworkDir, filename)
	l.logger.Info("downloading source image", "url", truncateURL(url), "file", filename)

	req, err := http.NewRequest(http.MethodGet, url, nil)
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
			l.mu.Lock()
			l.visited[existing] = true
			l.mu.Unlock()
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

	l.mu.Lock()
	l.visited[finalName] = true
	l.mu.Unlock()
	_ = os.Chmod(filepath.Join(l.artworkDir, finalName), 0o600)

	l.logger.Info("downloaded source image", "file", finalName, "size_bytes", written)
	return true, nil
}

// downloadWithIdentity is a helper that handles the full download, hashing,
// and indexing flow for a given identity.
func (l *Loader) downloadWithIdentity(url, identity string) (bool, error) {
	l.mu.Lock()
	limitReached := l.maxImages > 0 && len(l.visited) >= l.maxImages
	l.mu.Unlock()
	if limitReached {
		l.logger.Warn("global image limit reached, skipping download", "limit", l.maxImages)
		return false, nil
	}

	if existing, ok := l.checkExisting(identity); ok {
		l.mu.Lock()
		l.visited[existing] = true
		l.mu.Unlock()
		return false, nil
	}

	filename := identity + ".jpg"
	return l.executeDownload(url, filename)
}

// finalizeDownload checks for content duplicates, renames the file to include
// the hash, and updates the index. Returns the final filename and true if the
// file should be kept.
func (l *Loader) finalizeDownload(path, filename string) (string, bool) {
	hash, err := l.fileHash(path)
	if err != nil {
		// If hashing fails, we keep the file with its current name but log it.
		l.logger.Warn("failed to hash downloaded file", "file", filename, "error", err)
		return filename, true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if existing, ok := l.index[hash]; ok {
		if existing != filename {
			l.logger.Info("discarding duplicate content", "file", filename, "matches", existing)
			_ = os.Remove(path)
			l.visited[existing] = true
			return existing, false
		}
	}

	// Rename to include hash for future sync cycles.
	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	// Strip old .h_ separator if any.
	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	}

	finalName := fmt.Sprintf("%s__%s%s", identity, hash[:12], ext)
	finalPath := filepath.Join(l.artworkDir, finalName)

	if err := os.Rename(path, finalPath); err != nil {
		l.logger.Warn("failed to rename to hash-based name", "file", filename, "error", err)
		l.index[hash] = filename
		return filename, true
	}

	l.index[hash] = finalName
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

type job struct {
	filename string
}

type indexResult struct {
	filename      string
	hash          string
	cleanIdentity string
	identity      string
	err           error
}

// buildContentIndex hashes all existing files in the artwork directory
// to enable deduplication and fast syncs.
func (l *Loader) buildContentIndex() {
	info, statErr := os.Stat(l.artworkDir)
	if statErr == nil {
		if info.ModTime().Equal(l.lastDirModTime) && l.index != nil {
			return
		}
		l.lastDirModTime = info.ModTime()
	}

	l.index = make(map[string]string)
	l.prefixMap = make(map[string]string)

	entries, err := os.ReadDir(l.artworkDir)
	if err != nil {
		return
	}

	jobs := make(chan job, len(entries))
	results := make(chan indexResult, len(entries))

	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 16 {
		numWorkers = 16
	}

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				res := l.processSingleFile(j.filename)
				results <- res
			}
		}()
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		jobs <- job{filename: entry.Name()}
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if res.err != nil {
			continue
		}

		filename := res.filename
		path := filepath.Join(l.artworkDir, filename)
		hash := res.hash
		identity := res.identity
		cleanIdentity := res.cleanIdentity

		l.mu.Lock()
		mapIdentity := stripIndexPrefix(cleanIdentity)
		l.prefixMap[mapIdentity] = filename
		l.mu.Unlock()

		// If the filename didn't contain the hash, rename it now
		if !strings.Contains(filename, ".h_"+hash[:12]) && !strings.Contains(filename, "__"+hash[:12]) {
			ext := filepath.Ext(filename)
			newName := identity + ".h_" + hash[:12] + ext
			newPath := filepath.Join(l.artworkDir, newName)
			if err := os.Rename(path, newPath); err == nil {
				filename = newName
				path = newPath
				l.mu.Lock()
				l.prefixMap[mapIdentity] = filename
				l.mu.Unlock()
			}
			l.logger.Debug("migrated file to hash-based name", "original", identity, "hash", hash[:12])
		}

		l.mu.Lock()
		if existing, ok := l.index[hash]; ok {
			l.logger.Info("found existing duplicate content, removing", "file", filename, "matches", existing)
			_ = os.Remove(path)
		} else {
			l.index[hash] = filename
		}
		l.mu.Unlock()
	}
}

func (l *Loader) processSingleFile(filename string) indexResult {
	path := filepath.Join(l.artworkDir, filename)
	identity, cleanIdentity, hash := parseFileIdentity(filename)

	if hash == "" {
		h, err := l.fileHash(path)
		if err != nil {
			return indexResult{err: err}
		}
		hash = h
	}

	return indexResult{
		filename:      filename,
		hash:          hash,
		cleanIdentity: cleanIdentity,
		identity:      identity,
	}
}

func parseFileIdentity(filename string) (identity, cleanIdentity, hash string) {
	ext := filepath.Ext(filename)
	identity = strings.TrimSuffix(filename, ext)

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
		hash = parts[1]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		hash = parts[len(parts)-1]
		identity = strings.Join(parts[:len(parts)-1], "__")
	}

	cleanIdentity = identity
	cleanIdentity = strings.Split(cleanIdentity, "_opt")[0]
	if lastUnderscore := strings.LastIndex(cleanIdentity, "_"); lastUnderscore != -1 {
		suffix := cleanIdentity[lastUnderscore+1:]
		if strings.Contains(suffix, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(suffix, "%dx%d", &w, &h); n == 2 {
				cleanIdentity = cleanIdentity[:lastUnderscore]
			}
		}
	}
	return identity, cleanIdentity, hash
}

// fileHash calculates the SHA256 hash of a file's content.
func (l *Loader) fileHash(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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

// cleanupUnusedSources removes managed images from the artwork directory
// that were not encountered during the current sync cycle.
func (l *Loader) cleanupUnusedSources() {
	entries, err := os.ReadDir(l.artworkDir)
	if err != nil {
		return
	}

	managedPrefixes := []string{"src_", "unsplash_", "nasa_", "artic_", "pexels_", "pixabay_"}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		l.mu.Lock()
		visited := l.visited[filename]
		l.mu.Unlock()
		if visited {
			continue
		}

		isManaged := false
		if reManagedIndex.MatchString(filename) {
			isManaged = true
		} else {
			for _, prefix := range managedPrefixes {
				if strings.HasPrefix(filename, prefix) {
					isManaged = true
					break
				}
			}
		}

		if isManaged {
			l.logger.Info("removing unused source image", "file", filename)
			_ = os.Remove(filepath.Join(l.artworkDir, filename))
		}
	}
}
