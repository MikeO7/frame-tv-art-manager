// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "image/jpeg"
	_ "image/png"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
	"gopkg.in/yaml.v3"
)

const maxSourcesManifestBytes int64 = 4 << 20

const (
	extJPG      = ".jpg"
	extPNG      = ".png"
	schemeHTTP  = "http"
	schemeHTTPS = "https"

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
	cfg               *config.Config
	sourcesFile       string
	artworkDir        string
	logger            *slog.Logger
	client            *http.Client
	providers         []SourceProvider
	maxImages         int
	maxSizeMB         int
	importer          CollectionImporter
	sourceOrigins     map[string]struct{}
	collectionSize    int
	lastSourcesDigest [sha256.Size]byte
	hasSourcesDigest  bool
	cachedUrls        []string
}

// CollectionImporter is the narrow collection seam used to transactionally
// publish a validated source download. The source module never writes a final
// artwork path directly.
type CollectionImporter interface {
	Import(context.Context, collection.ImportRequest) (collection.Snapshot, error)
	ImportBatch(context.Context, []collection.ImportRequest) (collection.Snapshot, error)
}

// NewLoader creates a new sources loader from application config.
func NewLoader(
	cfg *config.Config,
	logger *slog.Logger,
	importer CollectionImporter,
) *Loader {
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
		providers:     providers,
		importer:      importer,
		sourceOrigins: make(map[string]struct{}),
	}
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
func (l *Loader) loadSources(ctx context.Context) ([]string, error) {
	data, err := l.readSourcesManifest(ctx)
	if err != nil || data == nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if l.hasSourcesDigest && digest == l.lastSourcesDigest && l.cachedUrls != nil {
		return append([]string(nil), l.cachedUrls...), nil
	}

	var urls []string
	ext := strings.ToLower(filepath.Ext(l.sourcesFile))
	if ext == ".yaml" || ext == ".yml" {
		urls, err = parseYamlSources(data)
	} else {
		urls, err = parseTxtSources(data)
	}

	if err == nil {
		l.cachedUrls = append([]string(nil), urls...)
		l.lastSourcesDigest = digest
		l.hasSourcesDigest = true
	}
	return urls, err
}

func parseTxtSources(data []byte) ([]string, error) {
	var urls []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls, scanner.Err()
}

func parseYamlSources(data []byte) ([]string, error) {
	if urls, ok := decodeProviderSources(data); ok {
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

func (l *Loader) readSourcesManifest(ctx context.Context) ([]byte, error) {
	data, err := durablefs.ReadStable(ctx, l.sourcesFile, durablefs.StableReadOptions{MaxBytes: maxSourcesManifestBytes})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sources manifest: %w", err)
	}
	return data, nil
}

func decodeProviderSources(data []byte) ([]string, bool) {
	var dry struct {
		Providers map[string][]string `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &dry); err != nil || len(dry.Providers) == 0 {
		return nil, false
	}

	providers := make([]string, 0, len(dry.Providers))
	for provider := range dry.Providers {
		providers = append(providers, provider)
	}
	sort.Strings(providers)

	var urls []string
	for _, provider := range providers {
		for _, command := range dry.Providers[provider] {
			urls = append(urls, fmt.Sprintf("%s:%s", provider, command))
		}
	}
	return urls, true
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
