package sync

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// LoadMapping reads the per-TV filename→content_id mapping from disk, keyed by
// the TV's IP. A missing file yields an empty (but usable) mapping; a malformed
// file returns an error.
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

func (m *Mapping) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("create mapping dir: %w", err)
	}

	raw, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mapping: %w", err)
	}

	return os.WriteFile(m.path, raw, 0o600)
}

// Save writes the mapping to disk as formatted JSON.
func (m *Mapping) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

// Set records a filename→content_id association.
func (m *Mapping) Set(filename string, contentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[filename] = contentID
	if err := m.saveLocked(); err != nil {
		slog.Error("failed to auto-save mapping Set", "file", filename, "error", err)
	}
}

// Delete removes a filename from the mapping.
func (m *Mapping) Delete(filename string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, filename)
	if err := m.saveLocked(); err != nil {
		slog.Error("failed to auto-save mapping Delete", "file", filename, "error", err)
	}
}

// DeleteBatch removes multiple filenames from the mapping under a single lock.
func (m *Mapping) DeleteBatch(filenames []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, filename := range filenames {
		delete(m.data, filename)
	}
	if err := m.saveLocked(); err != nil {
		slog.Error("failed to auto-save mapping DeleteBatch", "error", err)
	}
}

// Rename updates a filename in the mapping while preserving its content_id.
func (m *Mapping) Rename(oldName, newName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.data[oldName]; ok {
		delete(m.data, oldName)
		if newName != "" {
			m.data[newName] = id
		}
		if err := m.saveLocked(); err != nil {
			slog.Error("failed to auto-save mapping Rename", "old", oldName, "new", newName, "error", err)
		}
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
