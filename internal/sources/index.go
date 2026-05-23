// Package sources handles downloading images from URLs and Unsplash
// for use as Samsung Frame TV artwork.
package sources

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

// ArtworkIndex tracks content hashes and source identity prefixes for deduplication.
type ArtworkIndex struct {
	artworkDir     string
	logger         *slog.Logger
	hashIndex      map[string]string
	prefixMap      map[string]string
	visited        map[string]bool
	catalog        map[string]struct{}
	lastDirModTime time.Time
	mu             sync.Mutex
}

// NewArtworkIndex creates an empty artwork index for a directory.
func NewArtworkIndex(artworkDir string, logger *slog.Logger) *ArtworkIndex {
	return &ArtworkIndex{
		artworkDir: artworkDir,
		logger:     logger,
		hashIndex:  make(map[string]string),
		prefixMap:  make(map[string]string),
		visited:    make(map[string]bool),
		catalog:    make(map[string]struct{}),
	}
}

// InvalidateCache forces the next Rebuild or SupportedFiles call to rescan disk.
func (idx *ArtworkIndex) InvalidateCache() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.lastDirModTime = time.Time{}
	idx.catalog = make(map[string]struct{})
}

// NoteFileRename updates catalog and prefix maps after an on-disk rename.
func (idx *ArtworkIndex) NoteFileRename(oldName, newName string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.catalog, oldName)
	idx.catalog[newName] = struct{}{}
	for identity, filename := range idx.prefixMap {
		if filename == oldName {
			idx.prefixMap[identity] = newName
		}
	}
	for hash, filename := range idx.hashIndex {
		if filename == oldName {
			idx.hashIndex[hash] = newName
		}
	}
}

// SupportedFiles returns supported image filenames from the catalog cache.
func (idx *ArtworkIndex) SupportedFiles() (map[string]struct{}, error) {
	idx.Rebuild()

	idx.mu.Lock()
	defer idx.mu.Unlock()

	info, err := os.Stat(idx.artworkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artwork directory does not exist: %s", idx.artworkDir)
		}
		return nil, fmt.Errorf("stat artwork dir %s: %w", idx.artworkDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("artwork path is not a directory: %s", idx.artworkDir)
	}

	out := make(map[string]struct{}, len(idx.catalog))
	for name := range idx.catalog {
		out[name] = struct{}{}
	}
	return out, nil
}

// ResetVisited clears visit tracking for a new sync cycle.
func (idx *ArtworkIndex) ResetVisited() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.visited = make(map[string]bool)
}

// MarkVisited records that a filename was seen during the current cycle.
func (idx *ArtworkIndex) MarkVisited(filename string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.visited[filename] = true
}

// LookupPrefix returns an existing filename for a source identity prefix.
func (idx *ArtworkIndex) LookupPrefix(identity string) (string, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	filename, ok := idx.prefixMap[artwork.StripIndexPrefix(identity)]
	return filename, ok
}

// RegisterPrefix associates a source identity with a filename.
func (idx *ArtworkIndex) RegisterPrefix(identity, filename string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.prefixMap[artwork.StripIndexPrefix(identity)] = filename
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
func (idx *ArtworkIndex) Rebuild() {
	info, statErr := os.Stat(idx.artworkDir)
	if statErr == nil {
		idx.mu.Lock()
		cached := info.ModTime().Equal(idx.lastDirModTime) && len(idx.hashIndex) > 0
		idx.mu.Unlock()
		if cached {
			return
		}
		idx.lastDirModTime = info.ModTime()
	}

	idx.mu.Lock()
	idx.hashIndex = make(map[string]string)
	idx.prefixMap = make(map[string]string)
	idx.catalog = make(map[string]struct{})
	idx.mu.Unlock()

	entries, err := os.ReadDir(idx.artworkDir)
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
				results <- idx.processFile(filename)
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
		path := filepath.Join(idx.artworkDir, filename)
		hash := res.hash
		identity := res.identity
		cleanIdentity := res.cleanIdentity

		idx.RegisterPrefix(cleanIdentity, filename)

		if !strings.Contains(filename, ".h_"+hash[:12]) && !strings.Contains(filename, "__"+hash[:12]) {
			ext := filepath.Ext(filename)
			newName := artwork.BuildHashName(identity, hash[:12], ext)
			newPath := filepath.Join(idx.artworkDir, newName)
			if err := os.Rename(path, newPath); err == nil {
				filename = newName
				path = newPath
				idx.RegisterPrefix(cleanIdentity, filename)
			}
			idx.logger.Debug("migrated file to hash-based name", "original", identity, "hash", hash[:12])
		}

		idx.mu.Lock()
		if existing, ok := idx.hashIndex[hash]; ok {
			idx.logger.Info("found existing duplicate content, removing", "file", filename, "matches", existing)
			_ = os.Remove(path)
		} else {
			idx.hashIndex[hash] = filename
			if artwork.IsSupportedExtension(filepath.Ext(filename)) {
				idx.catalog[filename] = struct{}{}
			}
		}
		idx.mu.Unlock()
	}
}

func (idx *ArtworkIndex) processFile(filename string) indexEntry {
	path := filepath.Join(idx.artworkDir, filename)
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
func (idx *ArtworkIndex) RegisterHash(hash, filename string) (existing string, duplicate bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	existing, ok := idx.hashIndex[hash]
	if ok {
		return existing, true
	}
	idx.hashIndex[hash] = filename
	return "", false
}

// SetHash associates a hash with a filename, replacing any prior entry.
func (idx *ArtworkIndex) SetHash(hash, filename string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.hashIndex[hash] = filename
}

// MaxReached reports whether the visited file count hit the configured limit.
func (idx *ArtworkIndex) MaxReached(limit int) bool {
	if limit <= 0 {
		return false
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return len(idx.visited) >= limit
}

// UnusedManagedFiles returns managed source filenames not visited this cycle.
func (idx *ArtworkIndex) UnusedManagedFiles() []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	entries, err := os.ReadDir(idx.artworkDir)
	if err != nil {
		return nil
	}

	var unused []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		if idx.visited[filename] {
			continue
		}

		isManaged := reManagedIndex.MatchString(filename)

		if isManaged {
			unused = append(unused, filename)
		}
	}

	return unused
}

// SourceLoader downloads remote artwork into the local collection.
type SourceLoader interface {
	Sync() (downloaded int, err error)
}

// Ensure Loader implements SourceLoader.
var _ SourceLoader = (*Loader)(nil)
