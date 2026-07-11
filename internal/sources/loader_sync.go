package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// SourceLoader downloads remote artwork into the local collection.
type SourceLoader interface {
	Sync(context.Context) (downloaded int, err error)
}

// Ensure Loader implements SourceLoader.
var _ SourceLoader = (*Loader)(nil)

// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded.
//
//nolint:funlen,gocognit,gocyclo // cycle staging, cancellation, and conservative pruning are intentionally colocated
func (l *Loader) Sync(ctx context.Context) (int, error) {
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
	var errorMu sync.Mutex
	var cycleErrors []error
	semaphore := make(chan struct{}, maxConcurrentSourceLines)

	for _, line := range urls {
		if err := ctx.Err(); err != nil {
			return int(downloaded), err
		}
		wg.Add(1)
		semaphore <- struct{}{}
		go func(lne string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			count, syncErr := l.syncLine(ctx, lne, &globalIndex)
			if syncErr != nil {
				l.logger.Warn("source resolve failed", "line", lne, "error", syncErr)
				errorMu.Lock()
				cycleErrors = append(cycleErrors, fmt.Errorf("sync source %q: %w", lne, syncErr))
				errorMu.Unlock()
				return
			}

			if count > 0 {
				//nolint:gosec // per-line download count is bounded by MaxArtworkImages
				atomic.AddInt32(&downloaded, int32(count))
			}
		}(line)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return int(downloaded), err
	}

	// Remove managed images that are no longer in sources.
	if len(cycleErrors) == 0 {
		for _, filename := range l.index.UnusedManagedFiles() {
			l.logger.Info("removing unused source image", "file", filename)
			if err := os.Remove(filepath.Join(l.artworkDir, filename)); err != nil {
				cycleErrors = append(cycleErrors, fmt.Errorf("remove unused source image %s: %w", filename, err))
			}
		}
	} else {
		l.logger.Warn("retaining previous source collection because resolution was incomplete", "errors", len(cycleErrors))
	}

	if downloaded > 0 {
		l.logger.Info("downloaded new source images", "count", downloaded)
	}

	return int(downloaded), errors.Join(cycleErrors...)
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
	var downloadErrors []error
	for _, img := range images {
		ok, dErr := l.downloadWithIdentity(ctx, img.URL, img.Identity)
		if dErr != nil {
			l.logger.Warn("source download failed", "url", img.URL, "error", dErr)
			downloadErrors = append(downloadErrors, fmt.Errorf("download %s: %w", img.URL, dErr))
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
	return count, errors.Join(downloadErrors...)
}
