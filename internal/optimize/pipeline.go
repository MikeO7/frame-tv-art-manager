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
	mu         *sync.Mutex
}

// OptimizeCatalog performs parallel image resizing/validation across the catalog files.
//
//nolint:gocognit,revive,funlen // complexity, length, and argument count justified for parallel task processing
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

	type job struct {
		filename string
	}
	jobs := make(chan job, len(localFiles))
	for filename := range localFiles {
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		jobs <- job{filename: filename}
	}
	close(jobs)

	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 16 {
		numWorkers = 16
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(numWorkers)

	o := &optContext{
		artworkDir: artworkDir,
		localFiles: localFiles,
		cfg:        cfg,
		onRename:   onRename,
		catalog:    catalog,
		logger:     logger,
		mu:         &mu,
	}

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				wasModified, ok := handleSingleOptimization(j.filename, o)
				if ok && wasModified {
					atomic.AddInt64(&optimizedCount, 1)
				}
			}
		}()
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return int(optimizedCount), err
	}

	return int(optimizedCount), nil
}

func handleSingleOptimization(filename string, o *optContext) (bool, bool) {
	path := filepath.Join(o.artworkDir, filename)

	if !o.cfg.Enabled {
		if err := ValidateImage(path); err != nil {
			o.logger.Warn("skipping corrupt image", "file", filename, "error", err)
			o.mu.Lock()
			delete(o.localFiles, filename)
			o.mu.Unlock()
			return false, false
		}
		return false, true
	}

	newFilename, modified, err := OptimizeFile(path, o.cfg, o.logger)
	if err != nil {
		o.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		o.mu.Lock()
		delete(o.localFiles, filename)
		o.mu.Unlock()
		return false, false
	}

	if modified && newFilename != filename {
		if o.onRename != nil {
			o.onRename(filename, newFilename)
		}
		o.catalog.NoteFileRename(filename, newFilename)
		o.mu.Lock()
		delete(o.localFiles, filename)
		o.localFiles[newFilename] = struct{}{}
		o.mu.Unlock()
	}

	return modified, true
}
