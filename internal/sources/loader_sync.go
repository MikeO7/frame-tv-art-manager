package sources

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

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

// loadSources reads the sources file (YAML) and returns a list of source strings.
func (l *Loader) loadSources() ([]string, error) {
	info, statErr := os.Stat(l.sourcesFile)
	if statErr == nil {
		if info.ModTime().Equal(l.lastSourcesModTime) && l.cachedUrls != nil {
			return l.cachedUrls, nil
		}
		l.lastSourcesModTime = info.ModTime()
	}

	urls, err := l.loadYamlSources()
	if err == nil {
		l.cachedUrls = urls
	}
	return urls, err
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

	l.logger.Error("invalid YAML sources format: must contain 'providers:' map, 'sources:' list, or a direct list", "file", l.sourcesFile)
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
