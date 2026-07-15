package sources

import (
	"context"
	"errors"
	"fmt"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// SourceLoader downloads remote artwork into the local collection.
type SourceLoader interface {
	Sync(context.Context, collection.Snapshot) (downloaded int, err error)
}

// Ensure Loader implements SourceLoader.
var _ SourceLoader = (*Loader)(nil)

// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded.
func (l *Loader) Sync(ctx context.Context, snapshot collection.Snapshot) (int, error) {
	if l.disabled() {
		return 0, nil
	}
	l.resetSourceState(snapshot)

	urls, err := l.loadSources(ctx)
	if err != nil {
		return 0, err
	}

	if len(urls) == 0 {
		return 0, nil
	}

	l.logger.Info("processing image sources", "urls", len(urls))

	// Deduplicate URLs to avoid redundant processing.
	urls = deduplicateStrings(urls)

	var downloaded int
	var cycleErrors []error

	for sourceIndex, line := range urls {
		if err := ctx.Err(); err != nil {
			return downloaded, err
		}
		count, syncErr := l.syncLine(ctx, line)
		downloaded += count
		if syncErr != nil {
			l.logger.Warn("source resolve failed", "source_index", sourceIndex+1, "error", syncErr)
			cycleErrors = append(cycleErrors, fmt.Errorf("sync source %d: %w", sourceIndex+1, syncErr))
		}
	}
	if err := ctx.Err(); err != nil {
		return downloaded, err
	}

	// Every successful addition is already committed with its Source Origin by
	// the Collection Store. An incomplete provider view is never authority to
	// remove an earlier committed item.
	if len(cycleErrors) != 0 {
		l.logger.Warn("retaining previous source collection because resolution was incomplete", "errors", len(cycleErrors))
	}

	if downloaded > 0 {
		l.logger.Info("downloaded new source images", "count", downloaded)
	}

	return downloaded, errors.Join(cycleErrors...)
}

func (l *Loader) resetSourceState(snapshot collection.Snapshot) {
	l.sourceOrigins = make(map[string]struct{})
	l.collectionSize = len(snapshot.Items)
	for _, item := range snapshot.Items {
		if item.Origin.Class == collection.OriginSource {
			l.sourceOrigins[item.Origin.Key] = struct{}{}
		}
		for _, key := range item.SourceKeys {
			l.sourceOrigins[key] = struct{}{}
		}
	}
}

func (l *Loader) disabled() bool {
	return l.sourcesFile == "" || (l.cfg != nil && l.cfg.DryRun)
}

// syncLine resolves and downloads all images for one sources-file line.
func (l *Loader) syncLine(ctx context.Context, line string) (int, error) {
	provider := l.resolveProvider(line)
	if provider == nil {
		return 0, errors.New("no provider found for source")
	}

	images, err := provider.Resolve(ctx, line)
	if err != nil {
		return 0, err
	}

	var count int
	var downloadErrors []error
	for _, img := range images {
		originKey := "source:" + img.Identity
		ok, dErr := l.downloadWithIdentity(ctx, img.URL, img.Identity, originKey)
		if dErr != nil {
			l.logger.Warn("source download failed", "url", truncateURL(img.URL), "error", dErr)
			downloadErrors = append(downloadErrors, fmt.Errorf("download %s: %w", truncateURL(img.URL), dErr))
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
