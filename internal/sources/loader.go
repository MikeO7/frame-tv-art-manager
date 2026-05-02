// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/sanitize"
	"gopkg.in/yaml.v3"
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
	sourcesFile string
	artworkDir  string
	logger      *slog.Logger
	client      *http.Client
	unsplash    *UnsplashClient
	nasa        *NASAClient
	artic       *ArticClient
	pexels      *PexelsClient
	pixabay     *PixabayClient
	maxImages   int
	maxSizeMB   int
	index       map[string]string // hash -> filename (content deduplication)
	prefixMap   map[string]string // prefix -> filename (idempotency check)
	visited     map[string]bool   // filename -> true (cleanup tracking)
	mu          sync.Mutex        // Protects index, prefixMap, and visited
}

// NewLoader creates a new sources loader.
func NewLoader(sourcesFile, artworkDir string, unsplashAppID, unsplashAccessKey, unsplashSecretKey, nasaKey, pexelsKey, pixabayKey string, maxImages, maxSizeMB int, logger *slog.Logger) *Loader {
	return &Loader{
		sourcesFile: sourcesFile,
		artworkDir:  artworkDir,
		maxImages:   maxImages,
		maxSizeMB:   maxSizeMB,
		logger:      logger,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		unsplash:  NewUnsplashClient(unsplashAppID, unsplashAccessKey, unsplashSecretKey, logger),
		nasa:      NewNASAClient(nasaKey, logger),
		artic:     NewArticClient(logger),
		pexels:    NewPexelsClient(pexelsKey, logger),
		pixabay:   NewPixabayClient(pixabayKey, logger),
		index:     make(map[string]string),
		prefixMap: make(map[string]string),
		visited:   make(map[string]bool),
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
// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded (matched by URL hash filename).
//
//nolint:gocyclo // Sync loop handles multiple source types sequentially
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

			var count int
			var err error

			switch {
			case strings.HasPrefix(lne, "unsplash:"):
				count, err = l.handleUnsplashLine(lne, &globalIndex)
			case strings.HasPrefix(lne, "nasa:"):
				count, err = l.handleNASALine(lne, &globalIndex)
			case strings.HasPrefix(lne, "artic:") || strings.HasPrefix(lne, "art_institute:") || strings.HasPrefix(lne, "art_institute_of_chicago:"):
				count, err = l.handleArticLine(lne, &globalIndex)
			case strings.HasPrefix(lne, "pexels:"):
				count, err = l.handlePexelsLine(lne, &globalIndex)
			case strings.HasPrefix(lne, "pixabay:"):
				count, err = l.handlePixabayLine(lne, &globalIndex)
			default:
				lne = strings.TrimPrefix(lne, "direct:")
				// We need a unique index for direct sources too.
				idx := atomic.AddInt32(&globalIndex, 1) - 1
				identity := fmt.Sprintf("%03d__direct__%s", idx, l.urlToSlug(lne))
				ok, dErr := l.downloadWithIdentity(lne, identity)
				if dErr == nil && ok {
					count = 1
				}
				err = dErr
			}

			if err != nil {
				l.logger.Warn("source sync failed", "line", lne, "error", err)
			}
			if count > 0 {
				atomic.AddInt32(&downloaded, int32(count))
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

	req, err := http.NewRequest("GET", url, nil)
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
		if size := resp.ContentLength; size > int64(l.maxSizeMB)*1024*1024 {
			return false, fmt.Errorf("file too large: %d bytes (limit %d MB)", size, l.maxSizeMB)
		}
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
	out, err := os.Create(filepath.Clean(tmpPath)) //nolint:gosec // Safe temporary path
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

	finalName, ok := l.finalizeDownload(destPath, filename)
	if !ok {
		return false, nil
	}

	l.mu.Lock()
	l.visited[finalName] = true
	l.mu.Unlock()
	_ = os.Chmod(filepath.Join(l.artworkDir, finalName), 0644) //nolint:gosec

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

// urlToSlug generates a deterministic slug from a URL.
func (l *Loader) urlToSlug(url string) string {
	if u, err := neturl.Parse(url); err == nil && u.Host != "" {
		host := strings.TrimPrefix(u.Host, "www.")
		path := strings.Trim(u.Path, "/")
		if parts := strings.Split(path, "/"); len(parts) > 0 {
			path = parts[0]
		}
		slug := sanitize.Filename(host + "_" + path)
		slug = strings.ReplaceAll(slug, " ", "-")
		if len(slug) > 100 {
			slug = slug[:100]
		}
		return slug
	}
	return "direct-source"
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

// handleUnsplashLine resolves Unsplash collection or photo IDs and downloads them.
func (l *Loader) handleUnsplashLine(line string, globalIndex *int32) (int, error) {
	if l.unsplash.accessKey == "" {
		return 0, fmt.Errorf("UNSPLASH_ACCESS_KEY not configured")
	}

	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return 0, fmt.Errorf("invalid unsplash format: %s", line)
	}

	ctx := context.Background()
	var photos []UnsplashPhoto

	switch parts[1] {
	case "collection":
		p, err := l.unsplash.FetchCollectionPhotos(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		photos = p
	case cmdPhoto:
		p, err := l.unsplash.FetchPhoto(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		photos = []UnsplashPhoto{*p}
	default:
		return 0, fmt.Errorf("unknown unsplash type: %s", parts[1])
	}

	downloaded := int32(0)
	var wg sync.WaitGroup
	for _, p := range photos {
		// Check global limit.
		//nolint:gosec // maxImages comes from config and is safe to cast
		if l.maxImages > 0 && atomic.LoadInt32(globalIndex) > int32(l.maxImages) {
			l.logger.Warn("global image limit reached, skipping unsplash photo", "limit", l.maxImages)
			break
		}

		wg.Add(1)
		go func(ph UnsplashPhoto) {
			defer wg.Done()
			// Prefer RAW for maximum quality, with Frame TV friendly width.
			url := ph.URLs.Raw + "&w=3840&q=95&fm=jpg"

			// Use a descriptive identity including provider and source.
			slug := sanitize.Filename(parts[2] + "-" + ph.ID)
			slug = strings.ReplaceAll(slug, " ", "-")
			if len(slug) > 100 {
				slug = slug[:100]
			}
			idx := atomic.AddInt32(globalIndex, 1) - 1
			identity := fmt.Sprintf("%03d__unsplash__%s", idx, slug)

			// Fast path: skip download tracking and downloading if we already have it.
			if existing, ok := l.checkExisting(identity); ok {
				l.mu.Lock()
				l.visited[existing] = true
				l.mu.Unlock()
				return
			}

			// Track download as required by TOS.
			l.unsplash.TrackDownload(ctx, ph.Links.DownloadLocation)

			ok, err := l.downloadWithIdentity(url, identity)
			if err != nil {
				l.logger.Warn("failed to download unsplash image", "id", ph.ID, "error", err)
				return
			}
			if ok {
				atomic.AddInt32(&downloaded, 1)
			}
		}(p)
	}
	wg.Wait()

	return int(downloaded), nil
}

// handleNASALine resolves NASA APOD or search queries and downloads them.
//
//nolint:gocyclo // handleNASALine resolves NASA APOD or search queries and downloads them.
func (l *Loader) handleNASALine(line string, globalIndex *int32) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid nasa format: %s", line)
	}

	ctx := context.Background()
	var urls []string

	switch parts[1] {
	case "apod":
		apod, err := l.nasa.FetchAPOD(ctx)
		if err != nil {
			return 0, err
		}
		// Prefer HD version for Frame TV.
		u := apod.HDURL
		if u == "" {
			u = apod.URL
		}
		if u != "" {
			urls = append(urls, u)
		}
	case cmdSearch:
		if len(parts) < 3 {
			return 0, fmt.Errorf("nasa search requires a query: nasa:search:query")
		}
		p, err := l.nasa.SearchNASAImageLibrary(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		urls = p
	default:
		return 0, fmt.Errorf("unknown nasa type: %s", parts[1])
	}

	downloaded := int32(0)
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			// Use a deterministic slug based on URL.
			slug := l.urlToSlug(url)
			if strings.Contains(url, "nasa.gov") {
				parts := strings.Split(url, "/")
				if len(parts) > 0 {
					last := parts[len(parts)-1]
					id := strings.Split(last, "~")[0]
					slug = sanitize.Filename(id)
					slug = strings.ReplaceAll(slug, " ", "-")
					if len(slug) > 100 {
						slug = slug[:100]
					}
				}
			}

			idx := atomic.AddInt32(globalIndex, 1) - 1
			identity := fmt.Sprintf("%03d__nasa__%s", idx, slug)

			ok, err := l.downloadWithIdentity(url, identity)
			if err != nil {
				l.logger.Warn("failed to download nasa image", "url", url, "error", err)
				return
			}
			if ok {
				atomic.AddInt32(&downloaded, 1)
			}
		}(u)
	}
	wg.Wait()

	return int(downloaded), nil
}

// handleArticLine resolves Art Institute of Chicago search queries or photo IDs and downloads them.
func (l *Loader) handleArticLine(line string, globalIndex *int32) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return 0, fmt.Errorf("invalid art_institute_of_chicago format: %s (expected art_institute_of_chicago:search:query or art_institute_of_chicago:photo:id)", line)
	}

	ctx := context.Background()
	var urls []string

	switch parts[1] {
	case cmdSearch:
		p, err := l.artic.Search(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		urls = p
	case cmdPhoto:
		p, err := l.artic.FetchPhoto(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		urls = []string{p}
	default:
		return 0, fmt.Errorf("unknown artic type: %s", parts[1])
	}

	downloaded := int32(0)
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			slug := l.urlToSlug(url)
			if strings.Contains(url, "artic.edu") {
				parts := strings.Split(url, "/")
				if len(parts) > 5 {
					slug = sanitize.Filename(parts[5])
					if len(slug) > 100 {
						slug = slug[:100]
					}
				}
			}

			idx := atomic.AddInt32(globalIndex, 1) - 1
			identity := fmt.Sprintf("%03d__artic__%s", idx, slug)

			ok, err := l.downloadWithIdentity(url, identity)
			if err != nil {
				l.logger.Warn("failed to download artic image", "url", url, "error", err)
				return
			}
			if ok {
				atomic.AddInt32(&downloaded, 1)
			}
		}(u)
	}
	wg.Wait()

	return int(downloaded), nil
}

