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
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	unsplash    *UnsplashClient
	nasa        *NASAClient
	artic       *ArticClient
	pexels      *PexelsClient
}

// NewLoader creates a new sources loader.
func NewLoader(sourcesFile, artworkDir string, unsplashKey, nasaKey, pexelsKey string, logger *slog.Logger) *Loader {
	return &Loader{
		sourcesFile: sourcesFile,
		artworkDir:  artworkDir,
		logger:      logger,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		unsplash: NewUnsplashClient(unsplashKey, logger),
		nasa:     NewNASAClient(nasaKey, logger),
		artic:    NewArticClient(logger),
		pexels:   NewPexelsClient(pexelsKey, logger),
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

	downloaded := 0
	for _, line := range urls {
		// Existing source processing logic...
		if strings.HasPrefix(line, "unsplash:") {
			count, err := l.handleUnsplashLine(line)
			if err != nil {
				l.logger.Warn("unsplash sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "nasa:") {
			count, err := l.handleNASALine(line)
			if err != nil {
				l.logger.Warn("nasa sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "artic:") || strings.HasPrefix(line, "art_institute:") || strings.HasPrefix(line, "art_institute_of_chicago:") {
			count, err := l.handleArticLine(line)
			if err != nil {
				l.logger.Warn("art_institute sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "pexels:") {
			count, err := l.handlePexelsLine(line)
			if err != nil {
				l.logger.Warn("pexels sync failed", "line", line, "error", err)
			}
			downloaded += count
			continue
		}

		if strings.HasPrefix(line, "direct:") {
			line = strings.TrimPrefix(line, "direct:")
		}

		ok, err := l.downloadIfNew(line)
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

	// Ensure inclusive permissions for Mac access.
	_ = os.Chmod(destPath, 0644) //nolint:gosec // Requires inclusive permissions

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

// handleUnsplashLine resolves Unsplash collection or photo IDs and downloads them.
func (l *Loader) handleUnsplashLine(line string) (int, error) {
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
	case "photo":
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
		// Prefer RAW for maximum quality, with Frame TV friendly width.
		url := p.URLs.Raw + "&w=3840&q=95&fm=jpg"
		
		// Use a deterministic filename based on Unsplash ID.
		filename := fmt.Sprintf("unsplash_%s.jpg", p.ID)
		destPath := filepath.Join(l.artworkDir, filename)

		if _, err := os.Stat(destPath); err == nil {
			continue // skip existing
		}

		// Track download as required by TOS.
		l.unsplash.TrackDownload(ctx, p.Links.DownloadLocation)

		ok, err := l.downloadToFile(url, destPath)
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

// downloadToFile is a helper that downloads a URL directly to a path.
func (l *Loader) downloadToFile(url string, destPath string) (bool, error) {
	resp, err := l.client.Get(url)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmpPath := destPath + ".tmp"
	out, err := os.Create(filepath.Clean(tmpPath))
	if err != nil {
		return false, err
	}

	written, err := io.Copy(out, resp.Body)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}

	_ = os.Chmod(destPath, 0644) //nolint:gosec // Required for Mac/Docker volume access
	l.logger.Info("downloaded unsplash image", "path", filepath.Base(destPath), "size", written)
	return true, nil
}

// handleNASALine resolves NASA APOD or search queries and downloads them.
//nolint:gocyclo // NASA API requires multi-step manifest resolution
func (l *Loader) handleNASALine(line string) (int, error) {
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
	case "search":
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
		if strings.Contains(u, "nasa.gov") {
			// For NASA library, try to keep a more descriptive name if possible.
			// The URL usually contains the NASA ID.
			parts := strings.Split(u, "/")
			if len(parts) > 0 {
				last := parts[len(parts)-1]
				if strings.Contains(last, "~") {
					filename = "nasa_" + strings.Split(last, "~")[0] + ".jpg"
				}
			}
		}
		
		destPath := filepath.Join(l.artworkDir, filename)

		if _, err := os.Stat(destPath); err == nil {
			continue // skip existing
		}

		ok, err := l.downloadToFile(u, destPath)
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
func (l *Loader) handleArticLine(line string) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 3 {
		return 0, fmt.Errorf("invalid art_institute_of_chicago format: %s (expected art_institute_of_chicago:search:query or art_institute_of_chicago:photo:id)", line)
	}

	ctx := context.Background()
	var urls []string

	switch parts[1] {
	case "search":
		p, err := l.artic.Search(ctx, parts[2])
		if err != nil {
			return 0, err
		}
		urls = p
	case "photo":
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
		filename := l.urlToFilename(u)
		if strings.Contains(u, "artic.edu") {
			// Try to extract image_id for a nicer filename
			parts := strings.Split(u, "/")
			if len(parts) > 5 {
				filename = "artic_" + parts[5] + ".jpg"
			}
		}

		destPath := filepath.Join(l.artworkDir, filename)
		if _, err := os.Stat(destPath); err == nil {
			continue // skip existing
		}

		ok, err := l.downloadToFile(u, destPath)
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
func (l *Loader) handlePexelsLine(line string) (int, error) {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid pexels format: %s (expected pexels:search:query, pexels:curated, or pexels:photo:id)", line)
	}

	ctx := context.Background()
	var urls []string

	switch parts[1] {
	case "search":
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
	case "photo":
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
		ok, err := l.downloadIfNew(u)
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
