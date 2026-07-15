package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

const (
	sourceImportBatchSize     = 25
	sourceImportBatchMaxBytes = 256 << 20
)

type pendingSourceDownload struct {
	prepared preparedDownload
	image    SourceImage
}

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
			if ctx.Err() != nil {
				return downloaded, ctx.Err()
			}
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
//
//nolint:funlen,gocognit,gocyclo // Bounded batching keeps download cleanup, cancellation, and committed counts in one ordered pass.
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
	pending := make([]pendingSourceDownload, 0, sourceImportBatchSize)
	var pendingBytes int64
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		added, err := l.importSourceBatch(ctx, pending)
		for _, download := range pending {
			_ = os.Remove(download.prepared.path)
		}
		pending = pending[:0]
		pendingBytes = 0
		count += added
		return err
	}
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			for _, download := range pending {
				_ = os.Remove(download.prepared.path)
			}
			return count, err
		}
		originKey := "source:" + img.Identity
		if l.checkExisting(img.Identity) {
			continue
		}
		if l.maxImages > 0 && l.collectionSize+len(pending) >= l.maxImages {
			l.logger.Warn("global image limit reached, skipping download", "limit", l.maxImages)
			break
		}
		filename := img.Identity + ".jpg"
		prepared, dErr := l.prepareDownload(ctx, img.URL, filename, img.Identity, originKey)
		if dErr != nil {
			if ctx.Err() != nil {
				for _, download := range pending {
					_ = os.Remove(download.prepared.path)
				}
				return count, ctx.Err()
			}
			l.logger.Warn("source download failed", "url", truncateURL(img.URL), "error", dErr)
			downloadErrors = append(downloadErrors, fmt.Errorf("download %s: %w", truncateURL(img.URL), dErr))
			continue
		}
		if len(pending) > 0 && pendingBytes+prepared.written > sourceImportBatchMaxBytes {
			if err := flush(); err != nil {
				_ = os.Remove(prepared.path)
				return count, errors.Join(append(downloadErrors, err)...)
			}
		}
		pending = append(pending, pendingSourceDownload{prepared: prepared, image: img})
		pendingBytes += prepared.written
		if len(pending) == sourceImportBatchSize {
			if err := flush(); err != nil {
				return count, errors.Join(append(downloadErrors, err)...)
			}
		}
	}
	if err := flush(); err != nil {
		return count, errors.Join(append(downloadErrors, err)...)
	}
	return count, errors.Join(downloadErrors...)
}

//nolint:funlen,gocognit,gocyclo // File ownership and ordered import results are handled together to keep cleanup auditable.
func (l *Loader) importSourceBatch(ctx context.Context, pending []pendingSourceDownload) (int, error) {
	if l.importer == nil {
		return 0, errors.New("source collection importer is required")
	}
	files := make([]*os.File, 0, len(pending))
	requests := make([]collection.ImportRequest, 0, len(pending))
	for _, download := range pending {
		file, err := os.Open(filepath.Clean(download.prepared.path))
		if err != nil {
			for _, opened := range files {
				_ = opened.Close()
			}
			return 0, fmt.Errorf("open validated source download: %w", err)
		}
		files = append(files, file)
		requests = append(requests, collection.ImportRequest{
			Reader: file, Hint: download.prepared.filename, MaxBytes: int64(l.maxSizeMB) * bytesPerMB,
			Origin: collection.Origin{Key: download.prepared.originKey, Class: collection.OriginSource},
		})
	}
	snapshot, importErr := l.importer.ImportBatch(ctx, requests)
	for _, file := range files {
		_ = file.Close()
	}
	if importErr != nil {
		return 0, fmt.Errorf("transactionally import source artwork batch: %w", importErr)
	}
	if len(snapshot.Changes) != len(pending) {
		return 0, fmt.Errorf("source import returned %d changes for %d downloads", len(snapshot.Changes), len(pending))
	}
	items := make(map[string]collection.Item, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items[item.Name] = item
	}
	var added int
	for index, change := range snapshot.Changes {
		if change.Name == "" {
			return added, errors.New("source import returned an empty committed item name")
		}
		//nolint:gosec // exact slice lengths are checked before this paired ordered walk
		download := pending[index]
		l.sourceOrigins[download.prepared.originKey] = struct{}{}
		if change.Kind != collection.ChangeAdded {
			continue
		}
		added++
		l.collectionSize++
		l.logger.Info("downloaded source image", "file", items[change.Name].Name, "size_bytes", download.prepared.written)
		if download.image.OnDownload != nil {
			if hookErr := download.image.OnDownload(ctx); hookErr != nil {
				l.logger.Warn("post-download hook failed", "error", hookErr)
			}
		}
	}
	return added, nil
}
