package sources

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
)

var reManagedIndex = regexp.MustCompile(`^[0-9]{3}__`)

// ArtworkCatalog manages local image files, content-based indexing,
// parallel image optimization, and catalog pruning.
type ArtworkCatalog struct {
	artworkDir     string
	logger         *slog.Logger
	mu             sync.Mutex
	hashIndex      map[string]string
	prefixMap      map[string]string
	visited        map[string]bool
	catalog        map[string]struct{}
	lastDirModTime time.Time
}

// NewArtworkCatalog instantiates a new local catalog manager.
func NewArtworkCatalog(artworkDir string, logger *slog.Logger) *ArtworkCatalog {
	return &ArtworkCatalog{
		artworkDir: artworkDir,
		logger:     logger,
		hashIndex:  make(map[string]string),
		prefixMap:  make(map[string]string),
		visited:    make(map[string]bool),
		catalog:    make(map[string]struct{}),
	}
}

// InvalidateCache forces the next Rebuild or SupportedFiles call to rescan disk.
func (c *ArtworkCatalog) InvalidateCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastDirModTime = time.Time{}
	c.catalog = make(map[string]struct{})
}

// NoteFileRename updates catalog and prefix maps after an on-disk rename.
func (c *ArtworkCatalog) NoteFileRename(oldName, newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.catalog, oldName)
	c.catalog[newName] = struct{}{}
	for identity, filename := range c.prefixMap {
		if filename == oldName {
			c.prefixMap[identity] = newName
		}
	}
	for hash, filename := range c.hashIndex {
		if filename == oldName {
			c.hashIndex[hash] = newName
		}
	}
}

// SupportedFiles returns supported image filenames from the catalog cache.
func (c *ArtworkCatalog) SupportedFiles() (map[string]struct{}, error) {
	c.Rebuild()

	c.mu.Lock()
	defer c.mu.Unlock()

	info, err := os.Stat(c.artworkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artwork directory does not exist: %s", c.artworkDir)
		}
		return nil, fmt.Errorf("stat artwork dir %s: %w", c.artworkDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("artwork path is not a directory: %s", c.artworkDir)
	}

	out := make(map[string]struct{}, len(c.catalog))
	for name := range c.catalog {
		out[name] = struct{}{}
	}
	return out, nil
}

// ResetVisited clears visit tracking for a new sync cycle.
func (c *ArtworkCatalog) ResetVisited() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.visited = make(map[string]bool)
}

// MarkVisited records that a filename was seen during the current cycle.
func (c *ArtworkCatalog) MarkVisited(filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.visited[filename] = true
}

// LookupPrefix returns an existing filename for a source identity prefix.
func (c *ArtworkCatalog) LookupPrefix(identity string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	filename, ok := c.prefixMap[artwork.StripIndexPrefix(identity)]
	return filename, ok
}

// RegisterPrefix associates a source identity with a filename.
func (c *ArtworkCatalog) RegisterPrefix(identity, filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prefixMap[artwork.StripIndexPrefix(identity)] = filename
}

type indexEntry struct {
	filename      string
	hash          string
	cleanIdentity string
	identity      string
	err           error
}

// Rebuild scans the artwork directory and rebuilds hash and prefix indexes.
//
//nolint:gocognit,gocyclo,funlen // complexity justified for this domain-specific path
func (c *ArtworkCatalog) Rebuild() {
	info, statErr := os.Stat(c.artworkDir)
	if statErr == nil {
		c.mu.Lock()
		cached := info.ModTime().Equal(c.lastDirModTime) && len(c.hashIndex) > 0
		c.mu.Unlock()
		if cached {
			return
		}
		c.lastDirModTime = info.ModTime()
	}

	c.mu.Lock()
	c.hashIndex = make(map[string]string)
	c.prefixMap = make(map[string]string)
	c.catalog = make(map[string]struct{})
	c.mu.Unlock()

	entries, err := os.ReadDir(c.artworkDir)
	if err != nil {
		return
	}

	jobs := make(chan string, len(entries))
	results := make(chan indexEntry, len(entries))

	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 16 {
		numWorkers = 16
	}

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for filename := range jobs {
				results <- c.processFile(filename)
			}
		}()
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		jobs <- entry.Name()
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if res.err != nil {
			continue
		}

		filename := res.filename
		path := filepath.Join(c.artworkDir, filename)
		hash := res.hash
		identity := res.identity
		cleanIdentity := res.cleanIdentity

		c.RegisterPrefix(cleanIdentity, filename)

		if !strings.Contains(filename, ".h_"+hash[:12]) && !strings.Contains(filename, "__"+hash[:12]) {
			ext := filepath.Ext(filename)
			newName := artwork.BuildHashName(identity, hash[:12], ext)
			newPath := filepath.Join(c.artworkDir, newName)
			if err := os.Rename(path, newPath); err == nil {
				filename = newName
				path = newPath
				c.RegisterPrefix(cleanIdentity, filename)
			}
			c.logger.Debug("migrated file to hash-based name", "original", identity, "hash", hash[:12])
		}

		c.mu.Lock()
		if existing, ok := c.hashIndex[hash]; ok {
			c.logger.Info("found existing duplicate content, removing", "file", filename, "matches", existing)
			_ = os.Remove(path)
		} else {
			c.hashIndex[hash] = filename
			if artwork.IsSupportedExtension(filepath.Ext(filename)) {
				c.catalog[filename] = struct{}{}
			}
		}
		c.mu.Unlock()
	}
}

