// Package sync implements the artwork synchronization engine that
// coordinates local filesystem scanning, TV communication, and state
// management across one or more Samsung Frame TVs.
package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
)

// Mapping persists the filename→content_id relationship for a single TV.
// The JSON file tracks which local filenames correspond to which content
// IDs on the TV, enabling accurate diffing on subsequent sync cycles.
//
// File format: { "sunset.jpg": "MY_F0001_abc123", ... }
type Mapping struct {
	mu   gosync.RWMutex
	path string
	data map[string]string // filename → content_id
}

// LoadMapping reads a mapping file from disk. If the file does not exist,
// returns an empty mapping that will be created on first Save().
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
			return m, nil // new mapping, will be created on Save
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

	if err := os.MkdirAll(filepath.Dir(m.path), 0755); err != nil { //nolint:gosec // Need inclusive permissions
		return fmt.Errorf("create mapping dir: %w", err)
	}

	raw, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mapping: %w", err)
	}

	return os.WriteFile(m.path, raw, 0644) //nolint:gosec // Need inclusive permissions
}

// Set records a filename→content_id association.
func (m *Mapping) Set(filename, contentID string) {
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
