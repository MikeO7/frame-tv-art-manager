//nolint:errcheck // individual persistence-error cases assert results separately
package sync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	renamed, err := m2.Rename("monet.jpg", "monet_new.jpg")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
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
	renamedNonExistent, err := m2.Rename("nonexistent.jpg", "other.jpg")
	if err != nil {
		t.Fatalf("Rename nonexistent: %v", err)
	}
	if renamedNonExistent {
		t.Errorf("expected Rename of nonexistent file to return false")
	}

	// Rename to empty
	m2.Set("to_delete.jpg", "ID2")
	renamedEmpty, err := m2.Rename("to_delete.jpg", "")
	if err != nil {
		t.Fatalf("Rename empty: %v", err)
	}
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

func TestMapping_LoadFromCorruptButRecoverableFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tvIP := "192.168.1.52"
	path := filepath.Join(tempDir, "tv_192_168_1_52_mapping.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0o600); err != nil {
		t.Fatalf("failed to write invalid mapping: %v", err)
	}

	backup := map[string]string{"restored.jpg": "restored-id"}
	backupData, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("failed to marshal backup mapping: %v", err)
	}
	if err := os.WriteFile(path+".bak", backupData, 0o600); err != nil {
		t.Fatalf("failed to write mapping backup: %v", err)
	}

	m, err := LoadMapping(tempDir, tvIP)
	if err != nil {
		t.Fatalf("LoadMapping() = %v", err)
	}
	id, ok := m.GetContentID("restored.jpg")
	if !ok || id != "restored-id" {
		t.Fatalf("restored content = %s (ok=%t)", id, ok)
	}
}

func TestMapping_AtomicWriteErrorBranches(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	blocker := filepath.Join(tempDir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create blocker file: %v", err)
	}

	if err := atomicWriteWithBackup(filepath.Join(blocker, "mapping.json"), []byte("payload")); err == nil {
		t.Fatal("expected atomicWriteWithBackup error")
	}
	if err := atomicReplace(filepath.Join(blocker, "mapping.json"), []byte("payload")); err == nil {
		t.Fatal("expected atomicReplace error")
	}

	blockerDir := t.TempDir()
	path := filepath.Join(blockerDir, "mapping.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("failed to seed mapping file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(blockerDir, "mapping.json.bak"), 0o700); err != nil {
		t.Fatalf("failed to create backup directory: %v", err)
	}
	if err := atomicWriteWithBackup(path, []byte(`{"kept":"value"}`)); err == nil {
		t.Fatal("expected atomicWriteWithBackup backup replace error")
	}

	badFinal := filepath.Join(blockerDir, "final.json")
	if err := os.Mkdir(badFinal, 0o700); err != nil {
		t.Fatalf("failed to create final destination directory: %v", err)
	}
	if err := atomicReplace(badFinal, []byte("payload")); err == nil {
		t.Fatal("expected atomicReplace final destination error")
	}
}

func TestMapping_CloseStateFileBehavior(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	f, err := os.CreateTemp(tempDir, "state")
	if err != nil {
		t.Fatalf("failed to create temp state file: %v", err)
	}

	sentinel := errors.New("operation failed")
	if err := closeStateFile(f, sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("closeStateFile(uncached close) = %v, want %v", err, sentinel)
	}

	secondErr := closeStateFile(f, sentinel)
	if secondErr == nil {
		t.Fatal("expected closeStateFile second close to report close error")
	}
	if !strings.Contains(secondErr.Error(), "close state file") {
		t.Fatalf("second closeStateFile() = %v", secondErr)
	}
}

