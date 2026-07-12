package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// CapacityState tracks the capacity limit of a TV.
type CapacityState struct {
	MaxImages     int  `json:"max_images"`
	IsFull        bool `json:"is_full"`
	SuccessStreak int  `json:"success_streak"`
}

// capacityRecoveryThreshold is the number of consecutive clean syncs
// before the system attempts to grow the capacity limit.
const capacityRecoveryThreshold = 10

// capacityRecoveryDelta is how many additional images to attempt
// when probing for recovered space.
const capacityRecoveryDelta = 5

// CapacityManager manages the persistence of CapacityState.
type CapacityManager struct {
	mu   sync.RWMutex
	path string
}

var capacityMarshalState = func(state *CapacityState) ([]byte, error) {
	return json.MarshalIndent(state, "", "  ")
}

// NewCapacityManager creates a manager for the given TV IP.
func NewCapacityManager(dir, tvIP string) *CapacityManager {
	safeIP := strings.ReplaceAll(tvIP, ".", "_")
	path := filepath.Clean(filepath.Join(dir, fmt.Sprintf("tv_%s_capacity.json", safeIP)))
	return &CapacityManager{
		path: path,
	}
}

// Load retrieves the capacity state from disk.
func (cm *CapacityManager) Load() (*CapacityState, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	raw, err := os.ReadFile(cm.path)
	if os.IsNotExist(err) {
		return loadCapacityBackup(cm.path, true)
	}
	if err != nil {
		return nil, fmt.Errorf("read capacity file: %w", err)
	}

	state, err := decodeCapacityState(raw, "file")
	if err != nil {
		backupState, backupErr := loadCapacityBackup(cm.path, false)
		if backupErr != nil {
			return nil, errors.Join(err, backupErr)
		}
		return backupState, nil
	}
	return state, nil
}

func loadCapacityBackup(path string, defaultWhenMissing bool) (*CapacityState, error) {
	backupRaw, err := os.ReadFile(path + ".bak")
	if defaultWhenMissing && os.IsNotExist(err) {
		return &CapacityState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read capacity backup: %w", err)
	}
	return decodeCapacityState(backupRaw, "backup")
}

func decodeCapacityState(raw []byte, source string) (*CapacityState, error) {
	var state CapacityState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse capacity %s: %w", source, err)
	}
	return &state, nil
}

// RecordSuccess increments the success streak and triggers recovery
// if the threshold is met.
func (cm *CapacityManager) RecordSuccess() (*CapacityState, error) {
	state, err := cm.Load()
	if err != nil {
		return nil, err
	}
	if !state.IsFull {
		return state, nil
	}

	state.SuccessStreak++
	if state.SuccessStreak >= capacityRecoveryThreshold {
		state.MaxImages += capacityRecoveryDelta
		state.IsFull = false
		state.SuccessStreak = 0
	}

	if err := cm.Save(state); err != nil {
		return nil, err
	}
	return state, nil
}

// Save persists the capacity state to disk.
func (cm *CapacityManager) Save(state *CapacityState) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(cm.path), 0o700); err != nil {
		return fmt.Errorf("create capacity dir: %w", err)
	}

	raw, err := capacityMarshalState(state)
	if err != nil {
		return fmt.Errorf("marshal capacity state: %w", err)
	}

	if err := atomicWriteWithBackup(cm.path, raw, 0o600); err != nil {
		return fmt.Errorf("write capacity state: %w", err)
	}
	return nil
}

// FilterLocalFiles returns a subset of local files limited to MaxImages if the TV is full.
func FilterLocalFiles(localFiles map[string]struct{}, maxImages int) map[string]struct{} {
	if maxImages <= 0 || len(localFiles) <= maxImages {
		return localFiles
	}

	// Extract and sort keys for deterministic selection
	keys := make([]string, 0, len(localFiles))
	for k := range localFiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	filtered := make(map[string]struct{}, maxImages)
	for i := 0; i < maxImages; i++ {
		filtered[keys[i]] = struct{}{}
	}
	return filtered
}
