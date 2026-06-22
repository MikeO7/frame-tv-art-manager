// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	// maxConcurrentSourceLines caps simultaneous source-line processing so we
	// don't trip provider rate limits with a burst of parallel requests.
	maxConcurrentSourceLines = 5
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
