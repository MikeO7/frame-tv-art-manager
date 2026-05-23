package sync

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

// Collection orchestrates local artwork optimization against a shared catalog index.
type Collection struct {
	cfg      *config.Config
	logger   *slog.Logger
	mappings *MappingStore
	index    *sources.ArtworkIndex
}

// NewCollection instantiates a new local collection manager.
func NewCollection(cfg *config.Config, logger *slog.Logger, index *sources.ArtworkIndex) *Collection {
	return &Collection{
		cfg:      cfg,
		logger:   logger,
		mappings: NewMappingStore(cfg, logger),
		index:    index,
	}
}

// GetMapping returns a cached or newly loaded mapping for a TV.
func (c *Collection) GetMapping(ip string) (*Mapping, error) {
	return c.mappings.Get(ip)
}

// UpdateMappings migrates mappings from old name to new name across all TVs.
func (c *Collection) UpdateMappings(oldName, newName string) {
	c.mappings.RenameAll(oldName, newName)
	c.index.NoteFileRename(oldName, newName)
}

// ScanAndOptimize loads the local catalog and runs worker-pool image optimizations.
func (c *Collection) ScanAndOptimize(cycleLog *slog.Logger) (map[string]struct{}, int, error) {
	localFiles, err := c.index.SupportedFiles()
	if err != nil {
		return nil, 0, fmt.Errorf("scan artwork: %w", err)
	}

	optimized := c.OptimizeLocalArtwork(localFiles, cycleLog)

	cycleLog.Info("local artwork ready", "total", len(localFiles), "optimized", optimized)
	return localFiles, optimized, nil
}

// OptimizeLocalArtwork drives optimization worker threads.
func (c *Collection) OptimizeLocalArtwork(localFiles map[string]struct{}, cycleLog *slog.Logger) int {
	var optimizedCount int64
	optCfg := c.cfg.OptimizeOptions()

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

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				wasModified, ok := c.HandleSingleOptimization(j.filename, localFiles, optCfg, &mu, cycleLog)
				if ok && wasModified {
					atomic.AddInt64(&optimizedCount, 1)
				}
			}
		}()
	}

	wg.Wait()

	if optimizedCount > 0 {
		c.index.InvalidateCache()
	}

	return int(optimizedCount)
}

// HandleSingleOptimization handles resizing/validating a single image.
func (c *Collection) HandleSingleOptimization(
	filename string,
	localFiles map[string]struct{},
	optCfg optimize.Config,
	mu *sync.Mutex,
	logger *slog.Logger,
) (bool, bool) {
	path := filepath.Join(c.cfg.ArtworkDir, filename)

	if !optCfg.Enabled {
		if err := optimize.ValidateImage(path); err != nil {
			logger.Warn("skipping corrupt image", "file", filename, "error", err)
			mu.Lock()
			delete(localFiles, filename)
			mu.Unlock()
			return false, false
		}
		return false, true
	}

	newFilename, modified, err := optimize.OptimizeFile(path, optCfg, logger)
	if err != nil {
		logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		mu.Lock()
		delete(localFiles, filename)
		mu.Unlock()
		return false, false
	}

	if modified && newFilename != filename {
		c.UpdateMappings(filename, newFilename)
		mu.Lock()
		delete(localFiles, filename)
		localFiles[newFilename] = struct{}{}
		mu.Unlock()
	}

	return modified, true
}
