package sources

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

// ArtworkCatalog manages local image files, content-based indexing,
// parallel image optimization, and catalog pruning.
type ArtworkCatalog struct {
	artworkDir string
	logger     *slog.Logger
	mu         sync.Mutex
	hashIndex  map[string]string
	prefixMap  map[string]string
	visited    map[string]bool
	catalog    map[string]struct{}
	cacheValid bool
}

// NoteImported records a source item already committed by the Collection
// Store. It changes only the in-memory source index.
func (c *ArtworkCatalog) NoteImported(identity string, item collection.Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hash := fmt.Sprintf("%x", item.Digest)
	cleanIdentity := artwork.StripIndexPrefix(identity)
	c.hashIndex[hash] = item.Name
	c.prefixMap[cleanIdentity] = item.Name
	c.catalog[item.Name] = struct{}{}
	c.visited[item.Name] = true
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
	c.catalog = make(map[string]struct{})
	c.cacheValid = false
}

// SupportedFiles returns supported image filenames from the catalog cache.
func (c *ArtworkCatalog) SupportedFiles() (map[string]struct{}, error) {
	c.mu.Lock()
	valid := c.cacheValid
	c.mu.Unlock()
	if !valid {
		if err := c.Rebuild(); err != nil {
			return nil, err
		}
	}

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

// MaxReached reports whether the complete Artwork Collection catalog has hit
// the configured limit. Operator artwork and sources not yet visited in the
// current cycle still consume capacity.
func (c *ArtworkCatalog) MaxReached(limit int) bool {
	if limit <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.catalog) >= limit
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