// handlePexelsLine resolves Pexels search queries, curated lists, or photo IDs and downloads them.
func (l *Loader) handlePexelsLine(line string, globalIndex *int32) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid pexels format: %s (expected pexels:search:query, pexels:curated, or pexels:photo:id)", line)
	}

	ctx := context.Background()
	var urls []string
	var err error

	urls, err = l.fetchPexelsURLs(ctx, parts)
	if err != nil {
		return 0, err
	}

	return l.processPexelsURLs(urls, globalIndex)
}

func (l *Loader) fetchPexelsURLs(ctx context.Context, parts []string) ([]string, error) {
	switch parts[1] {
	case cmdSearch:
		if len(parts) < 3 {
			return nil, fmt.Errorf("pexels search requires a query")
		}
		return l.pexels.Search(ctx, parts[2])
	case "curated":
		return l.pexels.Curated(ctx)
	case "collection":
		if len(parts) < 3 {
			return nil, fmt.Errorf("pexels collection requires an ID")
		}
		return l.pexels.FetchCollection(ctx, parts[2])
	case cmdPhoto:
		if len(parts) < 3 {
			return nil, fmt.Errorf("pexels photo requires an ID")
		}
		p, err := l.pexels.FetchPhoto(ctx, parts[2])
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	default:
		return nil, fmt.Errorf("unknown pexels type: %s", parts[1])
	}
}

