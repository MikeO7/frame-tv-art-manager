package optimize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// Catalog is the seam defining capabilities required from the catalog index.
type Catalog interface {
	SupportedFiles() (map[string]struct{}, error)
	NoteFileRename(oldName, newName string)
}

// RenameObserver persists effects of an artwork filename change outside the
// local catalog. Returning an error makes failed durable propagation part of
// the collection transformation outcome.
type RenameObserver func(oldName, newName string) error

type optContext struct {
	artworkDir   string
	localFiles   map[string]struct{}
	cfg          Config
	onRename     RenameObserver
	catalog      Catalog
	logger       *slog.Logger
	inputs       map[string]StageInput
	transformKey string
	mu           sync.Mutex
	errs         []error
}

func (o *optContext) recordError(err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.errs = append(o.errs, err)
}

func (o *optContext) errors() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return errors.Join(o.errs...)
}

func (o *optContext) observeRename(oldName, newName string) {
	if o.onRename == nil {
		return
	}
	if err := o.onRename(oldName, newName); err != nil {
		o.recordError(fmt.Errorf("observe artwork rename %s to %s: %w", oldName, newName, err))
	}
}

func (o *optContext) recordDelete(filename string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.localFiles, filename)
}

func (o *optContext) recordRename(oldName, newName string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.localFiles, oldName)
	if newName != "" {
		o.localFiles[newName] = struct{}{}
	}
}

// Full-resolution decodes are serialized. Pixel-level work remains bounded by
// the shared Controller, avoiding multiple 4K image buffers in memory.
const (
	minOptimizeWorkers = 1
	maxOptimizeWorkers = 1
)

// OptimizeCatalog resizes, validates, and (optionally) collages the catalog's
// images in parallel, invoking onRename for any file renamed during the pass.
// It returns the number of optimized files and any context-cancellation error.
//
//nolint:revive // top-level public entry point; arguments are orthogonal and ctx is conventionally separate
func OptimizeCatalog(
	ctx context.Context,
	artworkDir string,
	catalog Catalog,
	cfg Config,
	onRename RenameObserver,
	logger *slog.Logger,
) (int, error) {
	return optimizeCatalog(ctx, artworkDir, catalog, cfg, onRename, logger, nil)
}

//nolint:revive // internal orchestration seam keeps filesystem, catalog, policy, observer, and metadata explicit
func optimizeCatalog(
	ctx context.Context,
	artworkDir string,
	catalog Catalog,
	cfg Config,
	onRename RenameObserver,
	logger *slog.Logger,
	inputs map[string]StageInput,
) (int, error) {
	localFiles, err := catalog.SupportedFiles()
	if err != nil {
		return 0, err
	}

	var optimizedCount int64

	// Collage pairing is explicit: every portrait source is eligible only when
	// PORTRAIT_MODE=collage. Upload origin does not override operator policy.
	//nolint:contextcheck // the batch carries this exact context into every collage filesystem operation
	collageErr := processCollages(collageBatch{
		ctx:            ctx,
		artworkDir:     artworkDir,
		localFiles:     localFiles,
		cfg:            cfg,
		catalog:        catalog,
		onRename:       onRename,
		logger:         logger,
		optimizedCount: &optimizedCount,
		inputs:         inputs,
	})

	o := &optContext{
		artworkDir:   artworkDir,
		localFiles:   localFiles,
		cfg:          cfg,
		onRename:     onRename,
		catalog:      catalog,
		logger:       logger,
		inputs:       inputs,
		transformKey: TransformKey(cfg),
	}

	runOptimizeWorkers(ctx, enqueueOptimizeJobs(localFiles), o, &optimizedCount)

	if err := ctx.Err(); err != nil {
		return int(optimizedCount), errors.Join(err, collageErr, o.errors())
	}
	return int(optimizedCount), errors.Join(collageErr, o.errors())
}

// enqueueOptimizeJobs buffers each eligible filename onto a closed channel,
// pruning AppleDouble ("._") sidecar entries from localFiles in the same pass.
func enqueueOptimizeJobs(localFiles map[string]struct{}) <-chan string {
	jobs := make(chan string, len(localFiles))
	for filename := range localFiles {
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		jobs <- filename
	}
	close(jobs)
	return jobs
}

// runOptimizeWorkers fans the jobs out across a bounded worker pool, honoring
// ctx cancellation between files, and blocks until every worker drains.
func runOptimizeWorkers(ctx context.Context, jobs <-chan string, o *optContext, optimizedCount *int64) {
	numWorkers := clampWorkers(runtime.NumCPU())

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for filename := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if wasModified, ok := handleSingleOptimizationContext(ctx, filename, o); ok && wasModified {
					atomic.AddInt64(optimizedCount, 1)
				}
			}
		}()
	}
	wg.Wait()
}

// clampWorkers bounds n to [minOptimizeWorkers, maxOptimizeWorkers].
func clampWorkers(n int) int {
	if n < minOptimizeWorkers {
		return minOptimizeWorkers
	}
	if n > maxOptimizeWorkers {
		return maxOptimizeWorkers
	}
	return n
}

func handleSingleOptimizationContext(ctx context.Context, filename string, o *optContext) (bool, bool) {
	path := filepath.Join(o.artworkDir, filename)
	input := o.inputs[filename]

	if !o.cfg.Enabled {
		if err := ValidateImage(path); err != nil {
			o.logger.Warn("skipping corrupt image", "file", filename, "error", err)
			o.recordDelete(filename)
			return false, false
		}
		return false, true
	}

	if input.Derivative != "" && input.TransformKey == o.transformKey &&
		input.Width == o.cfg.MaxWidth && input.Height == o.cfg.MaxHeight {
		return false, true
	}
	force := input.Derivative != "" && input.TransformKey != o.transformKey
	label := input.Key
	if label == "" {
		label = filename
	}
	newFilename, modified, err := optimizeFileWithPolicy(
		ctx, path, label, force, o.cfg, o.logger, defaultPixelWorkers(),
	)
	if err != nil {
		o.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		o.recordDelete(filename)
		return false, false
	}

	if modified && newFilename != filename {
		o.observeRename(filename, newFilename)
		o.catalog.NoteFileRename(filename, newFilename)
		o.recordRename(filename, newFilename)
	}

	return modified, true
}
