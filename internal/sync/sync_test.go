package sync

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestMappingRoundtrip(t *testing.T) {
	dir := t.TempDir()

	m, err := LoadMapping(dir, "192.168.1.100")
	if err != nil {
		t.Fatalf("LoadMapping: %v", err)
	}

	m.Set("photo.jpg", "cid001")
	m.Set("sunset.jpg", "cid002")

	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify.
	m2, err := LoadMapping(dir, "192.168.1.100")
	if err != nil {
		t.Fatalf("reload LoadMapping: %v", err)
	}

	cid, ok := m2.GetContentID("photo.jpg")
	if !ok || cid != "cid001" {
		t.Errorf("photo.jpg: got (%q, %v), want (cid001, true)", cid, ok)
	}

	m2.Delete("photo.jpg")
	if _, ok := m2.GetContentID("photo.jpg"); ok {
		t.Error("photo.jpg should be deleted")
	}

	all := m2.AllContentIDs()
	if len(all) != 1 {
		t.Errorf("expected 1 entry after delete, got %d", len(all))
	}
}

func TestDetermineBrightness(t *testing.T) {
	manual := 5
	cfg := &config.Config{
		ManualBrightness: &manual,
		SolarEnabled:     false,
	}
	got := determineBrightness(cfg, slog.Default())
	if got == nil || *got != 5 {
		t.Errorf("expected 5, got %v", got)
	}

	cfg.ManualBrightness = nil
	got = determineBrightness(cfg, slog.Default())
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

const (
	valID1       = "id1"
	valIDA       = "id-a"
	valIDB       = "id-b"
	valIDC       = "id-c"
	valID123     = "id-123"
	valIDOld     = "id-old"
	valIDNew     = "id-new"
	valIDUnknown = "id-unknown"
	valIDKeep    = "id-keep"
	testJPG      = "test.jpg"
	testAJPG     = "a.jpg"
	testBJPG     = "b.jpg"
	testCJPG     = "c.jpg"
	testOldJPG   = "old.jpg"
	testNewJPG   = "new.jpg"
	testStayJPG  = "stay.jpg"
)

func TestMapping_Set(t *testing.T) {
	m := &Mapping{
		data: make(map[string]string),
	}

	m.Set(testJPG, valID1)
	if m.data[testJPG] != valID1 {
		t.Errorf("expected id1, got %s", m.data[testJPG])
	}

	m.Set(testJPG, "id2")
	if m.data[testJPG] != "id2" {
		t.Errorf("expected id2, got %s", m.data[testJPG])
	}
}

func TestMapping_Delete(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testJPG: valID1,
		},
	}

	m.Delete(testJPG)
	if _, ok := m.data[testJPG]; ok {
		t.Error("expected test.jpg to be deleted")
	}

	// Deleting non-existent should not panic
	m.Delete("nonexistent.jpg")
}

func TestMapping_GetContentID(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testJPG: valID1,
		},
	}

	id, ok := m.GetContentID(testJPG)
	if !ok || id != valID1 {
		t.Errorf("expected (id1, true), got (%s, %v)", id, ok)
	}

	id, ok = m.GetContentID("missing.jpg")
	if ok || id != "" {
		t.Errorf("expected (empty, false), got (%s, %v)", id, ok)
	}
}

func TestMapping_GetFilename(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testJPG: valID1,
		},
	}

	file, ok := m.GetFilename(valID1)
	if !ok || file != testJPG {
		t.Errorf("expected (test.jpg, true), got (%s, %v)", file, ok)
	}

	file, ok = m.GetFilename("missing_id")
	if ok || file != "" {
		t.Errorf("expected (empty, false), got (%s, %v)", file, ok)
	}
}

func TestMapping_AllContentIDs(t *testing.T) {
	initial := map[string]string{
		testAJPG: valIDA,
		testBJPG: valIDB,
	}
	m := &Mapping{
		data: map[string]string{
			testAJPG: valIDA,
			testBJPG: valIDB,
		},
	}

	got := m.AllContentIDs()
	if !reflect.DeepEqual(got, initial) {
		t.Errorf("expected %v, got %v", initial, got)
	}

	// Modifying copy should not affect original
	got["c.jpg"] = "id-c"
	if _, ok := m.data["c.jpg"]; ok {
		t.Error("modifying copy affected internal state")
	}
}

func TestMapping_TrackedFilenames(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testAJPG: valIDA,
			testBJPG: valIDB,
		},
	}
	expected := map[string]struct{}{
		testAJPG: {},
		testBJPG: {},
	}

	got := m.TrackedFilenames()
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("expected %v, got %v", expected, got)
	}
}

func TestMapping_DeleteBatch(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testAJPG: valIDA,
			testBJPG: valIDB,
			testCJPG: valIDC,
		},
	}

	m.DeleteBatch([]string{testAJPG, testCJPG})

	if _, ok := m.data[testAJPG]; ok {
		t.Error("expected a.jpg to be deleted")
	}
	if _, ok := m.data[testCJPG]; ok {
		t.Error("expected c.jpg to be deleted")
	}
	if id, ok := m.data[testBJPG]; !ok || id != valIDB {
		t.Error("expected b.jpg to remain")
	}
}

func TestMapping_Rename(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			testOldJPG: valID123,
		},
	}

	// Successful rename
	ok := m.Rename("old.jpg", "new.jpg")
	if !ok {
		t.Error("expected rename to return true")
	}
	if _, ok := m.data["old.jpg"]; ok {
		t.Error("expected old.jpg to be removed")
	}
	if id, ok := m.data["new.jpg"]; !ok || id != "id-123" {
		t.Errorf("expected new.jpg to have id-123, got %s", id)
	}

	// Unsuccessful rename
	ok = m.Rename("missing.jpg", "other.jpg")
	if ok {
		t.Error("expected rename of missing file to return false")
	}
}