func (c *ArtworkCatalog) processFile(filename string) indexEntry {
	path := filepath.Join(c.artworkDir, filename)
	identity, cleanIdentity, hash := artwork.ParseIdentity(filename)

	if hash == "" {
		h, err := fileHash(path)
		if err != nil {
			return indexEntry{err: err}
		}
		hash = h
	}

	return indexEntry{
		filename:      filename,
		hash:          hash,
		cleanIdentity: cleanIdentity,
		identity:      identity,
	}
}

func fileHash(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// RegisterHash records a content hash. Returns existing filename and whether it was a duplicate.
func (c *ArtworkCatalog) RegisterHash(hash, filename string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, ok := c.hashIndex[hash]
	if ok {
		return existing, true
	}
	c.hashIndex[hash] = filename
	return "", false
}

// SetHash associates a hash with a filename, replacing any prior entry.
func (c *ArtworkCatalog) SetHash(hash, filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hashIndex[hash] = filename
}

// MaxReached reports whether the visited file count hit the configured limit.
func (c *ArtworkCatalog) MaxReached(limit int) bool {
	if limit <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.visited) >= limit
}

// UnusedManagedFiles returns managed source filenames not visited this cycle.
func (c *ArtworkCatalog) UnusedManagedFiles() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.artworkDir)
	if err != nil {
		return nil
	}

	var unused []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		if c.visited[filename] {
			continue
		}

		if reManagedIndex.MatchString(filename) {
			unused = append(unused, filename)
		}
	}

	return unused
}

// Optimize executes worker pool image resizing and other optimization processing.
//
//nolint:gocognit // complexity justified for this domain-specific path
func (c *ArtworkCatalog) Optimize(
	optCfg optimize.Config,
	onRename func(oldName, newName string),
) (int, error) {
	localFiles, err := c.SupportedFiles()
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

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				wasModified, ok := c.HandleSingleOptimization(j.filename, localFiles, optCfg, onRename, &mu)
				if ok && wasModified {
					atomic.AddInt64(&optimizedCount, 1)
				}
			}
		}()
	}

	wg.Wait()

	if optimizedCount > 0 {
		c.InvalidateCache()
	}

	return int(optimizedCount), nil
}

// HandleSingleOptimization handles resizing/validating a single image.
func (c *ArtworkCatalog) HandleSingleOptimization(
	filename string,
	localFiles map[string]struct{},
	optCfg optimize.Config,
	onRename func(oldName, newName string),
	mu *sync.Mutex,
) (bool, bool) {
	path := filepath.Join(c.artworkDir, filename)

	if !optCfg.Enabled {
		if err := optimize.ValidateImage(path); err != nil {
			c.logger.Warn("skipping corrupt image", "file", filename, "error", err)
			mu.Lock()
			delete(localFiles, filename)
			mu.Unlock()
			return false, false
		}
		return false, true
	}

	newFilename, modified, err := optimize.OptimizeFile(path, optCfg, c.logger)
	if err != nil {
		c.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		mu.Lock()
		delete(localFiles, filename)
		mu.Unlock()
		return false, false
	}

	if modified && newFilename != filename {
		if onRename != nil {
			onRename(filename, newFilename)
		}
		c.NoteFileRename(filename, newFilename)
		mu.Lock()
		delete(localFiles, filename)
		localFiles[newFilename] = struct{}{}
		mu.Unlock()
	}

	return modified, true
}
