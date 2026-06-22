package optimize

import (
	"context"
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

type optContext struct {
	artworkDir string
	localFiles map[string]struct{}
	cfg        Config
	onRename   func(oldName, newName string)
	catalog    Catalog
	logger     *slog.Logger
	mu         sync.Mutex
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

// Worker-pool bounds: enough parallelism to saturate disk + CPU on typical
// hosts without oversubscribing on very large machines.
const (
	minOptimizeWorkers = 4
	maxOptimizeWorkers = 16
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
	onRename func(oldName, newName string),
	logger *slog.Logger,
) (int, error) {
	localFiles, err := catalog.SupportedFiles()
	if err != nil {
		return 0, err
	}

	var optimizedCount int64

	// Collage pairing runs in two cases:
	//   1. Always for uploaded files (prefixed "upload"): iPhone/web uploads are
	//      personal photos and benefit from side-by-side collage layout.
	//   2. For all portrait files when PORTRAIT_MODE=collage is explicitly set.
	//
	// Remote source images (Unsplash, NASA, etc.) default to crop mode and are
	// excluded from auto-collage unless PORTRAIT_MODE=collage is set.
	processCollages(collageBatch{
		artworkDir:     artworkDir,
		localFiles:     localFiles,
		cfg:            cfg,
		catalog:        catalog,
		onRename:       onRename,
		logger:         logger,
		optimizedCount: &optimizedCount,
	})

	o := &optContext{
		artworkDir: artworkDir,
		localFiles: localFiles,
		cfg:        cfg,
		onRename:   onRename,
		catalog:    catalog,
		logger:     logger,
	}

	runOptimizeWorkers(ctx, enqueueOptimizeJobs(localFiles), o, &optimizedCount)

	if err := ctx.Err(); err != nil {
		return int(optimizedCount), err
	}
	return int(optimizedCount), nil
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
				if wasModified, ok := handleSingleOptimization(filename, o); ok && wasModified {
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

func handleSingleOptimization(filename string, o *optContext) (bool, bool) {
	path := filepath.Join(o.artworkDir, filename)

	if !o.cfg.Enabled {
		if err := ValidateImage(path); err != nil {
			o.logger.Warn("skipping corrupt image", "file", filename, "error", err)
			o.recordDelete(filename)
			return false, false
		}
		return false, true
	}

	newFilename, modified, err := OptimizeFile(path, o.cfg, o.logger)
	if err != nil {
		o.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		o.recordDelete(filename)
		return false, false
	}

	if modified && newFilename != filename {
		if o.onRename != nil {
			o.onRename(filename, newFilename)
		}
		o.catalog.NoteFileRename(filename, newFilename)
		o.recordRename(filename, newFilename)
	}

	return modified, true
}
