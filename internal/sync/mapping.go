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
		backupRaw, backupErr := os.ReadFile(path + ".bak")
		if backupErr != nil || json.Unmarshal(backupRaw, &m.data) != nil {
			return nil, fmt.Errorf("parse mapping %s and recover backup: %w", path, err)
		}
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

	return atomicWriteWithBackup(m.path, raw, 0o600)
}

func atomicWriteWithBackup(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mapping-*.tmp")
	if err != nil {
		return fmt.Errorf("create mapping temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod mapping temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write mapping temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync mapping temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close mapping temporary file: %w", err)
	}
	if current, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", current, perm); err != nil { //nolint:gosec // path is derived from validated internal state
			return fmt.Errorf("write mapping backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read mapping for backup: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace mapping: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open mapping directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync mapping directory: %w", err)
	}
	return nil
}

// Save writes the mapping to disk as formatted JSON.
func (m *Mapping) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

// Set records a filename→content_id association.
func (m *Mapping) Set(filename string, contentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, existed := m.data[filename]
	m.data[filename] = contentID
	if err := m.saveLocked(); err != nil {
		if existed {
			m.data[filename] = old
		} else {
			delete(m.data, filename)
		}
		return err
	}
	return nil
}

// Delete removes a filename from the mapping.
func (m *Mapping) Delete(filename string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, existed := m.data[filename]
	delete(m.data, filename)
	if err := m.saveLocked(); err != nil {
		if existed {
			m.data[filename] = old
		}
		return err
	}
	return nil
}

// DeleteBatch removes multiple filenames from the mapping under a single lock.
func (m *Mapping) DeleteBatch(filenames []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := make(map[string]string, len(filenames))
	for _, filename := range filenames {
		if value, ok := m.data[filename]; ok {
			old[filename] = value
		}
		delete(m.data, filename)
	}
	if err := m.saveLocked(); err != nil {
		for filename, value := range old {
			m.data[filename] = value
		}
		return err
	}
	return nil
}

// Rename updates a filename in the mapping while preserving its content_id.
func (m *Mapping) Rename(oldName, newName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.data[oldName]; ok {
		delete(m.data, oldName)
		if newName != "" {
			m.data[newName] = id
		}
		if err := m.saveLocked(); err != nil {
			delete(m.data, newName)
			m.data[oldName] = id
			return false, err
		}
		return true, nil
	}
	return false, nil
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
