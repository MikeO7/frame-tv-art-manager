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
	image    sourceImage
	tracker  downloadTracker
}

// SourceLoader downloads remote artwork into the local collection.
type SourceLoader interface {
	Sync(context.Context, collection.Snapshot) (downloaded int, err error)
}

// Ensure Loader implements SourceLoader.
var _ SourceLoader = (*loader)(nil)

// Sync reads the sources file and downloads any new images. Returns the
// number of newly downloaded images. Skips URLs that have already been
// downloaded.
func (l *loader) Sync(ctx context.Context, snapshot collection.Snapshot) (int, error) {
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
	if err := l.ensureState(ctx); err != nil {
		return 0, err
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
	if err := l.persistState(ctx, urls); err != nil {
		cycleErrors = append(cycleErrors, err)
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

func (l *loader) resetSourceState(snapshot collection.Snapshot) {
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

func (l *loader) disabled() bool {
	return l.sourcesFile == "" || l.dryRun
}

// syncLine resolves and downloads all images for one sources-file line.
//
//nolint:funlen,gocognit // Bounded batching keeps download cleanup, cancellation, and committed counts in one ordered pass.
func (l *loader) syncLine(ctx context.Context, line string) (int, error) {
	provider := l.resolveProvider(line)
	if provider == nil {
		return 0, errors.New("no provider found for source")
	}

	images, err := l.resolve(ctx, provider, line)
	if err != nil {
		return 0, err
	}
	tracker, _ := provider.(downloadTracker)

	var count int
	var downloadErrors []error
	pending := make([]pendingSourceDownload, 0, sourceImportBatchSize)
	var pendingBytes int64
	// Pending downloads are private temporary files; they are removed whether
	// or not the collection imported them.
	discard := func() {
		for _, download := range pending {
			_ = os.Remove(download.prepared.path)
		}
		pending = pending[:0]
		pendingBytes = 0
	}
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		added, rejected, err := l.importPending(ctx, pending)
		discard()
		count += added
		downloadErrors = append(downloadErrors, rejected...)
		return err
	}
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			discard()
			return count, err
		}
		if l.checkExisting(img.Identity) {
			continue
		}
		if l.downloadBackingOff("source:" + img.Identity) {
			l.logger.Debug("skipping source image that failed earlier until its retry is due", "identity", img.Identity)
			continue
		}
		if l.maxImages > 0 && l.collectionSize+len(pending) >= l.maxImages {
			l.logger.Warn("global image limit reached, skipping download", "limit", l.maxImages)
			break
		}
		prepared, dErr := l.prepareImage(ctx, img)
		if dErr != nil {
			if ctx.Err() != nil {
				discard()
				return count, ctx.Err()
			}
			l.recordDownloadFailure("source:"+img.Identity, dErr)
			l.logger.Warn("source download failed", "url", truncateURL(img.URL), "error", dErr)
			downloadErrors = append(downloadErrors, dErr)
			continue
		}
		if len(pending) > 0 && pendingBytes+prepared.written > sourceImportBatchMaxBytes {
			if err := flush(); err != nil {
				_ = os.Remove(prepared.path)
				return count, errors.Join(append(downloadErrors, err)...)
			}
		}
		pending = append(pending, pendingSourceDownload{prepared: prepared, image: img, tracker: tracker})
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

// importPending imports a batch of downloads. When the collection refuses the
// batch because of one download (invalid artwork) or because it is full, the
// downloads are imported one at a time so the valid ones are kept. It returns
// the count added, the errors of individually rejected downloads, and any
// error that stops the source line.
func (l *loader) importPending(ctx context.Context, pending []pendingSourceDownload) (int, []error, error) {
	added, err := l.importSourceBatch(ctx, pending)
	invalid := errors.Is(err, collection.ErrInvalidImport)
	separable := len(pending) > 1 && errors.Is(err, collection.ErrCollectionFull)
	if err == nil || !invalid && !separable {
		return added, nil, err
	}
	if len(pending) == 1 {
		l.recordDownloadFailure(pending[0].prepared.originKey, err)
		return added, []error{err}, nil
	}
	l.logger.Warn("collection refused a source import batch; importing its downloads one at a time",
		"downloads", len(pending), "error", err)
	var rejected []error
	for index := range pending {
		one, err := l.importSourceBatch(ctx, pending[index:index+1])
		added += one
		if err == nil {
			continue
		}
		if !errors.Is(err, collection.ErrInvalidImport) {
			return added, rejected, err
		}
		l.recordDownloadFailure(pending[index].prepared.originKey, err)
		rejected = append(rejected, err)
	}
	return added, rejected, nil
}

//nolint:gocognit // File ownership and ordered import results are handled together to keep cleanup auditable.
func (l *loader) importSourceBatch(ctx context.Context, pending []pendingSourceDownload) (int, error) {
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
		l.clearDownloadFailure(download.prepared.originKey)
		l.recordAttribution(download.prepared.originKey, download.image.Attribution)
		if change.Kind != collection.ChangeAdded {
			continue
		}
		added++
		l.collectionSize++
		l.logger.Info("downloaded source image", "file", items[change.Name].Name, "size_bytes", download.prepared.written)
		if download.tracker != nil && download.image.TrackURL != "" {
			if trackErr := download.tracker.TrackDownload(ctx, download.image.TrackURL); trackErr != nil {
				l.logger.Warn("provider download tracking failed", "error", trackErr)
			}
		}
	}
	return added, nil
}
