package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilterLocalFiles(t *testing.T) {
	local := map[string]struct{}{
		"c.jpg": {},
		"a.jpg": {},
		"d.jpg": {},
		"b.jpg": {},
	}

	// Case 1: Limit is greater or equal
	res := FilterLocalFiles(local, 10)
	if len(res) != 4 {
		t.Errorf("expected 4 files, got %d", len(res))
	}

	// Case 2: Limit is less
	res = FilterLocalFiles(local, 2)
	if len(res) != 2 {
		t.Errorf("expected 2 files, got %d", len(res))
	}

	// Verify deterministic alphabetical selection: "a.jpg", "b.jpg" should be kept
	if _, ok := res["a.jpg"]; !ok {
		t.Errorf("expected 'a.jpg' to be in filtered set")
	}
	if _, ok := res["b.jpg"]; !ok {
		t.Errorf("expected 'b.jpg' to be in filtered set")
	}
	if _, ok := res["c.jpg"]; ok {
		t.Errorf("did not expect 'c.jpg' to be in filtered set")
	}
}

func TestCapacityManager_LoadSave(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "capacity-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cm := NewCapacityManager(tempDir, "1.2.3.4")

	// Case 1: Load non-existent (default changed to MaxImages: 0, IsFull: false)
	state, err := cm.Load()
	if err != nil {
		t.Fatalf("expected no error loading non-existent file: %v", err)
	}
	if state.IsFull || state.MaxImages != 0 || state.SuccessStreak != 0 {
		t.Errorf("expected default values, got: %+v", state)
	}

	// Case 2: Save and Load
	saveState := &CapacityState{
		MaxImages:     42,
		IsFull:        true,
		SuccessStreak: 2,
	}
	if err := cm.Save(saveState); err != nil {
		t.Fatalf("failed to save capacity state: %v", err)
	}

	loadedState, err := cm.Load()
	if err != nil {
		t.Fatalf("failed to load capacity state: %v", err)
	}
	if !loadedState.IsFull || loadedState.MaxImages != 42 || loadedState.SuccessStreak != 2 {
		t.Errorf("expected saved values, got: %+v", loadedState)
	}

	// Case 3: Load invalid json
	invalidPath := filepath.Join(tempDir, "tv_1_2_3_4_capacity.json")
	if err := os.WriteFile(invalidPath, []byte("{invalid json"), 0o600); err != nil {
		t.Fatalf("failed to write invalid JSON: %v", err)
	}
	_, err = cm.Load()
	if err == nil {
		t.Errorf("expected error loading invalid JSON")
	}
}

func TestCapacityManager_RecordSuccess(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "capacity-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cm := NewCapacityManager(tempDir, "1.2.3.4")

	// Case 1: RecordSuccess when not full (no-op)
	state, err := cm.RecordSuccess()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.IsFull || state.SuccessStreak != 0 {
		t.Errorf("expected no-op when not full, got %+v", state)
	}

	// Case 2: RecordSuccess increments streak when full
	state.IsFull = true
	state.MaxImages = 50
	if err := cm.Save(state); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 9; i++ {
		state, err = cm.RecordSuccess()
		if err != nil {
			t.Fatal(err)
		}
		if !state.IsFull || state.SuccessStreak != i || state.MaxImages != 50 {
			t.Errorf("expected increment at step %d, got %+v", i, state)
		}
	}

	// Case 3: Reaches threshold and triggers recovery (IsFull becomes false, MaxImages bumps by delta)
	state, err = cm.RecordSuccess()
	if err != nil {
		t.Fatal(err)
	}
	if state.IsFull || state.SuccessStreak != 0 || state.MaxImages != 55 {
		t.Errorf("expected recovery trigger, got %+v", state)
	}
}

func TestCapacityManager_Errors(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "capacity-err-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Cause load error (e.g. read from a path where a parent directory is actually a file)
	dummyFile := filepath.Join(tempDir, "dummy_file")
	if err := os.WriteFile(dummyFile, []byte("xyz"), 0o600); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}

	cmBlocked := NewCapacityManager(dummyFile, "1.2.3.4")

	// 1. Load failure
	_, err = cmBlocked.Load()
	if err == nil {
		t.Errorf("expected load error when directory is a file")
	}

	// 2. Save failure
	err = cmBlocked.Save(&CapacityState{IsFull: true})
	if err == nil {
		t.Errorf("expected save error when directory is a file")
	}

	// 3. RecordSuccess failure on Load
	_, err = cmBlocked.RecordSuccess()
	if err == nil {
		t.Errorf("expected RecordSuccess to fail when Load fails")
	}

	// 4. RecordSuccess failure on Save
	cmWritable := NewCapacityManager(tempDir, "9.9.9.9")
	state := &CapacityState{IsFull: true, SuccessStreak: 5, MaxImages: 10}
	if err := cmWritable.Save(state); err != nil {
		t.Fatalf("failed to save initial state: %v", err)
	}

	_, err = cmWritable.Load()
	if err != nil {
		t.Fatalf("expected load to succeed: %v", err)
	}

	// Replace the capacity file with a directory to block future writes.
	filePath := cmWritable.path
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("failed to remove capacity file: %v", err)
	}
	if err := os.Mkdir(filePath, 0o700); err != nil {
		t.Fatalf("failed to create directory in place of file: %v", err)
	}
}