func TestMapping_MutationRollbackOnSaveFailure(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	blocker := filepath.Join(tempDir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create blocker file: %v", err)
	}

	m := &Mapping{
		path: filepath.Join(blocker, "mapping.json"),
		data: map[string]string{"kept": "keep-id"},
	}

	if err := m.Set("kept", "new-id"); err == nil {
		t.Fatal("expected Set to fail when mapping write fails")
	}
	if got, ok := m.GetContentID("kept"); !ok || got != "keep-id" {
		t.Fatalf("existing mapping should rollback: %q %t", got, ok)
	}
	if err := m.Set("added", "new-id"); err == nil {
		t.Fatal("expected Set insert failure when mapping write fails")
	}
	if _, ok := m.GetContentID("added"); ok {
		t.Fatalf("failed Set insert should rollback new entry")
	}

	if err := m.Delete("kept"); err == nil {
		t.Fatal("expected Delete failure when mapping write fails")
	}
	if _, ok := m.GetContentID("kept"); !ok || m.data["kept"] != "keep-id" {
		t.Fatalf("existing mapping should rollback after failed Delete")
	}

	m.data["kept"] = "keep-id"
	if err := m.DeleteBatch([]string{"kept"}); err == nil {
		t.Fatal("expected DeleteBatch failure when mapping write fails")
	}
	if _, ok := m.GetContentID("kept"); !ok {
		t.Fatal("DeleteBatch failure should preserve existing entries")
	}
}

func TestMapping_SaveWritesPersistedMapping(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	m := &Mapping{
		path: filepath.Join(tempDir, "nested", "state", "mapping.json"),
		data: map[string]string{"monet.jpg": "ID_MONET", "vangogh.jpg": "ID_VANGOGH"},
	}

	if err := m.Save(); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	raw, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatalf("failed to read saved mapping: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("saved mapping is not valid JSON: %v", err)
	}
	if len(got) != len(m.data) {
		t.Fatalf("saved mapping has wrong size: got %d, want %d", len(got), len(m.data))
	}
	for filename, expectedID := range m.data {
		if gotID := got[filename]; gotID != expectedID {
			t.Errorf("mapped %q = %q, want %q", filename, gotID, expectedID)
		}
	}

	info, err := os.Stat(m.path)
	if err != nil {
		t.Fatalf("failed to stat mapping: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mapping permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestMapping_AtomicWriteWithBackupCreatesStateAndBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	path := filepath.Join(tempDir, "mapping.json")
	if err := os.WriteFile(path, []byte(`{"kept":"old"}`), 0o600); err != nil {
		t.Fatalf("failed to seed state file: %v", err)
	}

	next := []byte(`{"kept":"new"}`)
	if err := atomicWriteWithBackup(path, next); err != nil {
		t.Fatalf("atomicWriteWithBackup() = %v", err)
	}

	var (
		got     map[string]string
		gotBack map[string]string
	)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("state is not valid JSON: %v", err)
	}
	if got["kept"] != "new" {
		t.Fatalf("state should be updated, got %q", got["kept"])
	}

	backupRaw, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("failed to read backup file: %v", err)
	}
	if err := json.Unmarshal(backupRaw, &gotBack); err != nil {
		t.Fatalf("backup is not valid JSON: %v", err)
	}
	if gotBack["kept"] != "old" {
		t.Fatalf("backup should contain old data, got %q", gotBack["kept"])
	}
}

