package sources

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
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

// RegisterPrefix associates a source identity with a filename.
func (c *ArtworkCatalog) RegisterPrefix(identity, filename string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prefixMap[artwork.StripIndexPrefix(identity)] = filename
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
