package sources

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

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
