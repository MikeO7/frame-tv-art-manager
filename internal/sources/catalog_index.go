package sources

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

const (
	// Worker bounds for concurrent catalog indexing.
	minCatalogWorkers = 1
	maxCatalogWorkers = 16
)

type indexEntry struct {
	filename      string
	hash          string
	cleanIdentity string
	err           error
}

func (c *ArtworkCatalog) resetState() {
	c.mu.Lock()
	c.hashIndex = make(map[string]string)
	c.prefixMap = make(map[string]string)
	c.catalog = make(map[string]struct{})
	c.cacheValid = false
	c.mu.Unlock()
}

func (c *ArtworkCatalog) processFilesConcurrent(entries []os.DirEntry) chan indexEntry {
	jobs := make(chan string, len(entries))
	results := make(chan indexEntry, len(entries))

	numWorkers := min(max(len(entries), minCatalogWorkers), maxCatalogWorkers)

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
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() ||
			!artwork.IsSupportedExtension(filepath.Ext(entry.Name())) {
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

func (c *ArtworkCatalog) processResult(res indexEntry) error {
	if res.err != nil {
		return res.err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, duplicate := c.hashIndex[res.hash]; duplicate {
		return nil
	}
	c.hashIndex[res.hash] = res.filename
	c.catalog[res.filename] = struct{}{}
	c.prefixMap[res.cleanIdentity] = res.filename
	return nil
}

// Rebuild scans the artwork directory and rebuilds hash and prefix indexes.
func (c *ArtworkCatalog) Rebuild() error {
	c.resetState()

	entries, err := os.ReadDir(c.artworkDir)
	if err != nil {
		return fmt.Errorf("read artwork directory: %w", err)
	}

	results := c.processFilesConcurrent(entries)

	indexed := make([]indexEntry, 0, len(entries))
	for res := range results {
		indexed = append(indexed, res)
	}
	sort.Slice(indexed, func(i, j int) bool { return indexed[i].filename < indexed[j].filename })

	var rebuildErrors []error
	for _, res := range indexed {
		if err := c.processResult(res); err != nil {
			rebuildErrors = append(rebuildErrors, err)
		}
	}
	if err := errors.Join(rebuildErrors...); err != nil {
		c.resetState()
		return fmt.Errorf("build artwork inventory: %w", err)
	}
	c.mu.Lock()
	c.cacheValid = true
	c.mu.Unlock()
	return nil
}

func (c *ArtworkCatalog) processFile(filename string) indexEntry {
	path := filepath.Join(c.artworkDir, filename)
	_, cleanIdentity, _ := artwork.ParseIdentity(filename)
	hash, err := fileHash(path)
	if err != nil {
		return indexEntry{err: fmt.Errorf("hash artwork %s: %w", filename, err)}
	}

	return indexEntry{
		filename:      filename,
		hash:          hash,
		cleanIdentity: cleanIdentity,
	}
}
