package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMapping_Lifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tvIP := "192.168.1.50"

	// Case 1: Load non-existent mapping
	m, err := LoadMapping(tempDir, tvIP)
	if err != nil {
		t.Fatalf("expected no error loading non-existent mapping: %v", err)
	}
	if len(m.AllContentIDs()) != 0 {
		t.Errorf("expected empty mapping, got %v", m.AllContentIDs())
	}

	// Case 2: Set, Get, Save, Load
	m.Set("monet.jpg", "MY_CONTENT_ID_1")
	if id, ok := m.GetContentID("monet.jpg"); !ok || id != "MY_CONTENT_ID_1" {
		t.Errorf("expected monet.jpg to have content ID MY_CONTENT_ID_1, got %s (ok=%t)", id, ok)
	}

	m2, err := LoadMapping(tempDir, tvIP)
	if err != nil {
		t.Fatalf("failed to load mapping: %v", err)
	}
	if id, ok := m2.GetContentID("monet.jpg"); !ok || id != "MY_CONTENT_ID_1" {
		t.Errorf("expected loaded mapping to contain monet.jpg -> MY_CONTENT_ID_1")
	}

	// Case 3: Rename
	renamed := m2.Rename("monet.jpg", "monet_new.jpg")
	if !renamed {
		t.Errorf("expected Rename to return true")
	}
	if _, ok := m2.GetContentID("monet.jpg"); ok {
		t.Errorf("expected old file name to be deleted after Rename")
	}
	if id, ok := m2.GetContentID("monet_new.jpg"); !ok || id != "MY_CONTENT_ID_1" {
		t.Errorf("expected new file name to have content ID, got %s (ok=%t)", id, ok)
	}

	// Rename non-existent
	renamedNonExistent := m2.Rename("nonexistent.jpg", "other.jpg")
	if renamedNonExistent {
		t.Errorf("expected Rename of nonexistent file to return false")
	}

	// Rename to empty
	m2.Set("to_delete.jpg", "ID2")
	renamedEmpty := m2.Rename("to_delete.jpg", "")
	if !renamedEmpty {
		t.Errorf("expected Rename to empty to return true")
	}
	if _, ok := m2.GetContentID("to_delete.jpg"); ok {
		t.Errorf("expected file to be deleted from map when renaming to empty")
	}

	// Case 4: Delete
	m2.Set("to_delete2.jpg", "ID3")
	m2.Delete("to_delete2.jpg")
	if _, ok := m2.GetContentID("to_delete2.jpg"); ok {
		t.Errorf("expected to_delete2.jpg to be deleted")
	}

	// Case 5: DeleteBatch
	m2.Set("b1.jpg", "ID_B1")
	m2.Set("b2.jpg", "ID_B2")
	m2.DeleteBatch([]string{"b1.jpg", "b2.jpg"})
	if _, ok := m2.GetContentID("b1.jpg"); ok {
		t.Errorf("expected b1.jpg to be deleted by batch")
	}
	if _, ok := m2.GetContentID("b2.jpg"); ok {
		t.Errorf("expected b2.jpg to be deleted by batch")
	}

	// Case 6: TrackedFilenames
	m2.Set("track.jpg", "TRACKED_ID")
	tracked := m2.TrackedFilenames()
	if _, ok := tracked["track.jpg"]; !ok {
		t.Errorf("expected track.jpg to be in TrackedFilenames")
	}
}

func TestMapping_ErrorsAndEdgeCases(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test invalid JSON load
	tvIP := "192.168.1.51"
	path := filepath.Join(tempDir, "tv_192_168_1_51_mapping.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0o600); err != nil {
		t.Fatalf("failed to write invalid JSON: %v", err)
	}
	_, err = LoadMapping(tempDir, tvIP)
	if err == nil {
		t.Errorf("expected load error on invalid JSON mapping")
	}

	// Test read error from a directory path that is actually a file
	dummyFile := filepath.Join(tempDir, "dummy_file")
	if err := os.WriteFile(dummyFile, []byte("xyz"), 0o600); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	_, err = LoadMapping(dummyFile, "1.2.3.4")
	if err == nil {
		t.Errorf("expected read error when directory is a file")
	}

	// Empty path Mapping saveLocked no-op
	emptyMapping := &Mapping{path: ""}
	if err := emptyMapping.Save(); err != nil {
		t.Errorf("expected Save to return nil when path is empty: %v", err)
	}

	// Test dir creation failure in saveLocked
	dummyFile2 := filepath.Join(tempDir, "dummy_file2")
	if err := os.WriteFile(dummyFile2, []byte("xyz"), 0o600); err != nil {
		t.Fatalf("failed to create dummy file: %v", err)
	}
	blockedMapping := &Mapping{
		path: filepath.Join(dummyFile2, "cannot_create_dir", "mapping.json"),
		data: map[string]string{"foo": "bar"},
	}
	if err := blockedMapping.Save(); err == nil {
		t.Errorf("expected error when directory creation is blocked by a file")
	}

	// Trigger slog.Error branches on Set, Delete, DeleteBatch, Rename
	blockedMapping.Set("another", "val")
	blockedMapping.Rename("foo", "newfoo")
	blockedMapping.Delete("newfoo")
	blockedMapping.DeleteBatch([]string{"another"})
}
