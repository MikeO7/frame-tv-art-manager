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
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
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
	if newName != "" {
		c.catalog[newName] = struct{}{}
	}
	for identity, filename := range c.prefixMap {
		if filename == oldName {
			if newName != "" {
				c.prefixMap[identity] = newName
			} else {
				delete(c.prefixMap, identity)
			}
		}
	}
	for hash, filename := range c.hashIndex {
		if filename == oldName {
			if newName != "" {
				c.hashIndex[hash] = newName
			} else {
				delete(c.hashIndex, hash)
			}
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

// registerPrefix associates a source identity with a filename.
func (c *ArtworkCatalog) registerPrefix(identity, filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prefixMap[artwork.StripIndexPrefix(identity)] = filename
}

// registerHash records a content hash. Returns existing filename and whether it was a duplicate.
func (c *ArtworkCatalog) registerHash(hash, filename string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	existing, ok := c.hashIndex[hash]
	if ok {
		return existing, true
	}
	c.hashIndex[hash] = filename
	return "", false
}

// setHash associates a hash with a filename, replacing any prior entry.
func (c *ArtworkCatalog) setHash(hash, filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hashIndex[hash] = filename
}

// RegisterDownload hashes a downloaded file, checks for duplicates,
// renames it to a hash-based filename, registers it in index maps, and marks it visited.
// It returns the final filename, whether it was newly added (not a duplicate), and any error.
func (c *ArtworkCatalog) RegisterDownload(tempPath, filename, identity string) (string, bool, error) {
	hash, err := fileHash(tempPath)
	if err != nil {
		return "", false, fmt.Errorf("hash file: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if this content is already in the catalog
	if existing, duplicate := c.hashIndex[hash]; duplicate {
		c.visited[existing] = true
		_ = os.Remove(tempPath)
		return existing, false, nil
	}

	ext := filepath.Ext(filename)
	cleanIdentity := artwork.StripIndexPrefix(identity)

	// Normalize identity by removing potential .h_ suffix if present
	if parts := strings.Split(cleanIdentity, ".h_"); len(parts) == 2 {
		cleanIdentity = parts[0]
	}

	finalName := artwork.BuildHashName(cleanIdentity, hash[:12], ext)
	finalPath := filepath.Join(c.artworkDir, finalName)

	if err := os.Rename(tempPath, finalPath); err != nil {
		c.hashIndex[hash] = filename
		c.prefixMap[cleanIdentity] = filename
		c.catalog[filename] = struct{}{}
		c.visited[filename] = true
		return filename, true, fmt.Errorf("rename to final hash name: %w", err)
	}
	// Explicit chmod to 0o644 is required to override restrictive system umasks (e.g. 0077)
	// so files are readable over SMB/NFS network shares. Do NOT tighten to 0o600.
	_ = os.Chmod(finalPath, 0o644)

	c.hashIndex[hash] = finalName
	c.prefixMap[cleanIdentity] = finalName
	c.catalog[finalName] = struct{}{}
	c.visited[finalName] = true

	return finalName, true, nil
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

type indexEntry struct {
	filename      string
	hash          string
	cleanIdentity string
	identity      string
	err           error
}

func (c *ArtworkCatalog) isCacheValid() bool {
	info, statErr := os.Stat(c.artworkDir)
	if statErr == nil {
		c.mu.Lock()
		cached := info.ModTime().Equal(c.lastDirModTime) && len(c.hashIndex) > 0
		c.mu.Unlock()
		if cached {
			return true
		}
		c.lastDirModTime = info.ModTime()
	}
	return false
}

func (c *ArtworkCatalog) resetState() {
	c.mu.Lock()
	c.hashIndex = make(map[string]string)
	c.prefixMap = make(map[string]string)
	c.catalog = make(map[string]struct{})
	c.mu.Unlock()
}

func (c *ArtworkCatalog) processFilesConcurrent(entries []os.DirEntry) chan indexEntry {
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

	return results
}

func (c *ArtworkCatalog) processResult(res indexEntry) {
	if res.err != nil {
		return
	}

	filename := res.filename
	path := filepath.Join(c.artworkDir, filename)
	hash := res.hash
	identity := res.identity
	cleanIdentity := res.cleanIdentity

	c.registerPrefix(cleanIdentity, filename)

	if !strings.Contains(filename, ".h_"+hash[:12]) && !strings.Contains(filename, "__"+hash[:12]) {
		ext := filepath.Ext(filename)
		newName := artwork.BuildHashName(identity, hash[:12], ext)
		newPath := filepath.Join(c.artworkDir, newName)
		if err := os.Rename(path, newPath); err == nil {
			filename = newName
			path = newPath
			// Explicit chmod to 0o644 is required to override restrictive system umasks (e.g. 0077)
			// so files are readable over SMB/NFS network shares. Do NOT tighten to 0o600.
			_ = os.Chmod(path, 0o644)
			c.registerPrefix(cleanIdentity, filename)
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

// Rebuild scans the artwork directory and rebuilds hash and prefix indexes.
func (c *ArtworkCatalog) Rebuild() {
	if c.isCacheValid() {
		return
	}

	c.resetState()

	entries, err := os.ReadDir(c.artworkDir)
	if err != nil {
		return
	}

	results := c.processFilesConcurrent(entries)

	for res := range results {
		c.processResult(res)
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
