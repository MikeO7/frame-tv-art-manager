//nolint:goconst
package sync

import (
	"reflect"
	"testing"
)

func TestMapping_Set(t *testing.T) {
	m := &Mapping{
		data: make(map[string]string),
	}

	m.Set("test.jpg", "id1")
	if m.data["test.jpg"] != "id1" {
		t.Errorf("expected id1, got %s", m.data["test.jpg"])
	}

	m.Set("test.jpg", "id2")
	if m.data["test.jpg"] != "id2" {
		t.Errorf("expected id2, got %s", m.data["test.jpg"])
	}
}

func TestMapping_Delete(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			"test.jpg": "id1",
		},
	}

	m.Delete("test.jpg")
	if _, ok := m.data["test.jpg"]; ok {
		t.Error("expected test.jpg to be deleted")
	}

	// Deleting non-existent should not panic
	m.Delete("nonexistent.jpg")
}

func TestMapping_GetContentID(t *testing.T) {
	m := &Mapping{
		data: map[string]string{
			"test.jpg": "id1",
		},
	}

	id, ok := m.GetContentID("test.jpg")
	if !ok || id != "id1" {
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
			"test.jpg": "id1",
		},
	}

	file, ok := m.GetFilename("id1")
	if !ok || file != "test.jpg" {
		t.Errorf("expected (test.jpg, true), got (%s, %v)", file, ok)
	}

	file, ok = m.GetFilename("missing_id")
	if ok || file != "" {
		t.Errorf("expected (empty, false), got (%s, %v)", file, ok)
	}
}

func TestMapping_AllContentIDs(t *testing.T) {
	initial := map[string]string{
		"a.jpg": "id-a",
		"b.jpg": "id-b",
	}
	m := &Mapping{
		data: map[string]string{
			"a.jpg": "id-a",
			"b.jpg": "id-b",
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
			"a.jpg": "id-a",
			"b.jpg": "id-b",
		},
	}
	expected := map[string]struct{}{
		"a.jpg": {},
		"b.jpg": {},
	}

	got := m.TrackedFilenames()
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("expected %v, got %v", expected, got)
	}
}