func (l *Loader) processPexelsURLs(urls []string, globalIndex *int32) (int, error) {
	downloaded := int32(0)
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			slug := l.urlToSlug(url)
			idx := atomic.AddInt32(globalIndex, 1) - 1
			identity := fmt.Sprintf("%03d__pexels__%s", idx, slug)
			ok, err := l.downloadWithIdentity(url, identity)
			if err != nil {
				l.logger.Warn("failed to download pexels image", "url", truncateURL(url), "error", err)
				return
			}
			if ok {
				atomic.AddInt32(&downloaded, 1)
			}
		}(u)
	}
	wg.Wait()
	return int(downloaded), nil
}

// handlePixabayLine resolves Pixabay search queries, editor's choice lists, or photo IDs and downloads them.
func (l *Loader) handlePixabayLine(line string, globalIndex *int32) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid pixabay format: %s (expected pixabay:search:query, pixabay:editors_choice, or pixabay:photo:id)", line)
	}

	ctx := context.Background()
	var urls []string
	var err error

	switch parts[1] {
	case cmdSearch:
		if len(parts) < 3 {
			return 0, fmt.Errorf("pixabay search requires a query: %s", line)
		}
		urls, err = l.pixabay.Search(ctx, parts[2])
	case "editors_choice":
		urls, err = l.pixabay.EditorsChoice(ctx)
	case cmdPhoto:
		if len(parts) < 3 {
			return 0, fmt.Errorf("pixabay photo requires an ID: %s", line)
		}
		var p string
		p, err = l.pixabay.FetchPhoto(ctx, parts[2])
		if err == nil {
			urls = []string{p}
		}
	case "user":
		if len(parts) < 3 {
			return 0, fmt.Errorf("pixabay user requires an ID: %s", line)
		}
		urls, err = l.pixabay.User(ctx, parts[2])
	default:
		return 0, fmt.Errorf("unknown pixabay type: %s", parts[1])
	}

	if err != nil {
		return 0, err
	}

	downloaded := int32(0)
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			slug := l.urlToSlug(url)
			idx := atomic.AddInt32(globalIndex, 1) - 1
			identity := fmt.Sprintf("%03d__pixabay__%s", idx, slug)
			ok, err := l.downloadWithIdentity(url, identity)
			if err != nil {
				l.logger.Warn("failed to download pixabay image", "url", truncateURL(url), "error", err)
				return
			}
			if ok {
				atomic.AddInt32(&downloaded, 1)
			}
		}(u)
	}
	wg.Wait()

	return int(downloaded), nil
}

// loadSources reads the sources file (TXT or YAML) and returns a list of source strings.
func (l *Loader) loadSources() ([]string, error) {
	ext := strings.ToLower(filepath.Ext(l.sourcesFile))
	if ext == ".yaml" || ext == ".yml" {
		return l.loadYamlSources()
	}
	return l.loadTxtSources()
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

		// If the filename didn't contain the hash, rename it now (sequentially to be safe)
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

// cleanupUnusedSources removes managed images (src_, unsplash_, etc.) from the artwork
// directory that were not encountered during the current sync cycle.
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
		// Match old format (source_) and new format (000__)
		if regexp.MustCompile(`^[0-9]{3}__`).MatchString(filename) {
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
