package sync

import (
	"encoding/json"
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
	if err != nil {
		if os.IsNotExist(err) {
			return &CapacityState{MaxImages: 0, IsFull: false, SuccessStreak: 0}, nil
		}
		return nil, fmt.Errorf("read capacity file: %w", err)
	}

	var state CapacityState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse capacity file: %w", err)
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

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal capacity state: %w", err)
	}

	return os.WriteFile(cm.path, raw, 0o600)
}

// FilterLocalFiles returns a subset of local files limited to MaxImages if the TV is full.
func FilterLocalFiles(localFiles map[string]struct{}, maxImages int) map[string]struct{} {
	if maxImages < 0 || len(localFiles) <= maxImages {
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
