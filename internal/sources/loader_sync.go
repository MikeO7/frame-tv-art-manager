package sources

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// loadSources reads the sources file (TXT) and returns a list of source strings.
func (l *Loader) loadSources() ([]string, error) {
	info, statErr := os.Stat(l.sourcesFile)
	if statErr == nil {
		if info.ModTime().Equal(l.lastSourcesModTime) && l.cachedUrls != nil {
			return l.cachedUrls, nil
		}
		l.lastSourcesModTime = info.ModTime()
	}

	urls, err := l.loadTxtSources()
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
