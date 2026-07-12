package sync

import (
	"encoding/json"
	"errors"
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

var (
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	marshalMappingState = func(state interface{}) ([]byte, error) {
		return json.MarshalIndent(state, "", "  ")
	}
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	createStateTempFile = os.CreateTemp
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	chmodStateFile = func(file *os.File, perm os.FileMode) error { return file.Chmod(perm) }
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	writeStateFile = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	syncStateFile = func(file *os.File) error { return file.Sync() }
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	closeStateFileHandle = func(file *os.File) error { return file.Close() }
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	openStateDirectory = os.Open
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	syncStateDirectory = func(file *os.File) error { return file.Sync() }
	//nolint:gochecknoglobals // fault-injection seam for persistence error tests
	closeStateDirectory = func(file *os.File) error { return file.Close() }
)

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

	raw, err := marshalMappingState(m.data)
	if err != nil {
		return fmt.Errorf("marshal mapping: %w", err)
	}

	return atomicWriteWithBackup(m.path, raw)
}

func atomicWriteWithBackup(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil {
		if err := atomicReplace(path+".bak", current); err != nil {
			return fmt.Errorf("write state backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read state for backup: %w", err)
	}
	if err := atomicReplace(path, data); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func atomicReplace(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := createStateTempFile(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := chmodStateFile(tmp, 0o600); err != nil {
		return closeStateFile(tmp, fmt.Errorf("chmod state temporary file: %w", err))
	}
	if _, err := writeStateFile(tmp, data); err != nil {
		return closeStateFile(tmp, fmt.Errorf("write state temporary file: %w", err))
	}
	if err := syncStateFile(tmp); err != nil {
		return closeStateFile(tmp, fmt.Errorf("sync state temporary file: %w", err))
	}
	if err := closeStateFileHandle(tmp); err != nil {
		return fmt.Errorf("close state temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename state temporary file: %w", err)
	}
	d, err := openStateDirectory(dir)
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	if err := syncStateDirectory(d); err != nil {
		return closeStateFile(d, fmt.Errorf("sync state directory: %w", err))
	}
	if err := closeStateDirectory(d); err != nil {
		return fmt.Errorf("close state directory: %w", err)
	}
	return nil
}

func closeStateFile(file *os.File, operationErr error) error {
	if err := file.Close(); err != nil {
		return errors.Join(operationErr, fmt.Errorf("close state file: %w", err))
	}
	return operationErr
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
