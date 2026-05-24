package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Mapping persists the filename→content_id relationship for a single TV.
type Mapping struct {
	mu   sync.RWMutex
	path string
	data map[string]string // filename → content_id
}

// LoadMapping reads a mapping file from disk.
//
// Parameters:
//   - dir: The directory where the TV-specific mapping JSON files are stored.
//   - tvIP: The IPv4 address of the target TV (used to generate the unique filename).
//
// Returns:
//   - *Mapping: An instantiated, thread-safe mapping struct loaded with existing filename→content_id pairs.
//   - error:    Any file I/O error encountered during read/parse, excluding "file not found" (which returns an empty Mapping).
//
// Example:
//
//	mapping, err := sync.LoadMapping("/data/artwork", "192.168.1.150")
//	if err != nil {
//	    log.Fatal("Could not load mapping state:", err)
//	}
//	if contentID, exists := mapping.GetContentID("monet.jpg"); exists {
//	    fmt.Println("Found existing artwork:", contentID)
//	}
func LoadMapping(dir, tvIP string) (*Mapping, error) {
	safeIP := strings.ReplaceAll(tvIP, ".", "_")
	path := filepath.Clean(filepath.Join(dir, fmt.Sprintf("tv_%s_mapping.json", safeIP)))

	m := &Mapping{
		path: path,
		data: make(map[string]string),
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("read mapping %s: %w", path, err)
	}

	if err := json.Unmarshal(raw, &m.data); err != nil {
		return nil, fmt.Errorf("parse mapping %s: %w", path, err)
	}

	return m, nil
}

// Save writes the mapping to disk as formatted JSON.
func (m *Mapping) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("create mapping dir: %w", err)
	}

	raw, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mapping: %w", err)
	}

	return os.WriteFile(m.path, raw, 0o600)
}

// Set records a filename→content_id association.
func (m *Mapping) Set(filename string, contentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[filename] = contentID
}

// Delete removes a filename from the mapping.
func (m *Mapping) Delete(filename string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, filename)
}

// DeleteBatch removes multiple filenames from the mapping under a single lock.
func (m *Mapping) DeleteBatch(filenames []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, filename := range filenames {
		delete(m.data, filename)
	}
}

// Rename updates a filename in the mapping while preserving its content_id.
func (m *Mapping) Rename(oldName, newName string) bool {
	return m.renameInternal(oldName, newName)
}

func (m *Mapping) renameInternal(oldName, newName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.data[oldName]; ok {
		delete(m.data, oldName)
		m.data[newName] = id
		return true
	}
	return false
}

// GetContentID returns the content_id for a filename, and whether it exists.
func (m *Mapping) GetContentID(filename string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.data[filename]
	return id, ok
}

// GetFilename returns the filename for a content_id, and whether it exists.
func (m *Mapping) GetFilename(contentID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for f, id := range m.data {
		if id == contentID {
			return f, true
		}
	}
	return "", false
}

// AllContentIDs returns a copy of the full filename→content_id map.
func (m *Mapping) AllContentIDs() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.data))
	for k, v := range m.data {
		out[k] = v
	}
	return out
}

// TrackedFilenames returns the set of filenames that have known content IDs.
func (m *Mapping) TrackedFilenames() map[string]struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]struct{}, len(m.data))
	for k := range m.data {
		out[k] = struct{}{}
	}
	return out
}
