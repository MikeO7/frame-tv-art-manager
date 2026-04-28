// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
}

// NewLoader creates a new sources loader.
func NewLoader(sourcesFile, artworkDir string, unsplashKey, nasaKey, pexelsKey, pixabayKey string, maxImages, maxSizeMB int, logger *slog.Logger) *Loader {
	return &Loader{
		sourcesFile: sourcesFile,
		artworkDir:  artworkDir,
		maxImages:   maxImages,
		maxSizeMB:   maxSizeMB,
		logger:      logger,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		unsplash: NewUnsplashClient(unsplashKey, logger),
		nasa:     NewNASAClient(nasaKey, logger),
		artic:    NewArticClient(logger),
		pexels:   NewPexelsClient(pexelsKey, logger),
		pixabay:  NewPixabayClient(pixabayKey, logger),
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
	downloaded := 0
	for i, line := range urls {
		prefix := fmt.Sprintf("%03d_", i+1)
		if strings.HasPrefix(line, "unsplash:") {
			count, err := l.handleUnsplashLine(line, prefix)
			if err != nil {
				l.logger.Warn("unsplash sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "nasa:") {
			count, err := l.handleNASALine(line, prefix)
			if err != nil {
				l.logger.Warn("nasa sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "artic:") || strings.HasPrefix(line, "art_institute:") || strings.HasPrefix(line, "art_institute_of_chicago:") {
			count, err := l.handleArticLine(line, prefix)
			if err != nil {
				l.logger.Warn("art_institute sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "pexels:") {
			count, err := l.handlePexelsLine(line, prefix)
			if err != nil {
				l.logger.Warn("pexels sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "pixabay:") {
			count, err := l.handlePixabayLine(line, prefix)
			if err != nil {
				l.logger.Warn("pixabay sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		line = strings.TrimPrefix(line, "direct:")
		identity := prefix + l.urlToFilename(line)

		ok, err := l.downloadWithIdentity(line, identity)
		if err != nil {
			l.logger.Warn("failed to download source image",
				"url", truncateURL(line),
				"error", err,
			)
			continue
		}
		if ok {
			downloaded++
		}
	}

	// Remove managed images that are no longer in sources.
	l.cleanupUnusedSources()

	if downloaded > 0 {
		l.logger.Info("downloaded new source images", "count", downloaded)
	}

	return downloaded, nil
}

func (l *Loader) checkExisting(filename string) (string, bool) {
	identity := strings.TrimSuffix(filename, filepath.Ext(filename))
	existing, ok := l.prefixMap[identity]
	return existing, ok
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
			l.visited[existing] = true
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

	l.visited[finalName] = true
	_ = os.Chmod(filepath.Join(l.artworkDir, finalName), 0644) //nolint:gosec

	l.logger.Info("downloaded source image", "file", finalName, "size_bytes", written)
	return true, nil
}

// downloadWithIdentity is a helper that handles the full download, hashing,
// and indexing flow for a given identity.
func (l *Loader) downloadWithIdentity(url, identity string) (bool, error) {
	if l.maxImages > 0 && len(l.index) >= l.maxImages {
		l.logger.Warn("global image limit reached, skipping download", "limit", l.maxImages)
		return false, nil
	}

	if existing, ok := l.checkExisting(identity); ok {
		l.visited[existing] = true
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

	if existing, ok := l.index[hash]; ok {
		if existing != filename {
			l.logger.Info("discarding duplicate content", "file", filename, "matches", existing)
			_ = os.Remove(path)
			l.visited[existing] = true
			return existing, false
		}
	}

	// Get image dimensions for the filename.
	dims := l.imageDimensions(path)

	// Rename to include hash and dimensions for future sync cycles.
	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	// Strip existing hash/meta if any (e.g. from previous runs) to normalize.
	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	}

	finalName := fmt.Sprintf("%s_%s.h_%s%s", identity, dims, hash[:12], ext)
	finalPath := filepath.Join(l.artworkDir, finalName)

	if err := os.Rename(path, finalPath); err != nil {
		l.logger.Warn("failed to rename to hash-based name", "file", filename, "error", err)
		l.index[hash] = filename
		return filename, true
	}

	l.index[hash] = finalName
	return finalName, true
}

func (l *Loader) imageDimensions(path string) string {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "unknown"
	}
	defer func() { _ = f.Close() }()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
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

// handleUnsplashLine resolves Unsplash collection or photo IDs and downloads them.
func (l *Loader) handleUnsplashLine(line, prefix string) (int, error) {
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

	downloaded := 0
	for _, p := range photos {
		// Check global limit.
		if l.maxImages > 0 && len(l.index) >= l.maxImages {
			l.logger.Warn("global image limit reached, skipping unsplash photo", "limit", l.maxImages)
			break
		}

		// Prefer RAW for maximum quality, with Frame TV friendly width.
		url := p.URLs.Raw + "&w=3840&q=95&fm=jpg"

		// Use a descriptive identity including provider and source.
		identity := fmt.Sprintf("%sunsplash_collection-%s_%s", prefix, parts[2], p.ID)
		if parts[1] == "photo" {
			identity = fmt.Sprintf("%sunsplash_photo_%s", prefix, p.ID)
		}

		// Track download as required by TOS.
		l.unsplash.TrackDownload(ctx, p.Links.DownloadLocation)

		ok, err := l.downloadWithIdentity(url, identity)
		if err != nil {
			l.logger.Warn("failed to download unsplash image", "id", p.ID, "error", err)
			continue
		}
		if ok {
			downloaded++
		}
	}

	return downloaded, nil
}

// handleNASALine resolves NASA APOD or search queries and downloads them.
//
//nolint:gocyclo // NASA API requires multi-step manifest resolution
func (l *Loader) handleNASALine(line, prefix string) (int, error) {
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

	downloaded := 0
	for _, u := range urls {
		// Use a deterministic filename based on URL.
		filename := l.urlToFilename(u)
		identity := strings.TrimSuffix(filename, filepath.Ext(filename))

		if strings.Contains(u, "nasa.gov") {
			// For NASA library, try to keep a more descriptive name if possible.
			parts := strings.Split(u, "/")
			if len(parts) > 0 {
				last := parts[len(parts)-1]
				id := strings.Split(last, "~")[0]
				// Shorten and sanitize the ID to keep names reasonable.
				cleanID := sanitize.Filename(id)
				if len(cleanID) > 40 {
					cleanID = cleanID[:40]
				}
				identity = fmt.Sprintf("%snasa_%s", prefix, cleanID)
			}
		}

		ok, err := l.downloadWithIdentity(u, identity)
		if err != nil {
			l.logger.Warn("failed to download nasa image", "url", u, "error", err)
			continue
		}
		if ok {
			downloaded++
		}
	}

	return downloaded, nil
}

// handleArticLine resolves Art Institute of Chicago search queries or photo IDs and downloads them.
func (l *Loader) handleArticLine(line, prefix string) (int, error) {
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

	downloaded := 0
	for _, u := range urls {
		identity := strings.TrimSuffix(l.urlToFilename(u), ".jpg")
		if strings.Contains(u, "artic.edu") {
			// Try to extract image_id for a nicer filename
			parts := strings.Split(u, "/")
			if len(parts) > 5 {
				querySlug := sanitize.Filename(parts[1] + "-" + parts[2])
				identity = fmt.Sprintf("%sartic_%s_%s", prefix, querySlug, parts[5])
			}
		}

		ok, err := l.downloadWithIdentity(u, identity)
		if err != nil {
			l.logger.Warn("failed to download artic image", "url", u, "error", err)
			continue
		}
		if ok {
			downloaded++
		}
	}

	return downloaded, nil
}

// handlePexelsLine resolves Pexels search queries, curated lists, or photo IDs and downloads them.
func (l *Loader) handlePexelsLine(line, prefix string) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid pexels format: %s (expected pexels:search:query, pexels:curated, or pexels:photo:id)", line)
	}

	ctx := context.Background()
	var urls []string

	switch parts[1] {
	case cmdSearch:
		if len(parts) < 3 {
			return 0, fmt.Errorf("pexels search requires a query: %s", line)
		}
		p, err := l.pexels.Search(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		urls = p
	case "curated":
		p, err := l.pexels.Curated(ctx)
		if err != nil {
			return 0, err
		}
		urls = p
	case cmdPhoto:
		if len(parts) < 3 {
			return 0, fmt.Errorf("pexels photo requires an ID: %s", line)
		}
		p, err := l.pexels.FetchPhoto(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		urls = []string{p}
	default:
		return 0, fmt.Errorf("unknown pexels type: %s", parts[1])
	}

	downloaded := 0
	for _, u := range urls {
		identity := prefix + "pexels_" + l.urlToFilename(u)
		ok, err := l.downloadWithIdentity(u, identity)
		if err != nil {
			l.logger.Warn("failed to download pexels image", "url", truncateURL(u), "error", err)
			continue
		}
		if ok {
			downloaded++
		}
	}

	return downloaded, nil
}

// handlePixabayLine resolves Pixabay search queries, editor's choice lists, or photo IDs and downloads them.
func (l *Loader) handlePixabayLine(line, prefix string) (int, error) {
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

	downloaded := 0
	for _, u := range urls {
		identity := prefix + "pixabay_" + l.urlToFilename(u)
		ok, err := l.downloadWithIdentity(u, identity)
		if err != nil {
			l.logger.Warn("failed to download pixabay image", "url", truncateURL(u), "error", err)
			continue
		}
		if ok {
			downloaded++
		}
	}

	return downloaded, nil
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

// buildContentIndex hashes all existing files in the artwork directory
// to enable deduplication and fast syncs.
func (l *Loader) buildContentIndex() {
	l.index = make(map[string]string)
	l.prefixMap = make(map[string]string)

	entries, err := os.ReadDir(l.artworkDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		path := filepath.Join(l.artworkDir, filename)

		var hash string
		// identity is the part before the hash suffix (if any).
		identity := strings.TrimSuffix(filename, filepath.Ext(filename))

		// Try to extract hash from filename: identity.h_[hash].[ext]
		if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
			identity = parts[0]
			hash = parts[1]
		}

		// Clean identity of metadata suffixes (WxH, _opt) for stable lookups.
		cleanIdentity := identity
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

		l.prefixMap[cleanIdentity] = filename

		// If no hash in filename, we must calculate it (once).
		if hash == "" {
			var err error
			hash, err = l.fileHash(path)
			if err != nil {
				continue
			}

			// Rename to include hash for future cycles.
			ext := filepath.Ext(filename)
			newName := identity + ".h_" + hash[:12] + ext
			newPath := filepath.Join(l.artworkDir, newName)
			if err := os.Rename(path, newPath); err == nil {
				filename = newName
				path = newPath
				l.prefixMap[identity] = filename
			}
			l.logger.Debug("migrated file to hash-based name", "original", identity, "hash", hash[:12])
		}

		if existing, ok := l.index[hash]; ok {
			l.logger.Info("found existing duplicate content, removing", "file", filename, "matches", existing)
			_ = os.Remove(path)
		} else {
			l.index[hash] = filename
		}
	}
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
		if l.visited[filename] {
			continue
		}

		isManaged := false
		for _, prefix := range managedPrefixes {
			if strings.HasPrefix(filename, prefix) {
				isManaged = true
				break
			}
		}

		if isManaged {
			l.logger.Info("removing unused source image", "file", filename)
			_ = os.Remove(filepath.Join(l.artworkDir, filename))
		}
	}
}
