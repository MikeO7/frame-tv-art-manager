package sources

import (
	"os"
	"path/filepath"
	"strings"
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
	identity      string
	err           error
}

func (c *ArtworkCatalog) isCacheValid() bool {
	info, statErr := os.Stat(c.artworkDir)
	if statErr != nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if info.ModTime().Equal(c.lastDirModTime) && len(c.hashIndex) > 0 {
		return true
	}
	c.lastDirModTime = info.ModTime()
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

	if !strings.Contains(filename, ".h_"+hash[:hashPrefixLen]) && !strings.Contains(filename, "__"+hash[:hashPrefixLen]) {
		ext := filepath.Ext(filename)
		newName := artwork.BuildHashName(identity, hash[:hashPrefixLen], ext)
		newPath := filepath.Join(c.artworkDir, newName)
		if err := os.Rename(path, newPath); err == nil {
			filename = newName
			path = newPath
			// Explicit chmod to 0o644 is required to override restrictive system umasks (e.g. 0077)
			// so files are readable over SMB/NFS network shares. Do NOT tighten to 0o600.
			_ = os.Chmod(path, 0o644)
			c.registerPrefix(cleanIdentity, filename)
			c.logger.Debug("migrated file to hash-based name", "original", identity, "hash", hash[:hashPrefixLen])
		}
	}

	c.mu.Lock()
	if existing, ok := c.hashIndex[hash]; ok {
		c.logger.Info("found existing duplicate content, removing", "file", filename, "matches", existing)
		_ = os.Remove(path)
	} else {
		c.hashIndex[hash] = filename
		c.catalog[filename] = struct{}{}
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