func TestAtomicReplace_ErrorBranches(t *testing.T) {
	tempDir := t.TempDir()

	origCreateStateTempFile := createStateTempFile
	origChmodStateFile := chmodStateFile
	origWriteStateFile := writeStateFile
	origSyncStateFile := syncStateFile
	origCloseStateFileHandle := closeStateFileHandle
	origOpenStateDirectory := openStateDirectory
	origSyncStateDirectory := syncStateDirectory
	origCloseStateDirectory := closeStateDirectory

	t.Cleanup(func() {
		createStateTempFile = origCreateStateTempFile
		chmodStateFile = origChmodStateFile
		writeStateFile = origWriteStateFile
		syncStateFile = origSyncStateFile
		closeStateFileHandle = origCloseStateFileHandle
		openStateDirectory = origOpenStateDirectory
		syncStateDirectory = origSyncStateDirectory
		closeStateDirectory = origCloseStateDirectory
	})

	createFailPath := filepath.Join(tempDir, "create_fail.json")
	createStateTempFile = func(string, string) (*os.File, error) {
		return nil, errors.New("create state temp failed")
	}
	if err := atomicReplace(createFailPath, []byte("payload")); err == nil || !strings.Contains(err.Error(), "create state temporary file") {
		t.Fatalf("expected create temporary file failure, got %v", err)
	}

	// Reuse working filesystem for the remaining branches.
	makeTemp := func(_ string, _ string) (*os.File, error) {
		return os.CreateTemp(tempDir, ".state-*.tmp")
	}
	createStateTempFile = makeTemp

	chmodStateFile = func(*os.File, os.FileMode) error {
		return errors.New("chmod state temporary file failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "chmod_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "chmod state temporary file") {
		t.Fatalf("expected chmod failure, got %v", err)
	}

	chmodStateFile = origChmodStateFile
	writeStateFile = func(*os.File, []byte) (int, error) {
		return 0, errors.New("write state temporary file failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "write_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "write state temporary file") {
		t.Fatalf("expected write failure, got %v", err)
	}

	writeStateFile = origWriteStateFile
	syncStateFile = func(*os.File) error {
		return errors.New("sync state temporary file failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "sync_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "sync state temporary file") {
		t.Fatalf("expected sync failure, got %v", err)
	}

	syncStateFile = origSyncStateFile
	closeStateFileHandle = func(*os.File) error {
		return errors.New("close state temporary file failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "close_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "close state temporary file") {
		t.Fatalf("expected close temp file failure, got %v", err)
	}

	closeStateFileHandle = origCloseStateFileHandle
	openStateDirectory = func(string) (*os.File, error) {
		return nil, errors.New("open state directory failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "opendir_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "open state directory") {
		t.Fatalf("expected open directory failure, got %v", err)
	}

	openStateDirectory = origOpenStateDirectory
	syncStateDirectory = func(*os.File) error {
		return errors.New("sync state directory failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "syncdir_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "sync state directory") {
		t.Fatalf("expected sync directory failure, got %v", err)
	}

	syncStateDirectory = origSyncStateDirectory
	closeStateDirectory = func(*os.File) error {
		return errors.New("close state directory failed")
	}
	if err := atomicReplace(filepath.Join(tempDir, "closedir_fail.json"), []byte("payload")); err == nil || !strings.Contains(err.Error(), "close state directory") {
		t.Fatalf("expected close directory failure, got %v", err)
	}
}

func TestMapping_AtomicReplaceSuccessPersistsFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	path := filepath.Join(tempDir, "state.json")
	payload := []byte(`{"done":true}`)
	if err := atomicReplace(path, payload); err != nil {
		t.Fatalf("atomicReplace() = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read replaced file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("unexpected file contents: %q", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestMapping_TrackedCollectionsReturnCopies(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mapping-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	m, err := LoadMapping(tempDir, "10.0.0.1")
	if err != nil {
		t.Fatalf("LoadMapping() = %v", err)
	}
	if err := m.Set("track1.jpg", "ID1"); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if err := m.Set("track2.jpg", "ID2"); err != nil {
		t.Fatalf("Set() = %v", err)
	}

	all := m.AllContentIDs()
	tracked := m.TrackedFilenames()
	all["extra"] = "ID_EXTRA"
	tracked["extra"] = struct{}{}

	if _, ok := m.GetContentID("extra"); ok {
		t.Fatal("mutating AllContentIDs should not affect mapping data")
	}
	if _, ok := m.TrackedFilenames()["extra"]; ok {
		t.Fatal("mutating TrackedFilenames should not affect mapping data")
	}
	if _, ok := m.GetContentID("track1.jpg"); !ok {
		t.Fatal("expected track1.jpg to remain mapped after mutating copies")
	}
}
